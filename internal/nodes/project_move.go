// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

var projectPrefixRE = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

type projectMoveResult struct {
	IssueID   string   `json:"issue_id"`
	OldKey    string   `json:"old_key"`
	NewKey    string   `json:"new_key"`
	ProjectID string   `json:"project_id"`
	Detached  []string `json:"detached"`
	Notes     []string `json:"notes"`
}

type journeyMembership struct {
	ProjectID             string  `json:"project_id"`
	FeatureID             *string `json:"feature_id"`
	ReleaseID             *string `json:"release_id"`
	WalkerPosition        int     `json:"walker_position"`
	Source                string  `json:"source"`
	ScopeRevisionRequired bool    `json:"scope_revision_required"`
	AccessChange          bool    `json:"access_change"`
	EstimatedHours        *string `json:"estimated_hours"`
}

type projectMoveSnapshot struct {
	Node    nodeJSON           `json:"node"`
	Journey *journeyMembership `json:"journey,omitempty"`
}

func loadJourneyMembership(ctx context.Context, tx pgx.Tx, id string) (*journeyMembership, error) {
	var m journeyMembership
	err := tx.QueryRow(ctx, `SELECT project_node_id::text,feature_node_id::text,release_node_id::text,walker_position,source,scope_revision_required,access_change,estimated_hours::text
	 FROM journey_tickets WHERE ticket_node_id=$1::uuid FOR UPDATE`, id).
		Scan(&m.ProjectID, &m.FeatureID, &m.ReleaseID, &m.WalkerPosition, &m.Source, &m.ScopeRevisionRequired, &m.AccessChange, &m.EstimatedHours)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Module) handleGetNodeByKey(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		writeErr(w, badRequest("invalid node key"))
		return
	}
	var node nodeJSON
	err := m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		id := ""
		err := tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE key=$1 AND deleted_at IS NULL
		 UNION ALL SELECT a.node_id::text FROM node_key_aliases a JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.node_id
		 WHERE a.key=$1 AND n.deleted_at IS NULL LIMIT 1`, key).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("node not found")
		}
		if err != nil {
			return err
		}
		node, err = loadNode(ctx, tx, id, false)
		if err != nil {
			return err
		}
		recurrences, err := loadNodeRecurrences(ctx, tx, []string{id})
		if err != nil {
			return err
		}
		node.Recurrence = recurrences[id]
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (m *Module) handleProjectMove(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var in struct {
		ProjectID string `json:"project_id"`
	}
	if err := decodeJSON(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	projectID, valid := parseUUID(in.ProjectID)
	if !valid {
		writeErr(w, badRequest("invalid project_id"))
		return
	}
	result, err := m.projectMove(r.Context(), p, id, projectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (m *Module) projectMove(ctx context.Context, p tenant.Principal, id, projectID string) (projectMoveResult, error) {
	var result projectMoveResult
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := armPortalModeration(ctx, tx, p); err != nil {
			return err
		}
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		current, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		var kind string
		if err := tx.QueryRow(ctx, `SELECT slug FROM node_kinds WHERE id=$1::uuid`, current.KindID).Scan(&kind); err != nil {
			return err
		}
		if kind != "epic" && kind != "ticket" && kind != "task" {
			return badRequest("only issues can move between projects")
		}
		var nested, feature bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE parent_id=$1::uuid AND deleted_at IS NULL),
		 EXISTS(SELECT 1 FROM journey_features WHERE feature_node_id=$1::uuid)`, id).Scan(&nested, &feature); err != nil {
			return err
		}
		if nested || feature {
			return conflict("issue has child nodes or a feature projection")
		}
		var prefix string
		err = tx.QueryRow(ctx, `SELECT coalesce(nullif(fields->>'project_key',''),nullif(fields->'classic'->>'key',''),split_part(key,'-',1))
		 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		 WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug='project'`, projectID).Scan(&prefix)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("project not found")
		}
		if err != nil {
			return err
		}
		if !projectPrefixRE.MatchString(prefix) {
			return badRequest("project key cannot allocate issue keys")
		}
		if err := ensureParentAllows(ctx, tx, projectID, kind); err != nil {
			return err
		}
		if err := requireMoveTarget(ctx, tx, p, id, &projectID); err != nil {
			return err
		}
		var sourceProject string
		err = tx.QueryRow(ctx, `WITH RECURSIVE ancestors AS (
		 SELECT n.id,n.parent_id,n.kind_id FROM nodes n WHERE n.id=$1::uuid
		 UNION ALL SELECT p.id,p.parent_id,p.kind_id FROM nodes p JOIN ancestors a ON p.id=a.parent_id
		) SELECT a.id::text FROM ancestors a JOIN node_kinds k ON k.id=a.kind_id AND k.slug='project' LIMIT 1`, id).Scan(&sourceProject)
		if err != nil {
			return err
		}
		if sourceProject == projectID {
			return conflict("issue already belongs to project")
		}
		beforeJourney, err := loadJourneyMembership(ctx, tx, id)
		if err != nil {
			return err
		}
		var newKey string
		if err := tx.QueryRow(ctx, `SELECT aeon_next_node_key(current_setting('aeon.tenant_id')::uuid,$1)`, prefix).Scan(&newKey); err != nil {
			return dbErr("allocate node key", err)
		}
		var position string
		if err := tx.QueryRow(ctx, `SELECT (coalesce(max(position),0)+1024)::text FROM nodes WHERE parent_id=$1::uuid AND deleted_at IS NULL`, projectID).Scan(&position); err != nil {
			return err
		}
		moved, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET key=$1,parent_id=$2::uuid,position=$3::numeric,
		 updated_at=greatest(clock_timestamp(),date_trunc('second',updated_at)+interval '1 second')
		 WHERE id=$4::uuid RETURNING `+nodeReturning, newKey, projectID, position, id))
		if err != nil {
			return dbErr("move issue", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_key_aliases(tenant_id,key,node_id) VALUES($1::uuid,$2,$3::uuid)`, p.TenantID, current.Key, id); err != nil {
			return dbErr("reserve former key", err)
		}
		afterJourney, err := transferJourneyMembership(ctx, tx, id, sourceProject, projectID, beforeJourney)
		if err != nil {
			return err
		}
		if err := m.record(ctx, tx, p.ID, &id, evNodeProjectMoved,
			projectMoveSnapshot{Node: current, Journey: beforeJourney}, projectMoveSnapshot{Node: moved, Journey: afterJourney}); err != nil {
			return err
		}
		result = projectMoveResult{IssueID: id, OldKey: current.Key, NewKey: newKey, ProjectID: projectID, Detached: []string{}, Notes: []string{}}
		if current.ParentID != nil && *current.ParentID != sourceProject {
			result.Detached = append(result.Detached, "parent")
		}
		if beforeJourney != nil {
			if beforeJourney.ReleaseID != nil {
				result.Detached = append(result.Detached, "release")
			}
			if beforeJourney.FeatureID != nil {
				result.Detached = append(result.Detached, "feature")
			}
			if afterJourney == nil {
				result.Detached = append(result.Detached, "journey")
			}
		}
		return nil
	})
	return result, err
}
