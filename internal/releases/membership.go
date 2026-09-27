// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type ticketOption struct {
	NodeID       string  `json:"ticket_node_id"`
	Key          string  `json:"key"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	Type         string  `json:"type"`
	FeatureID    *string `json:"feature_node_id"`
	ReleaseID    *string `json:"release_node_id"`
	ReleaseTitle *string `json:"release_title"`
	Availability string  `json:"availability"`
}
type optionsResult struct {
	Revision int64          `json:"expected_revision"`
	Tickets  []ticketOption `json:"tickets"`
}
type membershipInput struct {
	Revision    int64    `json:"expected_revision"`
	IDs         []string `json:"ticket_node_ids"`
	ConfirmMove bool     `json:"confirm_move"`
}
type membershipResult struct {
	Walker  Walker `json:"walker"`
	EventID int64  `json:"event_id"`
}
type memberState struct {
	TicketID      string  `json:"ticket_node_id"`
	Exists        bool    `json:"exists"`
	ReleaseID     *string `json:"release_node_id"`
	Position      int     `json:"walker_position"`
	ScopeRequired bool    `json:"scope_revision_required"`
}
type membershipSnapshot struct {
	ProjectID       string        `json:"project_node_id"`
	ReleaseID       string        `json:"release_node_id"`
	ProjectRevision int64         `json:"project_revision"`
	ReleaseRevision int64         `json:"release_revision"`
	Members         []memberState `json:"members"`
}

func (m *module) ticketOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	project, release := strings.ToLower(r.PathValue("projectId")), strings.ToLower(r.PathValue("releaseId"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	epic := strings.TrimSpace(r.URL.Query().Get("epic"))
	kind := strings.TrimSpace(r.URL.Query().Get("type"))
	if len(q) > 256 || len(status) > 64 || (epic != "" && !uuid.MatchString(epic)) || (kind != "" && kind != "ticket" && kind != "task") {
		respond(w, nil, fail(400, "invalid ticket filter"))
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			respond(w, nil, fail(400, "limit must be 1..100"))
			return
		}
		limit = n
	}
	out := optionsResult{Tickets: []ticketOption{}}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "releases.read", authz.Scope{ProjectID: project}) != nil {
			return fail(403, "project access required")
		}
		var state string
		if err := tx.QueryRow(r.Context(), `SELECT r.state,r.revision FROM journey_releases r JOIN journey_projects j ON j.tenant_id=r.tenant_id AND j.project_node_id=r.project_node_id JOIN nodes rn ON rn.tenant_id=r.tenant_id AND rn.id=r.release_node_id JOIN nodes pn ON pn.tenant_id=r.tenant_id AND pn.id=r.project_node_id WHERE r.project_node_id=$1 AND r.release_node_id=$2 AND j.current_release_node_id=r.release_node_id AND rn.deleted_at IS NULL AND pn.deleted_at IS NULL`, project, release).Scan(&state, &out.Revision); err != nil {
			return err
		}
		if state != "planning" {
			return fail(409, "release is not planning")
		}
		rows, err := tx.Query(r.Context(), `SELECT n.id::text,n.key,n.title,n.state,k.slug,
   coalesce(j.feature_node_id,e.id)::text,j.release_node_id::text,rn.title,other.state
   FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   LEFT JOIN journey_tickets j ON j.tenant_id=n.tenant_id AND j.ticket_node_id=n.id
   LEFT JOIN nodes e ON e.tenant_id=n.tenant_id AND e.id=n.parent_id AND e.deleted_at IS NULL
    AND EXISTS(SELECT 1 FROM node_kinds ek WHERE ek.tenant_id=e.tenant_id AND ek.id=e.kind_id AND ek.slug='epic')
   LEFT JOIN nodes rn ON rn.tenant_id=j.tenant_id AND rn.id=j.release_node_id AND rn.deleted_at IS NULL
   LEFT JOIN journey_releases other ON other.tenant_id=j.tenant_id AND other.project_node_id=j.project_node_id AND other.release_node_id=rn.id
   WHERE n.project_id=$1 AND n.deleted_at IS NULL AND k.slug IN ('ticket','task')
    AND ($2='' OR n.key ILIKE '%'||$2||'%' OR n.title ILIKE '%'||$2||'%')
    AND ($3='' OR n.state=$3) AND ($4='' OR coalesce(j.feature_node_id,e.id)=nullif($4,'')::uuid)
    AND ($5='' OR k.slug=$5)
   ORDER BY n.key,n.id LIMIT $6`, project, q, status, epic, kind, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t ticketOption
			var releaseState *string
			if err := rows.Scan(&t.NodeID, &t.Key, &t.Title, &t.Status, &t.Type, &t.FeatureID, &t.ReleaseID, &t.ReleaseTitle, &releaseState); err != nil {
				return err
			}
			switch {
			case t.Type != "ticket":
				t.Availability = "unsupported"
			case closedTicketState(t.Status):
				t.Availability = "closed"
			case t.ReleaseID == nil:
				t.Availability = "addable"
			case *t.ReleaseID == release:
				t.Availability = "included"
			case releaseState != nil && (*releaseState == "released" || *releaseState == "superseded"):
				t.Availability = "released"
			case releaseState != nil && *releaseState == "planning":
				t.Availability = "other_release"
			default:
				t.Availability = "active_release"
			}
			out.Tickets = append(out.Tickets, t)
		}
		return rows.Err()
	})
	respond(w, out, err)
}

func (m *module) addMembership(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	var in membershipInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || in.Revision < 1 || len(in.IDs) < 1 || len(in.IDs) > 100 {
		respond(w, nil, fail(400, "expected_revision and 1..100 ticket_node_ids required"))
		return
	}
	seen := map[string]bool{}
	for i, id := range in.IDs {
		id = strings.ToLower(id)
		if !uuid.MatchString(id) || seen[id] {
			respond(w, nil, fail(400, "ticket_node_ids must be unique UUIDs"))
			return
		}
		seen[id] = true
		in.IDs[i] = id
	}
	var out membershipResult
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "releases.write", authz.Scope{ProjectID: r.PathValue("projectId")}) != nil {
			return fail(403, "project access required")
		}
		var err error
		out, err = addExisting(r.Context(), tx, p, strings.ToLower(r.PathValue("projectId")), strings.ToLower(r.PathValue("releaseId")), in)
		return err
	})
	respond(w, out, err)
}

func addExisting(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, release string, in membershipInput) (membershipResult, error) {
	var result membershipResult
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0))`); err != nil {
		return result, err
	}
	var person bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind='person')`, p.ID).Scan(&person); err != nil {
		return result, err
	}
	if !person {
		return result, fail(403, "person required")
	}
	var current *string
	var projectRev int64
	if err := tx.QueryRow(ctx, `SELECT j.current_release_node_id::text,j.revision FROM journey_projects j JOIN nodes n ON n.tenant_id=j.tenant_id AND n.id=j.project_node_id WHERE j.project_node_id=$1 AND n.deleted_at IS NULL FOR UPDATE OF j`, project).Scan(&current, &projectRev); err != nil {
		return result, err
	}
	var state string
	var rev int64
	if err := tx.QueryRow(ctx, `SELECT r.state,r.revision FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id WHERE r.project_node_id=$1 AND r.release_node_id=$2 AND n.deleted_at IS NULL FOR UPDATE OF r`, project, release).Scan(&state, &rev); err != nil {
		return result, err
	}
	if state != "planning" || current == nil || *current != release {
		return result, fail(409, "only the current planning release can change")
	}
	if rev != in.Revision {
		return result, fail(409, "release revision changed")
	}
	before := membershipSnapshot{ProjectID: project, ReleaseID: release, ProjectRevision: projectRev, ReleaseRevision: rev, Members: make([]memberState, 0, len(in.IDs))}
	after := before
	after.Members = make([]memberState, 0, len(in.IDs))
	after.ProjectRevision++
	after.ReleaseRevision++
	oldReleases := map[string]bool{}
	for _, id := range in.IDs {
		var nodeProject, kind, status string
		var feature *string
		err := tx.QueryRow(ctx, `SELECT n.project_id::text,k.slug,n.state,
    CASE WHEN ek.slug='epic' THEN e.id::text ELSE NULL END
    FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
    LEFT JOIN nodes e ON e.tenant_id=n.tenant_id AND e.id=n.parent_id AND e.deleted_at IS NULL
    LEFT JOIN node_kinds ek ON ek.tenant_id=e.tenant_id AND ek.id=e.kind_id
    WHERE n.id=$1 AND n.deleted_at IS NULL FOR SHARE OF n`, id).Scan(&nodeProject, &kind, &status, &feature)
		if err == pgx.ErrNoRows {
			return result, fail(404, "ticket not found")
		}
		if err != nil {
			return result, err
		}
		if nodeProject != project || kind != "ticket" {
			return result, fail(404, "ticket not found in project")
		}
		if closedTicketState(status) {
			return result, fail(409, "closed tickets cannot be added")
		}
		old := memberState{TicketID: id}
		var existingFeature *string
		err = tx.QueryRow(ctx, `SELECT release_node_id::text,walker_position,scope_revision_required,feature_node_id::text FROM journey_tickets WHERE project_node_id=$1 AND ticket_node_id=$2 FOR UPDATE`, project, id).Scan(&old.ReleaseID, &old.Position, &old.ScopeRequired, &existingFeature)
		if err != nil && err != pgx.ErrNoRows {
			return result, err
		}
		old.Exists = err == nil
		if old.ReleaseID != nil {
			if *old.ReleaseID == release {
				return result, fail(409, "ticket is already included")
			}
			var oldState string
			if err := tx.QueryRow(ctx, `SELECT r.state FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL WHERE r.project_node_id=$1 AND r.release_node_id=$2 FOR UPDATE OF r`, project, *old.ReleaseID).Scan(&oldState); err == pgx.ErrNoRows {
				return result, fail(409, "source release is unavailable")
			} else if err != nil {
				return result, err
			}
			if oldState != "planning" {
				return result, fail(409, "released or active tickets cannot move")
			}
			if !in.ConfirmMove {
				return result, fail(409, "confirm_move required to move a ticket from another release")
			}
			oldReleases[*old.ReleaseID] = true
		}
		if !old.Exists {
			// Only an epic registered for this journey may be projected as a feature.
			if feature != nil {
				var registered bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_features WHERE project_node_id=$1 AND feature_node_id=$2)`, project, *feature).Scan(&registered); err != nil {
					return result, err
				}
				if !registered {
					feature = nil
				}
			}
			var pos int
			if err := tx.QueryRow(ctx, `SELECT coalesce(max(walker_position)+1,0) FROM journey_tickets WHERE project_node_id=$1`, project).Scan(&pos); err != nil {
				return result, err
			}
			_, err = tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,feature_node_id,release_node_id,walker_position,source,scope_revision_required) VALUES($1,$2,$3,$4,$5,$6,'manual',true)`, p.TenantID, id, project, feature, release, pos)
			if err != nil {
				return result, err
			}
			after.Members = append(after.Members, memberState{TicketID: id, Exists: true, ReleaseID: &release, Position: pos, ScopeRequired: true})
		} else {
			if _, err := tx.Exec(ctx, `UPDATE journey_tickets SET release_node_id=$2,scope_revision_required=true WHERE ticket_node_id=$1`, id, release); err != nil {
				return result, err
			}
			after.Members = append(after.Members, memberState{TicketID: id, Exists: true, ReleaseID: &release, Position: old.Position, ScopeRequired: true})
		}
		before.Members = append(before.Members, old)
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_releases SET revision=revision+1,access_required=EXISTS(SELECT 1 FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id WHERE t.release_node_id=$1 AND t.access_change AND n.deleted_at IS NULL) WHERE release_node_id=$1`, release); err != nil {
		return result, err
	}
	for id := range oldReleases {
		if _, err := tx.Exec(ctx, `UPDATE journey_releases SET revision=revision+1,access_required=EXISTS(SELECT 1 FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id WHERE t.release_node_id=$1 AND t.access_change AND n.deleted_at IS NULL) WHERE release_node_id=$1`, id); err != nil {
			return result, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_projects SET revision=revision+1,updated_at=now() WHERE project_node_id=$1`, project); err != nil {
		return result, err
	}
	event, err := events.Append(ctx, tx, p, events.Change{NodeID: &release, Type: "journey.release_membership_changed", Before: before, After: after})
	if err != nil {
		return result, err
	}
	result.Walker, err = load(ctx, tx, project, release)
	result.EventID = event.ID
	return result, err
}

// AddExistingToNewRelease is used by the journey action after it creates a
// planning release in the same transaction. Any rejected ticket rolls back
// release creation and the action receipt with it.
func AddExistingToNewRelease(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, release string, ids []string) error {
	if authz.RequireTx(ctx, tx, p, "releases.write", authz.Scope{ProjectID: project}) != nil {
		return fail(403, "project access required")
	}
	_, err := addExisting(ctx, tx, p, project, release, membershipInput{Revision: 1, IDs: ids})
	return err
}

func closedTicketState(state string) bool {
	switch state {
	case "accepted", "delivered", "done", "cancelled", "canceled", "archived", "closed":
		return true
	default:
		return false
	}
}

// MembershipFailure exposes safe validation outcomes to the journey action.
func MembershipFailure(err error) (int, string, bool) {
	var f *failure
	if !errors.As(err, &f) {
		return 0, "", false
	}
	return f.code, f.message, true
}
