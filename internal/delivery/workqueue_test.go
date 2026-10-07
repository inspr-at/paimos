// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func queuePath(f *fixture) string { return "/api/projects/" + f.project + "/delivery-queue" }
func requestID(n int) string      { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
func roundInput(f *fixture, slug, kind string, n int) RoundInput {
	return RoundInput{Ticket: f.ticket, Slug: slug, Kind: kind, Number: n, Brief: "briefs/" + slug + ".txt", Base: "origin/main", Estimate: 60}
}
func grantQueueAgent(t *testing.T, f *fixture) {
	t.Helper()
	f.agent.Scopes = append(f.agent.Scopes, "delivery_queue.read", "delivery_queue.manage", "delivery_queue.claim")
	f.tx(t, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'queue_launcher','Queue launcher') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'delivery_queue.read'),($1,$2,'delivery_queue.claim'),($1,$2,'delivery_queue.manage')`, f.person.TenantID, role)
		return err
	})
	dbtest.BindRole(t, f.d, f.agent.TenantID, f.agent.ID, "queue_launcher")
}
func queueEvents(t *testing.T, f *fixture) int {
	t.Helper()
	var n int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type LIKE 'delivery.work_queue.%'`).Scan(&n)
	})
	return n
}

func TestDeliveryWorkQueueShadowRotationFreezeReplayAndIsolation(t *testing.T) {
	// R1/R2/R3/R6/R7: incorrect authority, tenant leakage, head-of-line stalls,
	// lost audit/idempotency, or a shadow decision accidentally starts a worker.
	f := newFixture(t)
	path := queuePath(f)
	var page RoundPage
	f.call(t, f.person, "GET", path, nil, 200, &page)
	if page.Settings.Mode != "off" || len(page.Items) != 0 {
		t.Fatal("queue was not default off")
	}
	var a, b Round
	f.call(t, f.person, "POST", path, roundInput(f, "held-branch", "first_build", 1), 201, &a)
	f.call(t, f.person, "POST", path, roundInput(f, "ready-branch", "fix", 1), 201, &b)
	events := queueEvents(t, f)
	var duplicate Round
	f.call(t, f.person, "POST", path, roundInput(f, "held-branch", "first_build", 1), 200, &duplicate)
	if !reflect.DeepEqual(a, duplicate) || queueEvents(t, f) != events {
		t.Fatal("enqueue retry changed the round or repeated an event")
	}
	changed := roundInput(f, "held-branch", "first_build", 1)
	changed.Base = "origin/other"
	f.call(t, f.person, "POST", path, changed, 409, nil)
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	f.call(t, f.foreign, "POST", path+"/claim", ClaimInput{Request: requestID(1)}, 404, nil)
	f.call(t, f.person, "GET", path+"?limit=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Next == nil || page.Items[0].ID != a.ID {
		t.Fatal("first queue page incorrect")
	}
	f.call(t, f.person, "GET", path+"?limit=1&after="+*page.Next, nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].ID != b.ID || page.Next != nil {
		t.Fatal("position keyset lost a round")
	}
	var off QueueClaim
	f.call(t, f.person, "POST", path+"/claim", ClaimInput{Request: requestID(2)}, 200, &off)
	if off.Execute || off.Round != nil || off.Reason != "queue_off" {
		t.Fatal("disabled queue admitted work")
	}
	s := queueDefaults()
	s.Mode = "shadow"
	s.HeldSlugs = []string{a.Slug}
	f.call(t, f.person, "PUT", path+"/settings", s, 200, &s)
	var denied QueueClaim
	f.call(t, f.person, "POST", path+"/claim", ClaimInput{Request: requestID(3)}, 200, &denied)
	if denied.Round != nil || len(denied.Decisions) != 2 || denied.Decisions[0].Reason != "slug_hold" || denied.Decisions[1].Reason != "admission_unavailable" {
		t.Fatal("unconfigured admission did not fail closed", denied)
	}
	f.agent.Scopes = append(f.agent.Scopes, "delivery_queue.claim")
	f.call(t, f.agent, "POST", path+"/claim", ClaimInput{Request: requestID(4)}, 403, nil)
	grantQueueAgent(t, f)
	f.m.SetQueueAdmission(func(context.Context, pgx.Tx, tenant.Principal, Round) (bool, string, error) {
		return true, "admitted", nil
	})
	var claim QueueClaim
	f.call(t, f.agent, "POST", path+"/claim", ClaimInput{Request: requestID(5), ScriptRound: &b.ID}, 200, &claim)
	if claim.Execute || claim.Round == nil || claim.Round.ID != b.ID || claim.Round.State != "claimed" || claim.Agreement == nil || !*claim.Agreement || *claim.Round.Claimant != f.agent.ID {
		t.Fatal("wrong shadow claim", claim)
	}
	events = queueEvents(t, f)
	var replay QueueClaim
	f.call(t, f.agent, "POST", path+"/claim", ClaimInput{Request: requestID(5), ScriptRound: &b.ID}, 200, &replay)
	if !reflect.DeepEqual(claim, replay) || events != queueEvents(t, f) {
		t.Fatal("claim retry was not exactly idempotent")
	}
	f.call(t, f.person, "POST", path+"/claim", ClaimInput{Request: requestID(5), ScriptRound: &b.ID}, 409, nil)
	f.call(t, f.agent, "POST", path+"/claim", ClaimInput{Request: requestID(5), ScriptRound: &a.ID}, 409, nil)
	f.call(t, f.person, "POST", path+"/"+b.ID+"/progress", map[string]any{"revision": claim.Round.Revision, "state": "running"}, 403, nil)
	var running Round
	f.call(t, f.agent, "POST", path+"/"+b.ID+"/progress", map[string]any{"revision": claim.Round.Revision, "state": "running"}, 200, &running)
	f.call(t, f.agent, "POST", path+"/"+b.ID+"/progress", map[string]any{"revision": claim.Round.Revision, "state": "done"}, 409, nil)
	f.call(t, f.agent, "POST", path+"/"+b.ID+"/progress", map[string]any{"revision": running.Revision, "state": "done"}, 200, &running)
	f.call(t, f.person, "PATCH", path+"/"+b.ID, map[string]any{"revision": running.Revision, "state": "queued"}, 409, nil)
	s.Freeze = true
	s.ReleaseSet = []string{"ready-"}
	f.call(t, f.person, "PUT", path+"/settings", s, 200, &s)
	f.call(t, f.person, "POST", path+"/claim", ClaimInput{Request: requestID(6)}, 200, &denied)
	if len(denied.Decisions) != 1 || denied.Decisions[0].Action != "park" || denied.Decisions[0].Reason != "release_freeze" {
		t.Fatal("freeze did not park excluded work", denied)
	}
	f.call(t, f.person, "GET", path+"?ticket="+f.ticket, nil, 200, &page)
	var parked Round
	for _, r := range page.Items {
		if r.ID == a.ID {
			parked = r
		}
	}
	if parked.State != "parked" {
		t.Fatal("parked state missing")
	}
	s.Freeze = false
	s.HeldSlugs = []string{}
	f.call(t, f.person, "PUT", path+"/settings", s, 200, &s)
	f.call(t, f.person, "PATCH", path+"/"+a.ID, map[string]any{"revision": parked.Revision, "state": "queued", "hold_reason": "Review pending"}, 200, &parked)
	f.call(t, f.person, "POST", path+"/claim", ClaimInput{Request: requestID(7)}, 200, &denied)
	if len(denied.Decisions) != 1 || denied.Decisions[0].Reason != "round_hold" {
		t.Fatal("round hold bypassed")
	}
	f.call(t, f.person, "GET", path, nil, 200, &page)
	before := page
	if err := f.m.RebuildWorkQueue(t.Context(), f.person, f.project); err != nil {
		t.Fatal(err)
	}
	f.call(t, f.person, "GET", path, nil, 200, &page)
	if !reflect.DeepEqual(before, page) {
		t.Fatal("event replay changed rounds or settings")
	}
	f.call(t, f.agent, "POST", path+"/claim", ClaimInput{Request: requestID(5), ScriptRound: &b.ID}, 200, &replay)
	if !reflect.DeepEqual(claim, replay) {
		t.Fatal("event replay lost claim idempotency")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE permission='delivery_queue.claim' AND role_id=(SELECT id FROM roles WHERE key='queue_launcher')`)
		return err
	})
	f.call(t, f.agent, "POST", path+"/claim", ClaimInput{Request: requestID(5), ScriptRound: &b.ID}, 403, nil)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		for _, table := range []string{"delivery_work_rounds", "delivery_work_settings", "delivery_work_claims"} {
			var n int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE tenant_id=$1`, f.person.TenantID).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("tenant leaked through %s", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryWorkQueueConcurrentClaimOwnsSlugOnce(t *testing.T) {
	// R4/R7: two launchers race, or a later round of the same slug starts
	// while its predecessor is owned. Prove the overlap with PostgreSQL locks.
	f := newFixture(t)
	grantQueueAgent(t, f)
	path := queuePath(f)
	var round Round
	f.call(t, f.person, "POST", path, roundInput(f, "one-branch", "first_build", 1), 201, &round)
	f.call(t, f.person, "POST", path, roundInput(f, "one-branch", "fix", 1), 201, nil)
	s := queueDefaults()
	s.Mode = "shadow"
	f.call(t, f.person, "PUT", path+"/settings", s, 200, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	f.m.SetQueueAdmission(func(ctx context.Context, _ pgx.Tx, _ tenant.Principal, _ Round) (bool, string, error) {
		close(entered)
		select {
		case <-release:
			return true, "admitted", nil
		case <-ctx.Done():
			return false, "", ctx.Err()
		}
	})
	type result struct {
		out QueueClaim
		err error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	pid := make(chan int32, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	run := func(n int, backend chan int32, output chan result) {
		var out QueueClaim
		err := db.InTenant(tenant.WithPrincipal(ctx, f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
			if backend != nil {
				var id int32
				if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&id); err != nil {
					return err
				}
				backend <- id
			}
			if err := db.LockTenant(ctx, tx, f.agent.TenantID); err != nil {
				return err
			}
			if err := authz.RequireTx(ctx, tx, f.agent, "delivery_queue.claim", authz.Scope{ProjectID: f.project}); err != nil {
				return err
			}
			var err error
			out, err = f.m.claimQueueTx(ctx, tx, f.agent, f.project, ClaimInput{Request: requestID(n)})
			return err
		})
		output <- result{out, err}
	}
	go run(10, nil, first)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first claim did not reach admission")
	}
	go run(11, pid, second)
	var backend int32
	select {
	case backend = <-pid:
	case <-ctx.Done():
		t.Fatal("second claim did not begin")
	}
	for {
		var blocked bool
		if err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted)`, backend).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
	}
	close(release)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil {
		t.Fatal(a.err, b.err)
	}
	if a.out.Round == nil || a.out.Round.ID != round.ID || b.out.Round != nil || len(b.out.Decisions) != 1 || b.out.Decisions[0].Reason != "slug_running" {
		t.Fatal("concurrent claim duplicated slug ownership", a.out, b.out)
	}
	if a.out.Execute || b.out.Execute {
		t.Fatal("shadow claim gained execution authority")
	}
}
