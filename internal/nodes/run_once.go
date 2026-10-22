// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// runOnce composes normal node/order creation and the authoritative run writer.
// The single transaction also makes a lost response safe when creating a leaf.
func (m *Module) runOnce(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		agentruns.CreateInput
		Task              string    `json:"task"`
		ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "only a person can run once")
	}
	if !workorders.UUID(r.PathValue("nodeId")) || in.RequestID == nil || !workorders.UUID(*in.RequestID) || in.ExpectedUpdatedAt.IsZero() ||
		!utf8.ValidString(in.Task) || len(in.Task) > 640 || utf8.RuneCountInString(in.Task) > 160 {
		return nil, workorders.Fail(400, "context, request_id, context revision and a task of at most 160 characters required")
	}
	ctx := r.Context()
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return nil, err
	}
	if err := accountuse.LockShared(ctx, tx); err != nil {
		return nil, err
	}
	contextNode, err := loadNode(ctx, tx, r.PathValue("nodeId"), false)
	if err != nil {
		return nil, routineError(err)
	}
	var kind, project string
	if err := tx.QueryRow(ctx, `SELECT k.slug,CASE WHEN k.slug='project' THEN n.id::text ELSE coalesce(n.project_id::text,'') END FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1`, contextNode.ID).Scan(&kind, &project); err != nil {
		return nil, err
	}
	if kind != "project" && kind != "work" && kind != "ticket" && kind != "task" {
		return nil, workorders.Fail(400, "choose a project or work leaf")
	}
	for _, permission := range []string{"nodes.read", "work_orders.write", "run.create"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return nil, err
		}
	}
	if kind == "project" {
		if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: project}); err != nil {
			return nil, err
		}
	}
	if err := authz.RequireComputerStartForPersonTx(ctx, tx, p, in.Agent); err != nil {
		return nil, err
	}
	in.RequestDigest = agentruns.RunRequestDigest(contextNode.ID, in)
	previous, err := agentruns.FindRequestedRunTx(ctx, tx, p, in.CreateInput)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		return *previous, nil
	}
	if !contextNode.UpdatedAt.Equal(in.ExpectedUpdatedAt) {
		return nil, workorders.Fail(409, "selected context changed; reopen Run once before starting")
	}
	pendingNodes := &routineEvents{}
	writer := *m
	writer.events = pendingNodes
	ticket := contextNode
	if kind == "project" {
		if strings.TrimSpace(in.Task) == "" {
			return nil, workorders.Fail(400, "task text required for a new Run once ticket")
		}
		var workKind string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE slug='work'`).Scan(&workKind); err != nil {
			return nil, err
		}
		// createNodeTx retains the normal schema, project, readiness and audit
		// guards; this is a visible leaf, never a hidden throwaway context.
		body := in.Task
		ticket, err = writer.createNodeTx(ctx, tx, p, nodeCreate{KindID: workKind, ParentID: &contextNode.ID, Title: "Run once: " + in.Task, Body: &body, Fields: json.RawMessage(`{"hide_from_release_notes":true}`)})
		if err != nil {
			return nil, routineError(err)
		}
	} else if in.Task != "" {
		return nil, workorders.Fail(400, "task text is only for a new project leaf")
	}
	var held bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN nodes n ON n.id=r.work_order_id WHERE n.parent_id=$1 AND r.status IN ('queued','starting','running','waiting'))`, ticket.ID).Scan(&held); err != nil {
		return nil, err
	}
	if held {
		return nil, workorders.Fail(409, "this leaf already has an active run; follow it in Agents before starting another")
	}
	rows, err := tx.Query(ctx, `SELECT w.node_id::text FROM work_orders w JOIN nodes n ON n.id=w.node_id WHERE n.parent_id=$1 AND n.deleted_at IS NULL AND w.kind='build' AND w.status NOT IN ('done','cancelled') AND (w.assignee_principal_id IS NULL OR w.assignee_principal_id=$2) ORDER BY w.node_id LIMIT 2`, ticket.ID, in.Agent)
	if err != nil {
		return nil, err
	}
	var orders []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		orders = append(orders, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(orders) > 1 {
		return nil, workorders.Fail(409, "this leaf has several active work orders; resolve them before starting")
	}
	var order workorders.Order
	var pending []events.Change
	if len(orders) == 1 {
		order, err = workorders.Load(ctx, tx, orders[0], true)
		if err != nil {
			return nil, err
		}
		if order.Status == "blocked" {
			return nil, workorders.Fail(409, "this work order is blocked; resolve its blocker before starting")
		}
	} else {
		body := ticket.Body
		if strings.TrimSpace(body) == "" {
			body = ticket.Title
		}
		criteria := "Report the result of " + ticket.Key + " as described."
		var fields map[string]any
		_ = json.Unmarshal(ticket.Fields, &fields)
		if acceptance, ok := fields["acceptance_criteria"].(string); ok && strings.TrimSpace(acceptance) != "" {
			criteria = acceptance
		}
		order, pending, err = workorders.CreateDeferred(ctx, tx, p, workorders.CreateInput{Title: ticket.Key + ": " + ticket.Title, Parent: &ticket.ID, Assignee: &in.Agent, Body: body, Criteria: []string{criteria}})
		if err != nil {
			return nil, err
		}
	}
	if order.Status == "draft" || order.Assignee == nil {
		before := order
		if _, err := tx.Exec(ctx, `UPDATE work_orders SET status=CASE WHEN status='draft' THEN 'ready' ELSE status END,assignee_principal_id=$2,revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1`, order.NodeID, in.Agent); err != nil {
			return nil, err
		}
		order, err = workorders.Load(ctx, tx, order.NodeID, false)
		if err != nil {
			return nil, err
		}
		pending = append(pending, events.Change{NodeID: &order.NodeID, Type: "work_order.updated", Before: before, After: order})
	}
	run, runEvents, err := agentruns.CreatePersonRunTx(ctx, tx, p, order.NodeID, in.CreateInput)
	if err != nil {
		return nil, err
	}
	// No resource locks follow this point: the event counter is always last.
	for _, e := range pendingNodes.items {
		if err := m.events.WriteEvent(ctx, tx, e); err != nil {
			return nil, err
		}
	}
	for _, e := range pending {
		if _, err := events.Append(ctx, tx, p, e); err != nil {
			return nil, err
		}
	}
	for _, appendEvent := range runEvents {
		if err := appendEvent(); err != nil {
			return nil, err
		}
	}
	return run, nil
}
