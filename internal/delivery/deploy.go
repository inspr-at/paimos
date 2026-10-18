// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/jackc/pgx/v5"
)

const deploymentRecorded = "deployment.recorded"
const deploymentLimit = 1000

type deployInput struct {
	Environment string `json:"environment"`
	SHA         string `json:"sha"`
	Version     string `json:"version"`
}

// Deploy records live confirmation in the tenant's append-only audit log.
// Its tenant-fenced identity is (project, environment, sha), independent of
// version labels, deployment order and the repository's later availability.
type Deploy struct {
	EventID     int64                              `json:"event_id,omitempty"`
	Project     string                             `json:"project_id"`
	Environment string                             `json:"environment"`
	Repository  string                             `json:"repository"`
	SHA         string                             `json:"sha"`
	Version     string                             `json:"version"`
	ConfirmedAt time.Time                          `json:"confirmed_at"`
	ConfirmedBy string                             `json:"confirmed_by"`
	Tickets     []statusautopilot.DeploymentResult `json:"tickets"`
}

type deployMerge struct {
	NodeID string
	SHA    string
	At     time.Time
}

func readDeployTx(ctx context.Context, tx pgx.Tx, project string, in deployInput) (*Deploy, error) {
	var raw []byte
	var id int64
	err := tx.QueryRow(ctx, `SELECT id,after FROM events WHERE node_id=$1 AND type='deployment.recorded'
 AND after->>'environment'=$2 AND after->>'sha'=$3 ORDER BY id LIMIT 1`, project, in.Environment, in.SHA).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out Deploy
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out.EventID = id
	return &out, nil
}

// Editable fields and PR titles are not merge proof. Keep the newest recorded
// merged delivery for each current project work item, excluding undone facts.
func deployMergesTx(ctx context.Context, tx pgx.Tx, project, repository string) ([]deployMerge, error) {
	rows, err := tx.Query(ctx, `SELECT n.id::text,f.sha,f.at FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 JOIN LATERAL (
  SELECT fact.sha,fact.at FROM (
   SELECT e.metadata->>'merge_sha' sha,(e.metadata->>'merged_at')::timestamptz at FROM events e
   WHERE e.node_id=n.id AND e.type='delivery.merge_done' AND e.metadata->>'repository'=$2
   AND NOT EXISTS(SELECT 1 FROM events u WHERE u.tenant_id=e.tenant_id AND u.undo_of=e.id)
   UNION ALL
   SELECT a.merge_sha,a.at FROM delivery_merge_audit a WHERE a.ticket_node_id=n.id
   AND a.project_id=$1 AND a.repository=$2 AND a.pull_request IS NOT NULL
  ) fact ORDER BY fact.at DESC,fact.sha LIMIT 1
 ) f ON true
 WHERE n.project_id=$1 AND n.deleted_at IS NULL AND k.slug='work'
 AND regexp_replace(lower(btrim(n.state)), '[[:space:]-]+', '_', 'g')='done'
 ORDER BY n.id LIMIT $3`, project, repository, deploymentLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []deployMerge{}
	for rows.Next() {
		var f deployMerge
		if err := rows.Scan(&f.NodeID, &f.SHA, &f.At); err != nil {
			return nil, err
		}
		if !reviewgate.ValidSHA(f.SHA) || f.At.IsZero() {
			return nil, errRead
		}
		out = append(out, f)
	}
	if len(out) > deploymentLimit {
		return nil, fail(422, "deploy candidate inventory exceeds 1000 tickets; no deploy was recorded")
	}
	return out, rows.Err()
}

func deploySourceTx(ctx context.Context, tx pgx.Tx, project string) (string, error) {
	s, err := loadMetricSourceTx(ctx, tx, project, false)
	if err != nil {
		return "", err
	}
	if s == nil || !reviewgate.ValidRepository(s.Repository) {
		return "", fail(409, "link the project's delivery repository before recording a deploy")
	}
	return s.Repository, nil
}

func (m *Module) recordDeploy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	project = strings.ToLower(project)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	if err := httpapi.BufferRequestBodyTimeout(w, r, 4<<10, 5*time.Second); err != nil {
		respondError(w, fail(400, "invalid deploy body"))
		return
	}
	var in deployInput
	if err := decodeBounded(w, r, 4<<10, &in); err != nil {
		respondError(w, err)
		return
	}
	in.Environment, in.Version, in.SHA = strings.TrimSpace(in.Environment), strings.TrimSpace(in.Version), strings.ToLower(in.SHA)
	if in.Environment == "" || len(in.Environment) > 128 || len(in.Version) > 128 || !reviewgate.ValidSHA(in.SHA) || strings.ContainsFunc(in.Environment+in.Version, unicode.IsControl) {
		respondError(w, fail(400, "environment and full commit SHA are required; environment and version are limited to 128 bytes"))
		return
	}
	var repository string
	var merges []deployMerge
	var out *Deploy
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "releases.deploy", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var err error
		out, err = readDeployTx(ctx, tx, project, in)
		if err != nil || out != nil {
			return err
		}
		if repository, err = deploySourceTx(ctx, tx, project); err != nil {
			return err
		}
		merges, err = deployMergesTx(ctx, tx, project, repository)
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	if out != nil {
		httpapi.WriteJSON(w, 200, out)
		return
	}
	shas := make([]string, 0, len(merges))
	for _, f := range merges {
		shas = append(shas, f.SHA)
	}
	slices.Sort(shas)
	shas = slices.Compact(shas)
	var ancestors map[string]bool
	read := func(get func(string, any) error) error {
		var err error
		ancestors, err = deploymentAncestors(get, repository, in.SHA, shas)
		return err
	}
	if reader, ok := m.github.(deploymentReader); ok {
		err = reader.DeploymentRead(ctx, p.TenantID, repository, read)
	} else {
		err = publicDeploymentRead(ctx, repository, read)
	}
	if err != nil {
		var f *apiError
		if !errors.As(err, &f) {
			err = fail(502, "complete deploy ancestry could not be verified; no deploy was recorded")
		}
		respondError(w, err)
		return
	}
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(ctx, tx); err != nil {
			return err
		}
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "releases.deploy", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var err error
		out, err = readDeployTx(ctx, tx, project, in)
		if err != nil || out != nil {
			return err
		}
		currentRepository, err := deploySourceTx(ctx, tx, project)
		if err != nil {
			return err
		}
		currentMerges, err := deployMergesTx(ctx, tx, project, currentRepository)
		if err != nil {
			return err
		}
		if repository != currentRepository || !slices.Equal(merges, currentMerges) {
			return fail(409, "project repository or merged deliveries changed; retry the live confirmation")
		}
		ids := []string{}
		for _, f := range merges {
			if ancestors[f.SHA] {
				ids = append(ids, f.NodeID)
			}
		}
		// Prelock the audit foreign keys before publication takes the counter.
		if err := lockMergeEventParentsTx(ctx, tx, p, &project); err != nil {
			return err
		}
		var at time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
			return err
		}
		anchor := "deploy/" + digestJSON([]string{project, in.Environment, in.SHA})
		source := fmt.Sprintf("%s / %s at %s", repository, in.Environment, in.SHA)
		if in.Version != "" {
			source += " (" + in.Version + ")"
		}
		tickets, err := statusautopilot.DeliverDeploymentTx(ctx, tx, p, project, anchor, source, ids, at)
		if err != nil {
			return err
		}
		out = &Deploy{Project: project, Environment: in.Environment, Repository: repository, SHA: in.SHA, Version: in.Version, ConfirmedAt: at, ConfirmedBy: p.ID, Tickets: tickets}
		event, err := events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: deploymentRecorded, After: *out})
		if err == nil {
			out.EventID = event.ID
		}
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
