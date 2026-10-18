// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
)

// Pending pull facts are the only SQL. The product queue request supplies the
// binding; nothing here writes build_delivery fields or an agent_runs trace.
func (f *fixture) deliveryProject(t *testing.T) string {
	t.Helper()
	var project string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Fix project','{"project_key":"FIX"}' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.person.TenantID).Scan(&project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,'owner/linked','CI','Nightly',clock_timestamp())`, f.person.TenantID, project)
		return err
	})
	return project
}

// projectTicket is a ready leaf in the delivery project with nothing pending.
func (f *fixture) projectTicket(t *testing.T, project string) string {
	t.Helper()
	id := f.ticket(t, "open", "high", nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, id, project)
		return err
	})
	return id
}

func (f *fixture) deliveryTicket(t *testing.T, project string, pr int) (string, string) {
	t.Helper()
	id := f.projectTicket(t, project)
	return id, f.deliveryPull(t, id, pr)
}

// deliveryPull opens a pull request on the ticket's exact work/ branch.
func (f *fixture) deliveryPull(t *testing.T, id string, pr int) string {
	t.Helper()
	var branch string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var key string
		if err := tx.QueryRow(t.Context(), `SELECT key FROM nodes WHERE id=$1`, id).Scan(&key); err != nil {
			return err
		}
		branch = "work/" + strings.ToLower(key) + "-fix"
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_pulls(tenant_id,repository,pull_request,head_branch,opened_at,source,recorded_at) VALUES($1,'owner/linked',$2,$3,clock_timestamp(),'webhook',clock_timestamp())`, f.person.TenantID, pr, branch)
		return err
	})
	return branch
}

func queueDelivery(repository, branch string) map[string]any {
	return map[string]any{"repository": repository, "branch": branch}
}

// queuedBinding reads the binding stored on the ticket's queued order.
func (f *fixture) queuedBinding(t *testing.T, id string) *workorders.BuildCandidate {
	t.Helper()
	var orderID string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT work_order_id::text FROM agent_runs WHERE queue_node_id=$1 AND status='queued'`, id).Scan(&orderID)
	})
	var order workorders.Order
	f.call(t, f.person, "GET", "/api/work-orders/"+orderID, nil, 200, &order)
	return order.BuildDelivery
}

// Risk: a pending pull request can be continued only by writing the binding in
// SQL. Queue add must accept it, keep it on the order, and still route and claim.
func TestQueueEnqueueRoutesAndClaimsBoundDelivery(t *testing.T) {
	f := setup(t)
	f.queueAccount(t, 1000000)
	project := f.deliveryProject(t)
	id, branch := f.deliveryTicket(t, project, 42)
	body := map[string]any{"node_id": id, "build_delivery": queueDelivery("owner/linked", branch)}
	var e qEntry
	f.call(t, f.person, "POST", "/api/queue", body, 200, &e)
	if e.Run.ID == "" || e.Run.OrderID == "" || e.NodeID != id {
		t.Fatalf("queue entry %+v", e)
	}
	var order workorders.Order
	f.call(t, f.person, "GET", "/api/work-orders/"+e.Run.OrderID, nil, 200, &order)
	if order.BuildDelivery == nil || *order.BuildDelivery != (workorders.BuildCandidate{Repository: "owner/linked", Branch: branch}) {
		t.Fatalf("order binding %+v", order.BuildDelivery)
	}
	var replay qEntry
	f.call(t, f.person, "POST", "/api/queue", body, 200, &replay)
	if replay.Run.ID != e.Run.ID {
		t.Fatalf("replay run %s, want %s", replay.Run.ID, e.Run.ID)
	}
	var clash struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	}
	f.call(t, f.person, "POST", "/api/queue", map[string]any{"node_id": id, "build_delivery": queueDelivery("owner/linked", "work/other-1-fix")}, 409, &clash)
	if clash.Code != "build_in_flight" || clash.Reason != "built_unmerged" {
		t.Fatalf("different branch %+v", clash)
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, id) != 1 {
		t.Fatal("refused branch created another run")
	}
	target := map[string]string{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}
	var picked struct{ Entry *qEntry }
	f.call(t, f.person, "POST", "/api/queue/next", target, 200, &picked)
	if picked.Entry == nil || picked.Entry.Run.ID != e.Run.ID || picked.Entry.Run.QueueRoutedAt == nil {
		t.Fatalf("bound queue was not routed: %+v", picked)
	}
	message := "continues delivery PR #42 on " + branch
	if got := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='delivery.continued' AND after->>'run_id'=$2 AND after->>'message'=$3`, id, e.Run.ID, message); got != 1 {
		t.Fatalf("route continuation count = %d", got)
	}
	claimed := f.claim(t, e.Run)
	if claimed.Status != "starting" {
		t.Fatalf("claim status %s", claimed.Status)
	}
	var state string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1`, id).Scan(&state)
	})
	if state != "in_progress" {
		t.Fatalf("claim state %s", state)
	}
	if got := f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='delivery.continued' AND after->>'run_id'=$2 AND after->>'message'=$3`, id, e.Run.ID, message); got < 1 {
		t.Fatalf("claim dropped continuation audit: %d", got)
	}
	other, otherBranch := f.deliveryTicket(t, project, 43)
	f.call(t, f.person, "POST", "/api/queue", map[string]string{"node_id": other}, 409, &clash)
	if clash.Code != "build_in_flight" || clash.Reason != "built_unmerged" {
		t.Fatalf("omitted binding %+v", clash)
	}
	f.call(t, f.person, "POST", "/api/queue", map[string]any{"node_id": other, "build_delivery": queueDelivery("owner/linked", "work/other-2-fix")}, 409, &clash)
	if clash.Reason != "built_unmerged" {
		t.Fatalf("non-matching binding %+v", clash)
	}
	var invalid struct {
		Error string `json:"error"`
	}
	f.call(t, f.person, "POST", "/api/queue", map[string]any{"node_id": other, "build_delivery": queueDelivery("owner/linked", "main")}, 400, &invalid)
	if !strings.Contains(invalid.Error, "build_delivery requires") {
		t.Fatalf("invalid branch %s", invalid.Error)
	}
	f.call(t, f.person, "POST", "/api/queue", map[string]any{"node_id": other, "build_delivery": queueDelivery("", otherBranch)}, 400, &invalid)
	if !strings.Contains(invalid.Error, "build_delivery requires") {
		t.Fatalf("empty repository %s", invalid.Error)
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, other) != 0 {
		t.Fatal("refused enqueue created a run")
	}
	// Nothing pending: the binding continues nothing, so the order stays
	// unbound and replay compares that stored (absent) binding.
	plain := f.projectTicket(t, project)
	var stored qEntry
	f.call(t, f.person, "POST", "/api/queue", map[string]any{"node_id": plain, "build_delivery": queueDelivery("owner/linked", "work/plain-1-fix")}, 200, &stored)
	if got := f.queuedBinding(t, plain); got != nil {
		t.Fatalf("ticket with nothing pending stored binding %+v", got)
	}
	f.call(t, f.person, "POST", "/api/queue", map[string]any{"node_id": plain, "build_delivery": queueDelivery("owner/linked", "work/plain-2-fix")}, 200, &replay)
	if replay.Run.ID != stored.Run.ID {
		t.Fatalf("unbound replay %s, want %s", replay.Run.ID, stored.Run.ID)
	}
	// A delivery that became pending after enqueue is never attached silently.
	plainBranch := f.deliveryPull(t, plain, 44)
	var changed struct {
		Error string `json:"error"`
	}
	f.call(t, f.person, "POST", "/api/queue", map[string]any{"node_id": plain, "build_delivery": queueDelivery("owner/linked", plainBranch)}, 409, &changed)
	if !strings.Contains(changed.Error, "another delivery binding") {
		t.Fatalf("changed binding %s", changed.Error)
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, plain) != 1 || f.queuedBinding(t, plain) != nil {
		t.Fatal("refused replay changed the queued order")
	}
}

type deliverySnapshotItem struct {
	NodeID  string `json:"node_id"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	RunID   string `json:"run_id"`
}

type deliverySnapshot struct {
	State string                 `json:"state"`
	Items []deliverySnapshotItem `json:"items"`
}

func applyDeliverySnapshot(t *testing.T, f *fixture, id string, body any) deliverySnapshot {
	t.Helper()
	var out deliverySnapshot
	f.call(t, f.person, "POST", "/api/queue-snapshots/"+id+"/apply", body, 200, &out)
	return out
}

func deliveryLeaf(t *testing.T, s deliverySnapshot, id string) deliverySnapshotItem {
	t.Helper()
	for _, item := range s.Items {
		if item.NodeID == id {
			return item
		}
	}
	t.Fatalf("snapshot missing %s in %+v", id, s.Items)
	return deliverySnapshotItem{}
}

// Risk: snapshot apply ignores a fix-round binding and keeps refusing the leaf,
// or stamps it on a clean sibling whose worker then lands on another branch.
// Enqueue still must not write the continuation comment; route and claim do that.
func TestQueueSnapshotApplyContinuesBoundDelivery(t *testing.T) {
	f := setup(t)
	nodes.New(f.d.App, nil).Mount(f.mux)
	project := f.deliveryProject(t)
	match, branch := f.deliveryTicket(t, project, 42)
	other, _ := f.deliveryTicket(t, project, 43)
	refused := applyDeliverySnapshot(t, f, f.captureParent(t, project).ID, nil)
	for _, id := range []string{match, other} {
		item := deliveryLeaf(t, refused, id)
		if item.Outcome != "active" || item.Reason != "already built — ship it" || item.RunID != "" {
			t.Fatalf("unbound apply %+v", item)
		}
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1 OR queue_node_id=$2`, match, other) != 0 {
		t.Fatal("unbound apply queued a pending delivery")
	}
	clean := f.projectTicket(t, project)
	bound := applyDeliverySnapshot(t, f, f.captureParent(t, project).ID, map[string]any{"build_delivery": queueDelivery("owner/linked", branch)})
	got := deliveryLeaf(t, bound, match)
	skipped := deliveryLeaf(t, bound, other)
	fresh := deliveryLeaf(t, bound, clean)
	if got.Outcome != "queued" || got.RunID == "" || skipped.Outcome != "active" || skipped.Reason != "already built — ship it" || skipped.RunID != "" || fresh.Outcome != "queued" || fresh.RunID == "" {
		t.Fatalf("bound apply match %+v other %+v clean %+v", got, skipped, fresh)
	}
	if binding := f.queuedBinding(t, clean); binding != nil {
		t.Fatalf("clean sibling bound to %+v", binding)
	}
	var orderID string
	for _, item := range f.queuePage(t).Items {
		if item.NodeID == match {
			orderID = item.Run.OrderID
		}
	}
	if orderID == "" {
		t.Fatal("queued leaf is missing from the queue")
	}
	var order workorders.Order
	f.call(t, f.person, "GET", "/api/work-orders/"+orderID, nil, 200, &order)
	if order.BuildDelivery == nil || *order.BuildDelivery != (workorders.BuildCandidate{Repository: "owner/linked", Branch: branch}) {
		t.Fatalf("snapshot order binding %+v", order.BuildDelivery)
	}
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE queue_node_id=$1`, other) != 0 {
		t.Fatal("non-matching leaf was queued")
	}
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE node_id=$1 AND type='delivery.continued'`, match) != 0 {
		t.Fatal("enqueue wrote continuation audit")
	}
}

// Risk: recurrence enqueue stamps a fix-round binding on a ticket whose own
// delivery it does not continue, so that worker is steered onto another branch.
func TestEnqueueSystemStoresOnlyContinuedDelivery(t *testing.T) {
	f := setup(t)
	project := f.deliveryProject(t)
	match, branch := f.deliveryTicket(t, project, 42)
	clean := f.projectTicket(t, project)
	f.addQueue(t, f.projectTicket(t, project), nil) // establishes the queue holder
	binding := workorders.BuildCandidate{Repository: "owner/linked", Branch: branch}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, id := range []string{match, clean} {
			changes, err := workqueue.EnqueueSystem(t.Context(), tx, f.person, id, &binding)
			if err != nil {
				return err
			}
			for _, change := range changes {
				if _, err = events.Append(t.Context(), tx, f.person, change); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if got := f.queuedBinding(t, match); got == nil || *got != binding {
		t.Fatalf("continued delivery binding %+v", got)
	}
	if got := f.queuedBinding(t, clean); got != nil {
		t.Fatalf("clean ticket bound to %+v", got)
	}
}
