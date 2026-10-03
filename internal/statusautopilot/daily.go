// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const batchSize = 50

// RunTenant commits each bounded batch independently. The cursor advances in
// the same transaction as its events; retries and competing replicas resume
// after the last committed batch without holding up ordinary node writes.
func (m *Module) RunTenant(ctx context.Context, tenantID string, now time.Time) error {
	ctx = db.AllProjects(ctx, "daily status autopilot")
	serverMode, err := config.StatusAutopilotMode()
	if err != nil || serverMode == "off" {
		return err
	}
	// Creating the System principal takes the principal-link lock (532).
	// Principal writers can hold that lock before inserting a node, which takes
	// the tree lock. Commit first-use provisioning before taking any tree lock
	// so startup cannot invert that order. Later batches only read this actor.
	var actor tenant.Principal
	if err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		actor, err = systemactor.Ensure(ctx, tx, tenantID)
		return err
	}); err != nil {
		return err
	}
	for {
		count := 0
		err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT r.release_node_id::text FROM journey_releases r
 WHERE r.state IN ('released','superseded') AND NOT EXISTS
 (SELECT 1 FROM status_autopilot_releases s WHERE s.tenant_id=r.tenant_id AND s.release_id=r.release_node_id)
 ORDER BY r.released_at,r.release_node_id LIMIT $1`, batchSize)
			if err != nil {
				return err
			}
			ids, err := collectIDs(rows)
			if err != nil {
				return err
			}
			count = len(ids)
			for _, id := range ids {
				if err = queueReleaseTx(ctx, tx, tenantID, id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if count < batchSize {
			break
		}
	}
	for {
		count := 0
		err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
			if err := lock(ctx, tx); err != nil {
				return err
			}
			var err error
			count, err = deliveryBatchTx(ctx, tx, tenantID, "", now)
			return err
		})
		if err != nil {
			return err
		}
		if count < batchSize {
			break
		}
	}
	day := now.UTC().Truncate(24 * time.Hour)
	for {
		done := false
		err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
			if err := lock(ctx, tx); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO status_autopilot_days(tenant_id,day) VALUES($1,$2) ON CONFLICT DO NOTHING`, tenantID, day.Add(-24*time.Hour))
			if err != nil {
				return err
			}
			var completed time.Time
			var running *time.Time
			var after *string
			var priorMode string
			if err = tx.QueryRow(ctx, `SELECT day,running_day,after_node_id::text,mode FROM status_autopilot_days FOR UPDATE`).Scan(&completed, &running, &after, &priorMode); err != nil {
				return err
			}
			s, err := Load(ctx, tx)
			if err != nil {
				return err
			}
			mode := s.ModeAt(now)
			if mode != priorMode {
				completed, running, after = day.Add(-24*time.Hour), nil, nil
				if _, err = tx.Exec(ctx, `UPDATE status_autopilot_days SET mode=$1,day=$2,running_day=NULL,after_node_id=NULL`, mode, completed); err != nil {
					return err
				}
			}
			if !completed.Before(day) {
				done = true
				return nil
			}
			if running == nil {
				running = &day
				after = nil
			}
			rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE k.slug='ticket' AND n.deleted_at IS NULL AND `+candidateStateSQL+` IN ('new','backlog','blocked','in_progress','inprogress','progress','active','done','delivered')
 AND ($1::uuid IS NULL OR n.id>$1) ORDER BY n.id LIMIT $2`, after, batchSize)
			if err != nil {
				return err
			}
			ids, err := collectIDs(rows)
			if err != nil {
				return err
			}
			candidates, err := loadCandidates(ctx, tx, ids, *running)
			if err != nil {
				return err
			}
			overrides := map[string]Override{}
			for _, c := range candidates {
				effective := s
				if c.Node.ProjectID != nil {
					id := *c.Node.ProjectID
					o, ok := overrides[id]
					if !ok {
						o, err = Project(ctx, tx, id, s.Enabled)
						if err != nil {
							return err
						}
						overrides[id] = o
					}
					// Explicit project Off suppresses every rule. Workspace Off
					// retains its approved exception for New/Backlog suggestions.
					if o.Mode == "off" {
						continue
					}
					effective.Enabled = o.Effective
				}
				if d := evaluate(c, effective, *running); d != nil {
					if err = enact(ctx, tx, actor, c.Node, *d, mode); err != nil {
						return err
					}
				}
			}
			if len(ids) < batchSize {
				_, err = tx.Exec(ctx, `UPDATE status_autopilot_days SET day=$1,running_day=NULL,after_node_id=NULL`, *running)
				done = !running.Before(day)
			} else {
				_, err = tx.Exec(ctx, `UPDATE status_autopilot_days SET running_day=$1,after_node_id=$2`, *running, ids[len(ids)-1])
			}
			return err
		})
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func collectIDs(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
