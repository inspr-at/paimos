// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

// Both windows of the widest choice fit the budgets; a project beyond them
// gets 503 and no partial figures.
const (
	buildingItemLimit = 5000
	buildingStepLimit = 100000
)

func (m *Module) mountBuilding(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{projectId}/delivery/building", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		m.getBuilding(w, r.WithContext(ctx))
	})
}

func (m *Module) getBuilding(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := timingFilter{Days: 7, Type: q.Get("type"), Priority: q.Get("priority"), WorkedBy: q.Get("worked_by")}
	for _, v := range []*string{&f.Type, &f.Priority, &f.WorkedBy} {
		if *v == "all" {
			*v = ""
		}
	}
	if q.Has("window") {
		var err error
		if f.Days, err = strconv.Atoi(q.Get("window")); err != nil {
			respondError(w, fail(400, "invalid building window"))
			return
		}
	}
	validWindow := f.Days == 7 || f.Days == 30 || f.Days == 90 || f.Days == 180 || f.Days == 365
	if !validWindow || len(f.Type) > 64 || f.Priority != "" && f.Priority != "high" && f.Priority != "medium" && f.Priority != "low" || f.WorkedBy != "" && f.WorkedBy != "agent" && f.WorkedBy != "person" {
		respondError(w, fail(400, "invalid building filters"))
		return
	}
	now := m.now().UTC()
	var out deliveryBuilding
	err := db.InTenantReadSnapshot(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(r.Context(), tx, 5*time.Second); err != nil {
			return err
		}
		if err := timingProjectTx(r.Context(), tx, project); err != nil {
			return err
		}
		if err := requireMetrics(r.Context(), tx, p, "delivery.read", project); err != nil {
			return err
		}
		since, err := buildingSinceTx(r.Context(), tx, project)
		if err != nil {
			return err
		}
		items, err := loadBuildingTx(r.Context(), tx, project, windowSpan(now, f.Days, true).first, now)
		if err != nil {
			return err
		}
		targets, err := buildingTargetsTx(r.Context(), tx, &project)
		if err != nil {
			return err
		}
		out = aggregateBuilding(project, items, since, targets, f, now)
		return r.Context().Err()
	})
	if err != nil {
		if db.IsStatementTimeout(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			err = fail(503, "building read deadline exceeded; retry or narrow the window")
		}
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// buildingSinceTx is the first recorded builder step of the project: before
// it the Flow saw no rounds, so those days are "no data yet", never zero.
func buildingSinceTx(ctx context.Context, tx pgx.Tx, project string) (*time.Time, error) {
	var since *time.Time
	err := tx.QueryRow(ctx, `SELECT min(started_at) FROM delivery_flow_steps
 WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND project_id=$1 AND step_key IN ('build','merge_round')`, project).Scan(&since)
	if since != nil {
		v := since.UTC()
		since = &v
	}
	return since, err
}

// loadBuildingTx reads the changes merged in [from, to] with their tickets'
// filter facts, then only the steps that make rounds, verdicts and the queue.
func loadBuildingTx(ctx context.Context, tx pgx.Tx, project string, from, to time.Time) ([]buildingItem, error) {
	rows, err := tx.Query(ctx, `SELECT i.id::text,i.ticket_node_id::text,i.ref,i.title,i.ended_at,k.slug,
 left(coalesce(n.fields->>'priority',n.fields->'classic'->>'priority',''),64),
 EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(n.fields->'tags')='array' THEN n.fields->'tags' ELSE '[]'::jsonb END) tag
  WHERE lower(CASE WHEN jsonb_typeof(tag)='string' THEN tag#>>'{}' ELSE tag->>'name' END)='bug')
 FROM delivery_flow_items i
 JOIN nodes n ON n.tenant_id=i.tenant_id AND n.id=i.ticket_node_id AND n.deleted_at IS NULL
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE i.tenant_id=current_setting('aeon.tenant_id')::uuid AND i.project_id=$1 AND i.kind='change'
 AND i.ended_at>=$2 AND i.ended_at<=$3
 ORDER BY i.ended_at,i.id LIMIT $4`, project, from, to, buildingItemLimit+1)
	if err != nil {
		return nil, err
	}
	items := []buildingItem{}
	for rows.Next() {
		var it buildingItem
		if err := rows.Scan(&it.ID, &it.Ticket, &it.Ref, &it.Title, &it.Merged, &it.Kind, &it.Priority, &it.Bug); err != nil {
			rows.Close()
			return nil, err
		}
		it.Merged = it.Merged.UTC()
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > buildingItemLimit {
		return nil, fail(503, "building item budget exceeded")
	}
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]string, len(items))
	at := make(map[string]int, len(items))
	for i, it := range items {
		ids[i], at[it.ID] = it.ID, i
	}
	rows, err = tx.Query(ctx, `SELECT item_id::text,step_key,kind,actor_type,coalesce(outcome,''),started_at,ended_at FROM delivery_flow_steps
 WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND item_id=ANY($1::uuid[]) AND NOT side
 AND step_key IN ('build','merge_round','review','ci','queue')
 ORDER BY item_id,started_at,id LIMIT $2`, ids, buildingStepLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var item string
		var s buildingStep
		if err := rows.Scan(&item, &s.Key, &s.Kind, &s.Actor, &s.Outcome, &s.Started, &s.Ended); err != nil {
			return nil, err
		}
		if count++; count > buildingStepLimit {
			return nil, fail(503, "building step budget exceeded")
		}
		s.Started = s.Started.UTC()
		if s.Ended != nil {
			v := s.Ended.UTC()
			s.Ended = &v
		}
		if i, ok := at[item]; ok {
			items[i].Steps = append(items[i].Steps, s)
		}
	}
	return items, rows.Err()
}
