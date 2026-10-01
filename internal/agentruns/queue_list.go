// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

func (m *module) queueEntries(ctx context.Context, tx pgx.Tx) ([]queueEntry, error) {
	rows, err := tx.Query(ctx, workqueue.CTE+`SELECT queue_node_id::text,project_id::text,key,title,state,fields,id::text FROM queue_ordered ORDER BY queue_target_agent_id NULLS FIRST,queue_position`)
	if err != nil {
		return nil, err
	}
	items := []queueEntry{}
	for rows.Next() {
		var item queueEntry
		var fields []byte
		if err = rows.Scan(&item.NodeID, &item.ProjectID, &item.Key, &item.Title, &item.State, &fields, &item.Run.ID); err != nil {
			rows.Close()
			return nil, err
		}
		f := workqueue.Fields(fields)
		item.Hours = workqueue.Hours(f)
		item.Priority, _ = f["priority"].(string)
		if item.Priority == "" {
			item.Priority = "medium"
		}
		items = append(items, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].NodeID
	}
	queued, err := workqueue.Load(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Queued = queued[items[i].NodeID]
		items[i].Run, err = load(ctx, tx, items[i].Run.ID, false)
		if err != nil {
			return nil, err
		}
		p, _ := tenant.PrincipalFrom(ctx)
		project := ""
		if items[i].ProjectID != nil {
			project = *items[i].ProjectID
		}
		if p.Kind == tenant.Agent {
			err = queuePermission(ctx, tx, p, items[i].ProjectID, false)
		} else {
			err = authz.RequireTx(ctx, tx, p, "run.read", authz.Scope{ProjectID: project})
		}
		if err == nil {
			items[i].VisibleRun = &items[i].Run
		} else if !errors.Is(err, authz.ErrForbidden) {
			return nil, err
		}
	}
	return items, nil
}
func (m *module) queueEntry(ctx context.Context, tx pgx.Tx, id string) (queueEntry, error) {
	entries, err := m.queueEntries(ctx, tx)
	if err != nil {
		return queueEntry{}, err
	}
	for _, e := range entries {
		if e.NodeID == id {
			return e, nil
		}
	}
	return queueEntry{}, pgx.ErrNoRows
}
func (m *module) queueList(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	project := r.URL.Query().Get("project_id")
	if project != "" && !workorders.UUID(project) {
		return nil, workorders.Fail(400, "invalid project_id")
	}
	entries, err := m.queueEntries(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	out := queuePage{Items: []queueEntry{}}
	for _, e := range entries {
		if project != "" && (e.ProjectID == nil || *e.ProjectID != project) {
			continue
		}
		if err = queuePermission(r.Context(), tx, p, e.ProjectID, false); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, e)
		out.Capacity.QueuedHours += e.Hours
		out.Manual = out.Manual || e.Queued.Manual
	}
	out.Count = len(out.Items)
	// Capacity is advisory. Pool fingerprints prevent counting two doors into
	// one allowance twice. The actual router rechecks every grant and window.
	err = tx.QueryRow(r.Context(), `SELECT coalesce(sum(slots),0)::int FROM (
 SELECT max(a.max_parallel_runs) slots FROM agent_accounts a
 WHERE a.state='available' AND a.last_probe_ok AND a.last_probe_at>clock_timestamp()-interval '2 minutes'
 AND EXISTS(SELECT 1 FROM model_profiles m WHERE m.enabled AND m.harness=a.harness AND (a.allowed_model_profile_ids IS NULL OR m.id=ANY(a.allowed_model_profile_ids)))
 GROUP BY a.harness,coalesce(nullif(a.quota_pool_fingerprint,''),a.id::text)) pools`).Scan(&out.Capacity.Parallel)
	if err != nil {
		return nil, err
	}
	if out.Capacity.Parallel > 0 {
		hours := out.Capacity.QueuedHours / float64(out.Capacity.Parallel)
		out.Capacity.WorkHours = &hours
	}
	out.Capacity.Warning = out.Count > 0 && (out.Capacity.WorkHours == nil || *out.Capacity.WorkHours >= 4)
	return out, nil
}
func (m *module) queueMove(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Position int `json:"position"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Position < 1 {
		return nil, workorders.Fail(400, "position must be positive")
	}
	entries, err := m.queueEntries(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	shared := []queueEntry{}
	from := -1
	for _, e := range entries {
		if e.NodeID == r.PathValue("nodeId") && e.Queued.Targeted {
			return nil, workorders.Fail(409, "targeted entries cannot be reordered")
		}
		if !e.Queued.Targeted {
			if e.NodeID == r.PathValue("nodeId") {
				from = len(shared)
			}
			shared = append(shared, e)
		}
	}
	if from < 0 {
		return nil, pgx.ErrNoRows
	}
	if in.Position > len(shared) {
		return nil, workorders.Fail(400, "position is outside the queue")
	}
	for _, e := range shared {
		if err = queuePermission(r.Context(), tx, p, e.ProjectID, true); err != nil {
			return nil, err
		}
	}
	moved := shared[from]
	shared = append(shared[:from], shared[from+1:]...)
	to := in.Position - 1
	shared = append(shared, queueEntry{})
	copy(shared[to+1:], shared[to:])
	shared[to] = moved
	for i, e := range shared {
		if _, err = tx.Exec(r.Context(), `UPDATE agent_runs SET queue_rank=$2 WHERE id=$1 AND status='queued'`, e.Run.ID, i+1); err != nil {
			return nil, err
		}
	}
	if err = workorders.Record(r.Context(), tx, p, moved.NodeID, "queue.moved", map[string]int{"position": from + 1}, map[string]int{"position": in.Position}); err != nil {
		return nil, err
	}
	return m.queueList(r, tx, p)
}
func (m *module) queueReset(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	entries, err := m.queueEntries(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Queued.Targeted {
			if err = queuePermission(r.Context(), tx, p, e.ProjectID, true); err != nil {
				return nil, err
			}
		}
	}
	for _, e := range entries {
		if !e.Queued.Targeted && e.Queued.Manual {
			if _, err = tx.Exec(r.Context(), `UPDATE agent_runs SET queue_rank=NULL WHERE id=$1 AND status='queued'`, e.Run.ID); err != nil {
				return nil, err
			}
			if err = workorders.Record(r.Context(), tx, p, e.NodeID, "queue.reset", e.Queued, nil); err != nil {
				return nil, err
			}
		}
	}
	return m.queueList(r, tx, p)
}
