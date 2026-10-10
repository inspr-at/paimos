// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Observe the number of rows transferred by the cause snapshot query, not
// allocations or elapsed time. Oversized rows must stay in Postgres.
type causeTransferTrace struct {
	cause int64
	rows  []int64
}

func (s *causeTransferTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if len(q.Args) >= 2 && q.Args[1] == s.cause && strings.Contains(q.SQL, "before,after") {
		return context.WithValue(ctx, s, true)
	}
	return ctx
}
func (s *causeTransferTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(s) == true {
		s.rows = append(s.rows, q.CommandTag.RowsAffected())
	}
}

func TestCausalPreviewRejectsOversizedBulkBeforeTransfer(t *testing.T) {
	d, p, _ := fixture(t)
	var node string
	var cause, parentID int64
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'WK-1','Work' FROM node_kinds WHERE slug='work' RETURNING id::text`, p.TenantID).Scan(&node); err != nil {
			return err
		}
		// 100 distinct children remain below the preview's 200-child limit.
		// Their field snapshots exceed 4 MiB; JSONB is intentionally compressible,
		// so a physical pg_column_size bound would incorrectly admit it.
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state,fields)
		 SELECT $1,k.id,'WK-'||(s+1),'Child',$2::uuid,'done',jsonb_build_object('notes',repeat('x',45000))
		 FROM node_kinds k,generate_series(1,100) s WHERE k.slug='work'`, p.TenantID, node); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,before,after)
		 SELECT $1,$2,$3::uuid,'node.bulk_changed',jsonb_build_object('items',jsonb_agg(jsonb_build_object('id',id,'key',key,'state','open','updated_at',updated_at-interval '1 microsecond','fields',fields))),
		 jsonb_build_object('items',jsonb_agg(jsonb_build_object('id',id,'key',key,'state',state,'updated_at',updated_at,'fields',fields)))
		 FROM nodes WHERE tenant_id=$1 AND parent_id=$3::uuid RETURNING id`, p.TenantID, p.ID, node).Scan(&cause); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after,metadata) VALUES($1,$2,$3::uuid,'status_autopilot.derived',jsonb_build_object('id',$3::uuid::text,'state','done'),jsonb_build_object('cause_event_id',$4::bigint)) RETURNING id`, p.TenantID, p.ID, node, cause).Scan(&parentID)
	}); err != nil {
		t.Fatal(err)
	}
	trace := &causeTransferTrace{cause: cause}
	cfg := d.App.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m := &module{undo: map[string]UndoFunc{"node.bulk_changed": func(context.Context, pgx.Tx, tenant.Principal, Event) (Change, error) {
		t.Fatal("oversized cause reached Undo")
		return Change{}, nil
	}}}
	err = db.InTenant(dbtest.Seed(t.Context()), pool, p.TenantID, func(tx pgx.Tx) error {
		_, _, _, err := m.causalChange(t.Context(), tx, p, Event{ID: parentID, Type: derivedStatusEvent, NodeID: &node})
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("oversized cause refusal: %v", err)
	}
	if len(trace.rows) != 1 || trace.rows[0] != 0 {
		t.Fatalf("oversized cause snapshots transferred to application: rows=%v", trace.rows)
	}
}
