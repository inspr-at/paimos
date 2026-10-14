// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func (m *Module) mountStatusTiming(mux *http.ServeMux) {
	mount := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			fn(w, r.WithContext(ctx))
		})
	}
	mount("GET /api/projects/{projectId}/delivery/tickets", m.getTicketTiming)
	mount("GET /api/nodes/{nodeId}/lifeline", m.getTicketLifeline)
	mount("POST /api/projects/{projectId}/delivery/tickets/backfill", m.backfillTicketTiming)
}

func timingError(w http.ResponseWriter, err error) {
	if db.IsStatementTimeout(err) || err == context.DeadlineExceeded || err == context.Canceled {
		respondError(w, fail(503, "status timing read deadline exceeded; retry or narrow the scope"))
		return
	}
	respondError(w, err)
}

func (m *Module) getTicketTiming(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := timingFilter{Days: 90, Type: q.Get("type"), Priority: q.Get("priority"), WorkedBy: q.Get("worked_by")}
	if f.Type == "all" {
		f.Type = ""
	}
	if f.Priority == "all" {
		f.Priority = ""
	}
	if f.WorkedBy == "all" {
		f.WorkedBy = ""
	}
	if q.Has("window") {
		var err error
		f.Days, err = strconv.Atoi(q.Get("window"))
		if err != nil {
			respondError(w, fail(400, "invalid status timing window"))
			return
		}
	}
	validWindow := f.Days == 7 || f.Days == 30 || f.Days == 90 || f.Days == 180 || f.Days == 365
	if !validWindow || len(f.Type) > 64 || f.Priority != "" && f.Priority != "high" && f.Priority != "medium" && f.Priority != "low" || f.WorkedBy != "" && f.WorkedBy != "agent" && f.WorkedBy != "person" {
		respondError(w, fail(400, "invalid status timing filters"))
		return
	}
	now := m.now().UTC()
	var out ticketTiming
	err := db.InTenantReadSnapshot(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(r.Context(), tx, 5*time.Second); err != nil {
			return err
		}
		if err := timingProjectTx(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireTiming(r.Context(), tx, p, project, "nodes.read"); err != nil {
			return err
		}
		nodes, err := loadTimingNodes(r.Context(), tx, project, "")
		if err != nil {
			return err
		}
		history, err := loadTimingEvents(r.Context(), tx, nodes, now, timingEventLimit)
		if err != nil {
			return err
		}
		out = aggregateTiming(project, nodes, history, f, now)
		return r.Context().Err()
	})
	if err != nil {
		timingError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) getTicketLifeline(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("nodeId")
	if !workorders.UUID(id) {
		respondError(w, fail(400, "invalid lifeline node"))
		return
	}
	now := m.now().UTC()
	var out ticketLifeline
	err := db.InTenantReadSnapshot(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(r.Context(), tx, 5*time.Second); err != nil {
			return err
		}
		var project *string
		if err := tx.QueryRow(r.Context(), `SELECT project_id::text FROM nodes WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, p.TenantID, id).Scan(&project); err != nil {
			return err
		}
		scopeID := ""
		if project != nil {
			scopeID = *project
		}
		if err := requireTiming(r.Context(), tx, p, scopeID, "nodes.read"); err != nil {
			return err
		}
		nodes, err := loadTimingNodes(r.Context(), tx, "", id)
		if err != nil {
			return err
		}
		if len(nodes) != 1 {
			return pgx.ErrNoRows
		}
		history, err := loadTimingEvents(r.Context(), tx, nodes, now, lifelineEventLimit)
		if err != nil {
			return err
		}
		out = deriveLifeline(nodes[0], history[id], now)
		return r.Context().Err()
	})
	if err != nil {
		timingError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) backfillTicketTiming(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	var in struct {
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
	}
	in.Limit = 100
	if err := decodeBounded(w, r, 2048, &in); err != nil {
		respondError(w, err)
		return
	}
	if in.Limit < 1 || in.Limit > 200 || in.Cursor != "" && !workorders.UUID(in.Cursor) {
		respondError(w, fail(400, "invalid baseline backfill page"))
		return
	}
	var out timingBackfillResult
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(r.Context(), tx, 5*time.Second); err != nil {
			return err
		}
		var err error
		out, err = backfillTimingTx(r.Context(), tx, p, project, in.Cursor, in.Limit)
		return err
	})
	if err != nil {
		timingError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
