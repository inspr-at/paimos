// SPDX-License-Identifier: AGPL-3.0-only

// Package eta reads ready and live estimates. Roll-up and staleness are computed
// in the database at read time; this package only loads that row.
package eta

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// View is one ticket's estimate. Empty fields stay unset so clients omit them.
type View struct {
	LeafCount         int        `json:"leaf_count"`
	EstimatedLeaves   int        `json:"estimated_leaves"`
	ProgressBasis     string     `json:"progress_basis"`
	OpenLeaves        int        `json:"open_leaves"`
	ReadyLeaves       int        `json:"ready_leaves"`
	LiveLeaves        int        `json:"live_leaves"`
	ReadyPartial      bool       `json:"ready_partial"`
	LivePartial       bool       `json:"live_partial"`
	HasWorkingSession bool       `json:"has_working_session,omitempty"`
	ReadyAt           *time.Time `json:"eta_ready_at,omitempty"`
	LiveAt            *time.Time `json:"eta_live_at,omitempty"`
	Progress          *int       `json:"progress_pct,omitempty"`
	ReadyReportedAt   *time.Time `json:"ready_reported_at,omitempty"`
	LiveReportedAt    *time.Time `json:"live_reported_at,omitempty"`
	ReadyBy           string     `json:"ready_by,omitempty"`
	LiveBy            string     `json:"live_by,omitempty"`
	ReadyStale        bool       `json:"ready_stale,omitempty"`
	LiveStale         bool       `json:"live_stale,omitempty"`
	EtaStale          bool       `json:"eta_stale,omitempty"`
	// Finished is required whenever an estimate is present, never omitted: no session
	// is open on the ticket and the last worker to leave reported 100% and recorded a
	// clean exit (aeon_session_finished, AEON-437). Nothing else reads as Done.
	Finished   bool       `json:"finished"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	FinishedBy string     `json:"finished_by,omitempty"`
}

// Blank is true when nothing was reported. A known zero percent is not blank.
func (v View) Blank() bool {
	return v.ReadyAt == nil && v.LiveAt == nil && v.Progress == nil && !v.HasWorkingSession && !v.Finished
}

const workingSessionSQL = `EXISTS(SELECT 1 FROM harness_sessions s
	JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.ticket_node_id AND n.project_id=s.project_id
	WHERE s.ticket_node_id=u.id AND n.deleted_at IS NULL AND s.phase='working'
	AND s.stopped_at IS NULL AND s.archived_at IS NULL AND s.role IN ('worker','coordinator'))`

// CompletionJoin is the worker that left the ticket last, when nobody is on it any more,
// as a LEFT JOIN LATERAL named alias over the ticket id expression idExpr. A finished
// worker's estimate is not kept, so completion is read through aeon_node_completion,
// the same projection used by the recursive epic roll-up.
func CompletionJoin(idExpr, alias string) string {
	return `LEFT JOIN LATERAL aeon_node_completion(` + idExpr + `) ` + alias + ` ON true`
}

// ProgressSQL is the one projection of a ticket's progress: the open sessions'
// percent or recursive epic roll-up from aeon_node_eta_progress (etaAlias), or
// 100 when the last worker finished (CompletionJoin as completionAlias). The
// ticket list displays it and sorts by it, so the order is the number on screen.
func ProgressSQL(etaAlias, completionAlias string) string {
	return `CASE WHEN coalesce(` + completionAlias + `.finished, false) THEN 100 ELSE ` + etaAlias + `.progress_pct END`
}

const estimateColumns = `e.eta_ready_at,e.eta_live_at,e.progress_pct,
 e.ready_reported_at,e.live_reported_at,e.ready_by,e.live_by,e.ready_stale,e.live_stale,e.has_working_session,
 e.finished,e.finished_at,e.finished_by,e.leaf_count,e.estimated_leaves,e.progress_basis,e.open_leaves,e.ready_leaves,e.live_leaves`

// Load reads estimates for the page. Ids with no report are absent.
func Load(ctx context.Context, tx pgx.Tx, ids []string) (map[string]View, error) {
	out := map[string]View{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT e.id::text, `+estimateColumns+` FROM aeon_work_aggregates($1::uuid[]) e`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		view, err := scanView(rows, &id)
		if err != nil {
			return nil, err
		}
		if !view.Blank() {
			out[id] = view
		}
	}
	return out, rows.Err()
}

// One reads a single node's estimate. A blank view means nothing is known.
func One(ctx context.Context, tx pgx.Tx, id string) (View, error) {
	rows, err := tx.Query(ctx, `SELECT e.id::text, `+estimateColumns+` FROM aeon_work_aggregates(ARRAY[$1::uuid]) e`, id)
	if err != nil {
		return View{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return View{}, rows.Err()
	}
	var got string
	return scanView(rows, &got)
}

func scanView(rows pgx.Rows, id *string) (View, error) {
	var view View
	var progress *int
	var readyBy, liveBy, finishedBy *string
	err := rows.Scan(id, &view.ReadyAt, &view.LiveAt, &progress, &view.ReadyReportedAt, &view.LiveReportedAt, &readyBy, &liveBy, &view.ReadyStale, &view.LiveStale, &view.HasWorkingSession,
		&view.Finished, &view.FinishedAt, &finishedBy, &view.LeafCount, &view.EstimatedLeaves, &view.ProgressBasis, &view.OpenLeaves, &view.ReadyLeaves, &view.LiveLeaves)
	if err != nil {
		return View{}, err
	}
	view.ReadyPartial = view.ReadyLeaves < view.OpenLeaves
	view.LivePartial = view.LiveLeaves < view.OpenLeaves
	view.Progress = progress
	if view.Finished && finishedBy != nil {
		view.FinishedBy = *finishedBy
	}
	if readyBy != nil {
		view.ReadyBy = *readyBy
	}
	if liveBy != nil {
		view.LiveBy = *liveBy
	}
	view.EtaStale = view.ReadyStale || view.LiveStale
	return view, nil
}
