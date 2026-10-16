// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// RevalidateTenant records only passed flips. It never compares or appends an
// ETA value; transient ETA transitions belong to the source-write capture.
// The cursor bounds every transaction to 100 goals and advances past large
// expansions. A caller can continue the tenant on the next scheduled tick.
func (m *module) RevalidateTenant(ctx context.Context, tenantID, after string, now time.Time) (string, error) {
	next := ""
	service := db.AllProjects(ctx, "Goals passed revalidation: system-derived events only")
	ctx, cancel := context.WithTimeout(service, 8*time.Second)
	defer cancel()
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, tenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+nodeColumns+` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug IN ('goal','release') AND n.deleted_at IS NULL AND ($1='' OR n.id::text>$1) AND (n.fields ? 'eta_at' OR EXISTS(SELECT 1 FROM nodes o JOIN node_kinds ok ON ok.tenant_id=o.tenant_id AND ok.id=o.kind_id WHERE o.parent_id=n.id AND ok.slug='outcome' AND o.deleted_at IS NULL)) ORDER BY n.id LIMIT 101`, after)
		if err != nil {
			return err
		}
		nodes := []storedNode{}
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				rows.Close()
				return err
			}
			nodes = append(nodes, n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(nodes) > 100 {
			nodes = nodes[:100]
			next = nodes[99].ID
		}
		if len(nodes) == 0 {
			return nil
		}
		p := tenant.Principal{TenantID: tenantID, Kind: tenant.Agent}
		created := false
		err = tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE name='Goals revalidation' AND kind='agent' AND roles=ARRAY['operator']::text[] ORDER BY id LIMIT 1`).Scan(&p.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent','Goals revalidation',ARRAY['operator']::text[]) RETURNING id::text`, tenantID).Scan(&p.ID)
			created = true
		}
		if err != nil {
			return err
		}
		pending := []events.Change{}
		if created {
			pending = append(pending, events.Change{Type: "principal.created", After: map[string]any{"id": p.ID, "kind": "agent", "name": "Goals revalidation"}})
		}
		for _, n := range nodes {
			// Read with invoker rights in this service tenant; use the same bounded
			// state/ETA/coverage predicates as the interactive read.
			nested, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			g, err := expandGoal(ctx, nested, p, GoalView{ID: n.ID, node: n, Steps: []Step{}, Projects: []ProjectBadge{}}, now)
			if err != nil {
				if rollback := nested.Rollback(ctx); rollback != nil {
					return rollback
				}
				if tooLarge(err) {
					slog.Warn("goal revalidation expansion too large", "goal_id", n.ID)
					continue
				}
				return err
			}
			if err := nested.Commit(ctx); err != nil {
				return err
			}
			for _, item := range g.Items {
				prior := false
				err := tx.QueryRow(ctx, `SELECT after='true'::jsonb FROM events WHERE node_id=$1 AND type='goal.passed_changed' ORDER BY id DESC LIMIT 1`, item.ID).Scan(&prior)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if prior == item.Passed {
					continue
				}
				id := item.ID
				pending = append(pending, events.Change{NodeID: &id, Type: "goal.passed_changed", Before: prior, After: item.Passed, At: &now})
			}
			prior := false
			if err := tx.QueryRow(ctx, `SELECT count(*)%2=1 FROM events WHERE node_id=$1 AND type='goal.passed_changed'`, n.ID).Scan(&prior); err != nil {
				return err
			}
			if g.Passed != nil && *g.Passed != prior {
				w, err := watermark(ctx, tx)
				if err != nil {
					return err
				}
				id := n.ID
				pending = append(pending, events.Change{NodeID: &id, Type: "goal.passed_changed", After: ChangePing{GoalID: id, Watermark: w}, At: &now})
			}
		}
		// No principal, node, session or access locks are acquired after this point.
		for _, change := range pending {
			if _, err := events.Append(ctx, tx, p, change); err != nil {
				return err
			}
		}
		return nil
	})
	return next, err
}
func (m *module) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	tenantCursor := ""
	goalCursors := map[string]string{}
	for {
		// The global tenant table is also paged; newly created tenants are picked
		// up after the cursor wraps, without requiring a process restart.
		ids := []string{}
		rows, err := m.pool.Query(ctx, `SELECT id::text FROM tenants WHERE id::text>$1 ORDER BY id LIMIT 100`, tenantCursor)
		if err == nil {
			for rows.Next() {
				var id string
				if e := rows.Scan(&id); e != nil {
					err = e
					break
				}
				ids = append(ids, id)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("goal revalidation tenants", "error", err)
		}
		for _, id := range ids {
			next, err := m.RevalidateTenant(ctx, id, goalCursors[id], m.now())
			if err != nil && ctx.Err() == nil {
				slog.Error("goal revalidation", "tenant_id", id, "error", err)
			} else {
				if next == "" {
					delete(goalCursors, id)
				} else {
					goalCursors[id] = next
				}
			}
			tenantCursor = id
		}
		if len(ids) < 100 {
			tenantCursor = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
