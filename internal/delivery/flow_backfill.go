// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const flowBackfillLimit = 4000

type FlowBackfillItem struct {
	ID    string  `json:"id"`
	Ref   string  `json:"ref"`
	PRs   []int64 `json:"prs"`
	Steps int     `json:"candidate_steps"`
}

type FlowBackfillPlan struct {
	Schema     string             `json:"schema"`
	Tenant     string             `json:"tenant_id"`
	Project    string             `json:"project_id"`
	Repository string             `json:"repository"`
	DryRun     bool               `json:"dry_run"`
	LinkedPRs  []int64            `json:"linked_prs"`
	Items      []FlowBackfillItem `json:"items"`
	Events     []int64            `json:"source_event_ids"`
	// nil in a dry run: candidates are not a claim about changed rows.
	Changed *int `json:"changed"`
}

// BackfillFlow is the OPS-only tool path, not an HTTP route or a startup job.
// Planning uses a READ ONLY snapshot, never writes then rolls back. Applying
// requires current delivery.manage authority under flowWrite's tenant fence.
// It only projects linked change items; release reports and cursors are untouched.
func (m *Module) BackfillFlow(ctx context.Context, p tenant.Principal, project string, expectedPRs int, apply bool) (FlowBackfillPlan, error) {
	out := FlowBackfillPlan{Schema: "aeon.flow.backfill.v1", Tenant: p.TenantID, Project: project, Repository: m.config.Repository, DryRun: !apply, Items: []FlowBackfillItem{}, Events: []int64{}}
	if !workorders.UUID(p.TenantID) || !workorders.UUID(p.ID) || !workorders.UUID(project) || expectedPRs < 1 || expectedPRs > 100 || p.TenantID != m.config.TenantID {
		return out, fail(400, "backfill requires an explicit tenant, actor, project and expected linked PR count (1 to 100)")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ctx = db.AllProjects(tenant.WithPrincipal(ctx, p), "explicit OPS delivery Flow backfill of linked changes only")
	seen := map[string]*FlowBackfillItem{}
	steps := map[string]bool{}
	build := func(ctx context.Context, tx pgx.Tx, write func([]flowBatch) error) error {
		permission := "delivery.read"
		if apply {
			permission = "delivery.manage"
		}
		if err := requireMetrics(ctx, tx, p, permission, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT d.pull_request FROM delivery_items d JOIN nodes n ON n.id=d.ticket_node_id AND n.project_id=d.project_id WHERE d.project_id=$1 AND d.repository=$2 AND d.pull_request IS NOT NULL AND n.deleted_at IS NULL ORDER BY d.pull_request LIMIT 101`, project, m.config.Repository)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pr int64
			if err := rows.Scan(&pr); err != nil {
				rows.Close()
				return err
			}
			out.LinkedPRs = append(out.LinkedPRs, pr)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.LinkedPRs) != expectedPRs {
			return fail(409, fmt.Sprintf("expected %d linked PRs, found %d; inspect drift before backfill", expectedPRs, len(out.LinkedPRs)))
		}
		accept := func(b *flowBatch) error {
			if b == nil || b.Item.Project != project || b.Item.Kind != "change" {
				return nil
			}
			id := flowItemID(p.TenantID, project, "change", b.Item.Ref)
			item := seen[id]
			if item == nil {
				item = &FlowBackfillItem{ID: id, Ref: b.Item.Ref}
				seen[id] = item
			}
			item.PRs = sortedPRs(append(item.PRs, b.Item.PRs...))
			for _, s := range b.Steps {
				key := flowStepID(p.TenantID, id, s.Source, s.Key)
				if !steps[key] {
					steps[key] = true
					item.Steps++
				}
			}
			if len(steps) > flowBackfillLimit {
				return fail(409, "backfill exceeds 4000 candidate steps; narrow the project plan")
			}
			if write != nil {
				return write([]flowBatch{*b})
			}
			return nil
		}
		// One bounded read of already stored observations. No GitHub permissions,
		// network reads, metric cursor changes or production fact writes.
		rows, err = tx.Query(ctx, `SELECT run_id,attempt,workflow_path,event,head_sha,pull_request,created_at,started_at,completed_at,conclusion FROM delivery_metric_runs WHERE repository=$1 AND pull_request=ANY($2::bigint[]) AND event IN ('pull_request','merge_group') ORDER BY run_id,attempt LIMIT $3`, m.config.Repository, out.LinkedPRs, flowBackfillLimit+1)
		if err != nil {
			return err
		}
		byPR := map[int64][]metricRun{}
		n := 0
		for rows.Next() {
			var r metricRun
			if err := rows.Scan(&r.ID, &r.Attempt, &r.Workflow, &r.Event, &r.Head, &r.PR, &r.Created, &r.Started, &r.Completed, &r.Conclusion); err != nil {
				rows.Close()
				return err
			}
			byPR[*r.PR] = append(byPR[*r.PR], r)
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if n > flowBackfillLimit {
			return fail(409, "backfill exceeds 4000 stored runs; narrow the plan")
		}
		for _, pr := range out.LinkedPRs {
			b, err := m.flowRunBatchTx(ctx, tx, pr, byPR[pr])
			if err != nil {
				return err
			}
			if err := accept(b); err != nil {
				return err
			}
		}
		types := append(append([]string{}, flowHarnessEventTypes...), "delivery.state_changed")
		rows, err = tx.Query(ctx, `WITH linked AS MATERIALIZED
		 (SELECT ticket_node_id::text AS ticket,pull_request::text AS pr FROM delivery_items
		 WHERE project_id=$1 AND repository=$3 AND pull_request=ANY($5::bigint[]))
		 SELECT e.id,e.type,e.node_id::text,e.at,e.before,e.after,e.metadata
		 FROM unnest($2::text[]) AS kinds(kind) CROSS JOIN LATERAL
		 (SELECT id,type,node_id,at,before,after,metadata FROM events WHERE type=kinds.kind
		 AND (node_id::text IN (SELECT ticket FROM linked) OR node_id=$1::uuid AND
		 (after->>'ticket_node_id' IN (SELECT ticket FROM linked) OR type='delivery.ship.decision'
		 AND after#>>'{decision,facts,pull_request}' IN (SELECT pr FROM linked))) ORDER BY id LIMIT $4) e
		 ORDER BY e.id LIMIT $4`, project, types, m.config.Repository, flowBackfillLimit+1, out.LinkedPRs)
		if err != nil {
			return err
		}
		var events []flowSourceEvent
		for rows.Next() {
			var e flowSourceEvent
			if err := rows.Scan(&e.ID, &e.Type, &e.NodeID, &e.At, &e.Before, &e.After, &e.Metadata); err != nil {
				rows.Close()
				return err
			}
			events = append(events, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(events) > flowBackfillLimit {
			return fail(409, "backfill exceeds 4000 source events; narrow the plan")
		}
		for _, e := range events {
			b, err := m.flowSourceBatchTx(ctx, tx, p.TenantID, e)
			if err != nil {
				return err
			}
			if b == nil || b.Item.Project != project {
				continue
			}
			out.Events = append(out.Events, e.ID)
			if err := accept(b); err != nil {
				return err
			}
		}
		return nil
	}
	var err error
	if apply {
		var changed int
		changed, err = m.flowWrite(ctx, p.TenantID, &p, build)
		out.Changed = &changed
	} else {
		err = db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error { return build(ctx, tx, nil) })
	}
	for _, item := range seen {
		out.Items = append(out.Items, *item)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	return out, err
}
