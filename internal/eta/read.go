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
	ReadyAt         *time.Time `json:"eta_ready_at,omitempty"`
	LiveAt          *time.Time `json:"eta_live_at,omitempty"`
	Progress        *int       `json:"progress_pct,omitempty"`
	ReadyReportedAt *time.Time `json:"ready_reported_at,omitempty"`
	LiveReportedAt  *time.Time `json:"live_reported_at,omitempty"`
	ReadyBy         string     `json:"ready_by,omitempty"`
	LiveBy          string     `json:"live_by,omitempty"`
	ReadyStale      bool       `json:"ready_stale,omitempty"`
	LiveStale       bool       `json:"live_stale,omitempty"`
}

// Blank is true when nothing was reported. A known zero percent is not blank.
func (v View) Blank() bool {
	return v.ReadyAt == nil && v.LiveAt == nil && v.Progress == nil
}

// Load reads estimates for the page. Ids with no report are absent.
func Load(ctx context.Context, tx pgx.Tx, ids []string) (map[string]View, error) {
	out := map[string]View{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT u.id::text, e.eta_ready_at, e.eta_live_at, e.progress_pct,
		e.ready_reported_at, e.live_reported_at, e.ready_by, e.live_by, e.ready_stale, e.live_stale
		FROM unnest($1::uuid[]) AS u(id)
		CROSS JOIN LATERAL aeon_node_eta(u.id) e`, ids)
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
	rows, err := tx.Query(ctx, `SELECT $1::text, e.eta_ready_at, e.eta_live_at, e.progress_pct,
		e.ready_reported_at, e.live_reported_at, e.ready_by, e.live_by, e.ready_stale, e.live_stale
		FROM aeon_node_eta($1::uuid) e`, id)
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
	var readyBy, liveBy *string
	err := rows.Scan(id, &view.ReadyAt, &view.LiveAt, &progress, &view.ReadyReportedAt, &view.LiveReportedAt, &readyBy, &liveBy, &view.ReadyStale, &view.LiveStale)
	if err != nil {
		return View{}, err
	}
	view.Progress = progress
	if readyBy != nil {
		view.ReadyBy = *readyBy
	}
	if liveBy != nil {
		view.LiveBy = *liveBy
	}
	return view, nil
}
