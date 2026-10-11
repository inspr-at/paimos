// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type LastRun struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// S21 vocabulary and sentences from AEON-1180. The prefixed helper can coexist
// with its runState implementation when the coordinator merges run history.
func lastRunState(raw string) (string, string) {
	switch raw {
	case "pending", "queued", "created":
		return "created", "The occurrence was created; execution has not started."
	case "starting", "running":
		return "running", "Execution is in progress."
	case "needs_person", "waiting_for_person":
		return "waiting_for_person", "A person must decide before execution can continue."
	case "waiting", "held", "blocked", "paused":
		return "held", "Execution is held until its required conditions are confirmed."
	case "failed", "ownership_lost":
		return "failed", "Execution failed and has not completed successfully."
	case "succeeded", "completed", "done":
		return "done", ""
	case "cancelled", "skipped":
		return "skipped", "The run was skipped or cancelled."
	default:
		return "held", "The recorded outcome is unknown and requires reconciliation."
	}
}

func lastRuns(ctx context.Context, tx pgx.Tx, items []Recurrence) error {
	if len(items) == 0 {
		return nil
	}
	if len(items) > 100 {
		return workorders.Fail(400, "routine page exceeds limits")
	}
	ids := make([]string, len(items))
	indices := map[string]int{}
	for i := range items {
		ids[i], indices[items[i].ID] = items[i].ID, i
	}
	rows, err := tx.Query(ctx, `SELECT page.id::text,r.id::text,r.state,r.created_at
 FROM unnest($1::uuid[]) page(id)
 CROSS JOIN LATERAL (SELECT id,state,created_at FROM routine_runs
 WHERE recurrence_id=page.id ORDER BY created_at DESC,id DESC LIMIT 1) r`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var recurrence, raw string
		var run LastRun
		if err = rows.Scan(&recurrence, &run.ID, &raw, &run.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		run.State, run.Reason = lastRunState(raw)
		run.CreatedAt = run.CreatedAt.UTC()
		items[indices[recurrence]].LastRun = &run
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// S21 confirms process evidence before presenting a successful outcome.
	for i := range items {
		run := items[i].LastRun
		if run == nil || run.State != "done" {
			continue
		}
		var proven, failed, confirmed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1),
 EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1 AND (state IN ('failed','ownership_lost') OR (process_receipt->>'exit_confirmed'='true' AND CASE WHEN process_receipt->>'exit_code' ~ '^-?[0-9]{1,9}$' THEN (process_receipt->>'exit_code')::bigint<>0 ELSE false END))),
 NOT EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1 AND (state NOT IN ('done','completed','succeeded') OR agent_run_id IS NULL OR process_receipt->>'exit_confirmed' IS DISTINCT FROM 'true' OR process_receipt->>'exit_code' IS DISTINCT FROM '0'))`, run.ID).Scan(&proven, &failed, &confirmed)
		if err != nil {
			return err
		}
		if failed {
			run.State, run.Reason = lastRunState("failed")
		} else if !proven || !confirmed {
			run.State, run.Reason = "held", "Successful execution has not been confirmed by process evidence."
		}
	}
	return nil
}

func (m *Module) getDraftGuardrails(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if len(r.URL.RawQuery) > 256 {
		reply(w, 0, nil, workorders.Fail(400, "guardrail query exceeds limits"))
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "scope" && key != "project_id") || len(values) != 1 || values[0] == "" {
			reply(w, 0, nil, workorders.Fail(400, "invalid guardrail query"))
			return
		}
	}
	scope, project := query.Get("scope"), query.Get("project_id")
	if (scope != "personal" && scope != "project" && scope != "workspace") || (scope == "project") != (project != "") || project != "" && !workorders.UUID(project) {
		reply(w, 0, nil, workorders.Fail(400, "invalid guardrail scope"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var out routineguard.Policy
	err := db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, readPermission(p), authz.Scope{ProjectID: project, AnyProject: scope == "personal"}); err != nil {
			return err
		}
		item := Recurrence{Input: Input{Definition: &Definition{Scope: DefinitionScope{Kind: scope, ProjectID: project}}}}
		if scope == "personal" {
			if p.Kind != tenant.Person {
				return authz.ErrForbidden
			}
			owner, err := canonicalPerson(ctx, tx, p)
			if err != nil {
				return err
			}
			item.Definition.OwnerPrincipalID = owner
		}
		if scope == "project" {
			var id string
			if err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL`, project).Scan(&id); err != nil {
				return err
			}
		}
		sources, err := guardSources(ctx, tx, p, item)
		if err != nil {
			return err
		}
		out, err = routineguard.Resolve(sources)
		if err != nil {
			return workorders.Fail(409, "guardrail policy needs revision or administrator evidence")
		}
		return nil
	})
	reply(w, 200, out, err)
}
