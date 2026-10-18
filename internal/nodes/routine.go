// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// RoutineMutation leaves audit inserts to the broker, after its receipt. The
// ordinary schema, leaf status, benefit, portal and planning guards still run.
type RoutineMutation struct {
	ID       string
	Revision int64
	Events   []Event
}
type routineEvents struct{ items []Event }

func (w *routineEvents) WriteEvent(_ context.Context, _ pgx.Tx, e Event) error {
	w.items = append(w.items, e)
	return nil
}
func routineError(err error) error {
	var e *httpError
	if errors.As(err, &e) {
		return workorders.Fail(e.status, e.msg)
	}
	return err
}

// CreateRoutineWorkTx requires the caller to hold tenant/tree/pairing fences.
// Its narrow input cannot change kinds, assignments, policy or human checks.
func CreateRoutineWorkTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, parent, title, body string) (RoutineMutation, error) {
	var siblings int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM nodes WHERE parent_id=$1 AND deleted_at IS NULL LIMIT 1025) bounded`, parent).Scan(&siblings); err != nil {
		return RoutineMutation{}, err
	}
	if siblings > 1024 {
		return RoutineMutation{}, workorders.Fail(409, "sibling_limit_exceeded")
	}
	var kind string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE slug='work'`).Scan(&kind); err != nil {
		return RoutineMutation{}, err
	}
	pending := &routineEvents{}
	m := &Module{events: pending}
	n, err := m.createNodeTx(ctx, tx, p, nodeCreate{KindID: kind, ParentID: &parent, Title: title, Body: &body, Fields: json.RawMessage(`{}`)})
	return RoutineMutation{ID: n.ID, Revision: n.UpdatedAt.UnixMicro(), Events: pending.items}, routineError(err)
}

// UpdateRoutineWorkTx shares the normal patch implementation and authorization
// in this transaction. Queued terminal transitions belong to the pipeline and
// cannot append queue audit before the action receipt's remaining locks.
func UpdateRoutineWorkTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, project string, revision int64, title, body, state *string) (RoutineMutation, error) {
	if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: project}); err != nil {
		return RoutineMutation{}, err
	}
	var kind string
	if err := tx.QueryRow(ctx, `SELECT k.slug FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL`, id).Scan(&kind); err != nil {
		return RoutineMutation{}, err
	}
	if kind != "work" {
		return RoutineMutation{}, workorders.Fail(400, "work_target_required")
	}
	if state != nil {
		var parent bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds k ON k.id=c.kind_id WHERE c.parent_id=$1 AND c.deleted_at IS NULL AND k.slug='work')`, id).Scan(&parent); err != nil {
			return RoutineMutation{}, err
		}
		if parent {
			return RoutineMutation{}, workorders.Fail(409, "parent status follows its children")
		}
	}
	if state != nil && workqueue.Terminal(*state) {
		var queued bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE queue_node_id=$1 AND status='queued')`, id).Scan(&queued); err != nil {
			return RoutineMutation{}, err
		}
		if queued {
			return RoutineMutation{}, workorders.Fail(409, "queued_terminal_requires_pipeline")
		}
	}
	raw := map[string]json.RawMessage{}
	for k, v := range map[string]*string{"title": title, "body": body, "state": state} {
		if v != nil {
			raw[k], _ = json.Marshal(*v)
		}
	}
	expected := time.UnixMicro(revision)
	pending := &routineEvents{}
	n, err := (&Module{events: pending}).updateNodeTx(ctx, tx, p, id, raw, &expected)
	return RoutineMutation{ID: n.ID, Revision: n.UpdatedAt.UnixMicro(), Events: pending.items}, routineError(dbErr("update node", err))
}
