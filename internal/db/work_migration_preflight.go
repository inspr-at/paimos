// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This predicate is shared by the operational preflight and 1215's locked
// mutation guard. Only live direct work children make a blocking parent.
const busyWorkParentsSQL = `SELECT n.id::text,n.key,
 CASE WHEN EXISTS(SELECT 1 FROM harness_sessions s WHERE s.ticket_node_id=n.id AND s.stopped_at IS NULL)
 THEN 'bound session' ELSE 'claim or running work order' END
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND ($2::uuid IS NULL OR n.id>$2::uuid) AND n.deleted_at IS NULL AND k.slug IN ('epic','ticket','task')
 AND EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id
 WHERE c.tenant_id=n.tenant_id AND c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug IN ('epic','ticket','task','work'))
 AND (EXISTS(SELECT 1 FROM harness_sessions s WHERE s.ticket_node_id=n.id AND s.stopped_at IS NULL)
 OR EXISTS(SELECT 1 FROM agent_runs r WHERE r.queue_node_id=n.id AND r.status IN ('queued','starting','running','waiting'))
 OR EXISTS(SELECT 1 FROM work_orders w JOIN nodes wn ON wn.tenant_id=w.tenant_id AND wn.id=w.node_id
 WHERE w.tenant_id=n.tenant_id AND wn.parent_id=n.id AND w.status='running'))
 ORDER BY n.id LIMIT $3`

type BusyWorkParent struct {
	TenantID string
	ID       string
	Key      string
	Reason   string
}

// BusyWorkParentsError is also returned by boot, allowing the operator command
// and server entry point to exit distinctly without retrying migration.
type BusyWorkParentsError struct {
	Parents   []BusyWorkParent
	Truncated bool
}

func (e *BusyWorkParentsError) Error() string {
	keys := make([]string, len(e.Parents))
	for i, p := range e.Parents {
		keys[i] = p.TenantID + ": " + p.Key + " (" + p.Reason + ")"
	}
	more := ""
	if e.Truncated {
		more = "; diagnostic summary truncated (more than 100); migrate --check reports every blocker"
	}
	return "busy work parents require graceful handover: " + strings.Join(keys, ", ") + more +
		"; keep the old container running, drain claims and complete graceful handovers, then rerun migrate --check before stopping it"
}

func busyWorkParents(ctx context.Context, tx pgx.Tx, tenantID, after string, limit int) ([]BusyWorkParent, error) {
	var cursor any
	if after != "" {
		cursor = after
	}
	rows, err := tx.Query(ctx, busyWorkParentsSQL, tenantID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parents := make([]BusyWorkParent, 0, limit)
	for rows.Next() {
		p := BusyWorkParent{TenantID: tenantID}
		if err := rows.Scan(&p.ID, &p.Key, &p.Reason); err != nil {
			return nil, err
		}
		parents = append(parents, p)
	}
	return parents, rows.Err()
}

// CheckWorkMigration reads one repeatable, read-only snapshot. It never calls
// Open, the migration runner, or InTenant's optional write machinery. It emits
// all blocking parents in bounded keyset pages and retains only 100 diagnostics.
// The locked guard still runs during migration: this is no execution permit.
func CheckWorkMigration(ctx context.Context, pool *pgxpool.Pool, emit func(BusyWorkParent) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='10s'; SET LOCAL lock_timeout='1s'`); err != nil {
		return err
	}
	var ledger, nodes bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL,to_regclass('nodes') IS NOT NULL`).Scan(&ledger, &nodes); err != nil {
		return err
	}
	if ledger {
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='1215_one_work_kind.sql')`).Scan(&applied); err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	if !nodes {
		return nil
	}
	ctx = AllProjects(ctx, "AEON-648 read-only work migration preflight")
	blocked := &BusyWorkParentsError{}
	err = workTenantPages(ctx, tx, func(tid string) error {
		if err := enterTenant(ctx, tx, tid); err != nil {
			return err
		}
		after := ""
		for {
			parents, err := busyWorkParents(ctx, tx, tid, after, 100)
			if err != nil {
				return err
			}
			if len(parents) == 0 {
				break
			}
			for _, p := range parents {
				if emit != nil {
					if err := emit(p); err != nil {
						return err
					}
				}
				if len(blocked.Parents) < 100 {
					blocked.Parents = append(blocked.Parents, p)
				} else {
					blocked.Truncated = true
				}
			}
			after = parents[len(parents)-1].ID
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("work migration preflight incomplete: %w", err)
	}
	if len(blocked.Parents) != 0 {
		return blocked
	}
	return nil
}
