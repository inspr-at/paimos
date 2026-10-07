// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func greenShipFacts(head string) ShipFacts {
	n := int64(7)
	out := emptyShipFacts()
	out.PushedHead = head
	out.PullHead = head
	out.Base = strings.Repeat("a", 40)
	out.PR = &n
	out.Open = true
	out.MergeableKnown = true
	for _, name := range *defaults().RequiredChecks {
		out.Checks = append(out.Checks, Check{Name: name, Status: "completed", Conclusion: "success"})
	}
	return out
}
func TestDeliveryShippingHeadBoundActionsAndInfraShards(t *testing.T) {
	// Risk: old-head green checks bypass quarantine/gates, cancelled shards
	// create code fixes, or an unchanged head repeatedly spawns the same fix.
	head := strings.Repeat("b", 40)
	source := Round{RoundInput: RoundInput{Kind: "first_build"}}
	cases := []struct {
		name           string
		change         func(*ShipFacts)
		quarantine     bool
		action, reason string
	}{
		{"green", func(*ShipFacts) {}, false, "enqueue", "current_head_green"},
		{"quarantine", func(*ShipFacts) {}, true, "held", "queue_failure_quarantine"},
		{"open PR", func(f *ShipFacts) { f.PR = nil }, false, "open_pr", "gated_head_pushed"},
		{"draft", func(f *ShipFacts) { f.Draft = true }, false, "update_pr", "gated_draft_ready"},
		{"unknown", func(f *ShipFacts) { f.MergeableKnown = false }, false, "wait", "mergeability_unknown"},
		{"conflict", func(f *ShipFacts) { f.Conflict = true }, false, "merge", "main_moved"},
		{"no PR main moved", func(f *ShipFacts) { f.PR = nil; f.Behind = true }, false, "merge", "main_moved"},
		{"not pushed", func(f *ShipFacts) { f.PushedHead = strings.Repeat("c", 40) }, false, "wait", "launcher_push_required"},
		{"old PR head", func(f *ShipFacts) { f.PullHead = strings.Repeat("c", 40) }, false, "wait", "pull_head_pending"},
		{"queued", func(f *ShipFacts) { f.Queued = true; f.QueueHead = head }, false, "noop", "already_queued"},
		{"synthetic queue head", func(f *ShipFacts) { f.Queued = true; f.QueueHead = strings.Repeat("c", 40) }, false, "noop", "already_queued"},
		{"stale queued PR head", func(f *ShipFacts) { f.Queued = true; f.PullHead = strings.Repeat("c", 40) }, false, "held", "queued_head_changed"},
		{"closed", func(f *ShipFacts) { f.Open = false }, false, "held", "pull_request_closed"},
		{"merged", func(f *ShipFacts) { f.Merged = true }, false, "noop", "already_merged"},
		{"missing check", func(f *ShipFacts) { f.Checks = f.Checks[:1] }, false, "wait", "required_checks_pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ShipDecision{ShipInput: ShipInput{Head: head}, Facts: greenShipFacts(head)}
			tc.change(&out.Facts)
			proposeShip(&out, source, defaults(), tc.quarantine)
			if out.Action != tc.action || out.Reason != tc.reason || out.Execute {
				t.Fatalf("wrong proposal: %+v", out)
			}
		})
	}
	for _, conclusion := range []string{"cancelled", "timed_out"} {
		for _, real := range []bool{false, true} {
			facts := greenShipFacts(head)
			run := ShipRun{ID: 1, Attempt: 2, Head: head, Workflow: "CI", Event: "pull_request", Status: "completed", Conclusion: "failure", Jobs: []Check{{Name: "go", Status: "completed", Conclusion: "failure"}, {Name: "go-test (2)", Status: "completed", Conclusion: conclusion}}}
			want := "rerun_failed"
			if real {
				run.Jobs = append(run.Jobs, Check{Name: "web-unit", Status: "completed", Conclusion: "failure"})
				want = "fix"
			}
			facts.Runs = []ShipRun{run}
			out := ShipDecision{ShipInput: ShipInput{Head: head}, Facts: facts}
			proposeShip(&out, source, defaults(), false)
			if out.Action != want || out.Run == nil || *out.Run != 1 || out.RunAttempt != 2 {
				t.Fatalf("infra classification %s/%v: %+v", conclusion, real, out)
			}
			out.Claimed = true
			key := *shipActionKey(out)
			out.Source = requestID(2)
			out.RunAttempt = 3
			next := *shipActionKey(out)
			if real && next != key || !real && next == key {
				t.Fatal("fix head or rerun attempt ownership drifted")
			}
		}
	}
	out := ShipDecision{ShipInput: ShipInput{Head: head}, Facts: greenShipFacts(head)}
	out.Facts.PushedHead = ""
	source.Kind = "merge"
	proposeShip(&out, source, defaults(), false)
	if out.Action != "land" || out.Round == nil || out.Round.Kind != "land" {
		t.Fatal("completed no-PR merge requires a launcher land proposal")
	}
}

type shipTestReader struct {
	*fakeGitHub
	facts ShipFacts
	read  func()
	err   error
	reads int32
}

func (g *shipTestReader) Shipping(context.Context, string, string) (ShipFacts, error) {
	atomic.AddInt32(&g.reads, 1)
	if g.read != nil {
		g.read()
	}
	return g.facts, g.err
}
func shipPath(f *fixture) string { return "/api/projects/" + f.project + "/delivery-shipping" }
func shipFixture(t *testing.T) (*fixture, Round, *shipTestReader) {
	t.Helper()
	f := newReviewFixture(t)
	f.build(t)
	in, source := reviewSource(t, f, "ship-test", "first_build", 1, strings.Repeat("b", 40))
	profile := seedReviewRoute(t, f)
	gate := ReviewRound{ReviewInput: in, ID: requestID(301), Project: f.project, Ticket: f.ticket, Slug: source.Slug, Key: source.Key, State: "completed", Position: 1, AuthorFamily: "openai", Route: &ReviewRoute{Profile: profile, Family: "anthropic", Harness: "claude", Model: "claude-opus-4-6"}, Action: "ready_to_ship", EffectiveVerdict: "ok", Revision: 3, Updated: f.at, VerdictInput: &ReviewVerdictInput{Profile: profile, Head: in.Head, Verdict: "ok", Findings: []reviewgate.Finding{}}}
	f.tx(t, func(tx pgx.Tx) error { return saveReviewTx(t.Context(), tx, f.person.TenantID, gate) })
	g := &shipTestReader{fakeGitHub: f.gh, facts: greenShipFacts(in.Head)}
	f.m.github = g
	return f, source, g
}
func shippingEvents(t *testing.T, f *fixture) int {
	t.Helper()
	var n int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='delivery.ship.decision'`).Scan(&n)
	})
	return n
}
func enableShipping(t *testing.T, f *fixture) {
	t.Helper()
	f.call(t, f.person, "PUT", shipPath(f)+"/settings", ShipSettings{Mode: "shadow"}, 200, nil)
}

func TestDeliveryShippingShadowOwnershipReplayAndIsolation(t *testing.T) {
	// Risk: shadow creates external actions, multiple claimants own one action,
	// replay loses ownership, or moved/foreign targets leak through receipts.
	f, source, g := shipFixture(t)
	path := shipPath(f)
	in := ShipInput{Request: requestID(1), Source: source.ID, Head: g.facts.PushedHead}
	var page ShipPage
	f.call(t, f.person, "GET", path, nil, 200, &page)
	if page.Settings.Mode != "off" || len(page.Items) != 0 {
		t.Fatal("shipping not default off")
	}
	var off ShipDecision
	f.call(t, f.person, "POST", path+"/claim", in, 200, &off)
	if off.Execute || off.Claimed || off.Reason != "shipping_off" || g.reads != 0 {
		t.Fatal("off claim performed observation or action")
	}
	f.call(t, f.foreign, "POST", path+"/claim", in, 404, nil)
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	f.agent.Scopes = append(f.agent.Scopes, "delivery_ship.claim")
	f.call(t, f.agent, "POST", path+"/claim", in, 403, nil)
	enableShipping(t, f)
	f.call(t, f.person, "PUT", path+"/settings", ShipSettings{Mode: "act", Revision: 1}, 400, nil)
	script := "enqueue"
	in.Request = requestID(2)
	in.ScriptAction = &script
	var first ShipDecision
	f.call(t, f.person, "POST", path+"/claim", in, 200, &first)
	if first.Action != "enqueue" || !first.Claimed || first.Execute || first.Agreement == nil || !*first.Agreement {
		t.Fatalf("wrong shadow claim: %+v", first)
	}
	n := shippingEvents(t, f)
	reads := g.reads
	var retry ShipDecision
	f.call(t, f.person, "POST", path+"/claim", in, 200, &retry)
	if !reflect.DeepEqual(first, retry) || shippingEvents(t, f) != n || g.reads != reads {
		t.Fatal("retry repeated observation/decision")
	}
	changed := in
	changed.Head = strings.Repeat("c", 40)
	f.call(t, f.person, "POST", path+"/claim", changed, 409, nil)
	in.Request = requestID(3)
	var duplicate ShipDecision
	f.call(t, f.person, "POST", path+"/claim", in, 200, &duplicate)
	if duplicate.Claimed || duplicate.Action != "wait" || duplicate.Reason != "action_already_claimed" {
		t.Fatal("same action had two owners")
	}
	f.call(t, f.person, "GET", path+"?limit=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Next == nil {
		t.Fatal("keyset page omitted cursor")
	}
	if err := f.m.RebuildShipping(t.Context(), f.person, f.project); err != nil {
		t.Fatal(err)
	}
	in.Request = requestID(2)
	f.call(t, f.person, "POST", path+"/claim", in, 200, &retry)
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("replay changed claim")
	}
	var decisions int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_ship_decisions WHERE project_id=$1`, f.project).Scan(&decisions)
	})
	if decisions != 3 {
		t.Fatalf("replay lost receipts: %d", decisions)
	}
	// Replay does not create PRs, delivery queue rows, gate statuses or work orders.
	f.tx(t, func(tx pgx.Tx) error {
		var rounds int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM delivery_work_rounds WHERE project_id=$1`, f.project).Scan(&rounds); err != nil {
			return err
		}
		if rounds != 1 {
			t.Fatal("shadow created worker rounds")
		}
		return nil
	})
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=$2 WHERE id=$1`, f.ticket, f.at)
		return err
	})
	f.call(t, f.person, "POST", path+"/claim", in, 404, nil)
	f.call(t, f.person, "GET", path, nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("deleted target leaked through list")
	}
}

func TestDeliveryShippingRechecksRevocationAndPendingRoundsAfterRead(t *testing.T) {
	// Risk: a network gap lets a revoked claimant or a pending fix ship an old
	// green head. The injected callback is an exact read/write interleaving.
	f, source, g := shipFixture(t)
	enableShipping(t, f)
	path := shipPath(f)
	f.agent.Scopes = append(f.agent.Scopes, "delivery_ship.claim")
	f.tx(t, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'ship_controller','Ship controller') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'delivery_ship.claim'),($1,$2,'nodes.read')`, f.person.TenantID, role)
		return err
	})
	dbtest.BindRole(t, f.d, f.agent.TenantID, f.agent.ID, "ship_controller")
	in := ShipInput{Request: requestID(11), Source: source.ID, Head: g.facts.PushedHead}
	g.read = func() {
		f.tx(t, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE permission='delivery_ship.claim' AND role_id IN (SELECT id FROM roles WHERE key='ship_controller')`)
			return err
		})
	}
	f.call(t, f.agent, "POST", path+"/claim", in, 403, nil)
	if shippingEvents(t, f) != 0 {
		t.Fatal("revoked claim committed")
	}
	g.read = func() {
		r := source
		r.ID = requestID(401)
		r.Kind = "fix"
		r.Number = 2
		r.Position++
		r.State = "queued"
		f.tx(t, func(tx pgx.Tx) error { return saveRoundTx(t.Context(), tx, f.person.TenantID, r) })
	}
	var out ShipDecision
	f.call(t, f.person, "POST", path+"/claim", in, 200, &out)
	if out.Action != "held" || out.Reason != "round_pending" || out.Claimed {
		t.Fatalf("pending fix was ignored: %+v", out)
	}
}

func TestDeliveryShippingFixDedupHoldsAndQuarantine(t *testing.T) {
	f, source, g := shipFixture(t)
	enableShipping(t, f)
	path := shipPath(f)
	in := ShipInput{Request: requestID(21), Source: source.ID, Head: g.facts.PushedHead}
	g.facts.Runs = []ShipRun{{ID: 42, Attempt: 1, Head: in.Head, Workflow: "CI", Event: "pull_request", Status: "completed", Conclusion: "failure", Jobs: []Check{{Name: "web-unit", Status: "completed", Conclusion: "failure"}}}}
	var out ShipDecision
	f.call(t, f.person, "POST", path+"/claim", in, 200, &out)
	if out.Action != "fix" || out.Round == nil || out.Round.Kind != "fix" || !out.Claimed {
		t.Fatal("real failure did not propose fix")
	}
	g.facts.Runs[0].Attempt = 2
	in.Request = requestID(22)
	f.call(t, f.person, "POST", path+"/claim", in, 200, &out)
	if out.Reason != "action_already_claimed" || out.Round != nil {
		t.Fatal("same failed head spawned another fix")
	}
	g.facts.Runs = nil
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_queue_failures(tenant_id,repository,pull_request,head_sha,check_name,workflow,conclusion,kind,check_run_id,at) VALUES($1,$2,7,$3,'go','CI','failure','required_failure',99,$4)`, f.person.TenantID, f.m.config.Repository, in.Head, f.at)
		return err
	})
	in.Request = requestID(23)
	f.call(t, f.person, "POST", path+"/claim", in, 200, &out)
	if out.Reason != "queue_failure_quarantine" || out.Claimed {
		t.Fatal("quarantine bypassed")
	}
	f.tx(t, func(tx pgx.Tx) error {
		return saveQueueSettingsTx(t.Context(), tx, f.person.TenantID, f.project, QueueSettings{Mode: "shadow", Freeze: true, ReleaseSet: []string{}, HeldSlugs: []string{}, HeldPRs: []int64{}})
	})
	in.Request = requestID(24)
	f.call(t, f.person, "POST", path+"/claim", in, 200, &out)
	if out.Reason != "release_freeze" {
		t.Fatal("freeze ignored")
	}
	f.tx(t, func(tx pgx.Tx) error {
		return saveQueueSettingsTx(t.Context(), tx, f.person.TenantID, f.project, QueueSettings{Mode: "shadow", HeldSlugs: []string{source.Slug}})
	})
	in.Request = requestID(25)
	f.call(t, f.person, "POST", path+"/claim", in, 200, &out)
	if out.Reason != "slug_hold" {
		t.Fatal("slug hold ignored")
	}
	f.tx(t, func(tx pgx.Tx) error {
		return saveQueueSettingsTx(t.Context(), tx, f.person.TenantID, f.project, queueDefaults())
	})
	g.err = errors.New("partial response")
	in.Request = requestID(26)
	f.call(t, f.person, "POST", path+"/claim", in, 200, &out)
	if out.Reason != "github_unavailable" || out.Action != "wait" {
		t.Fatal("partial read became green")
	}
	// Calls remain read-only even when no verified platform gate exists.
	var reviews int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM work_order_reviews`).Scan(&reviews)
	})
	if reviews != 0 {
		t.Fatal("shadow manufactured verified approval")
	}
}

func TestDeliveryShippingConcurrentClaimantsOwnOneAction(t *testing.T) {
	// Risk: two clients read the same green head before either writes, then
	// both own enqueue. A barrier makes the network reads overlap deterministically.
	f, source, g := shipFixture(t)
	enableShipping(t, f)
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	g.read = func() { arrived <- struct{}{}; <-release }
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 1; i <= 2; i++ {
		go func(i int) {
			raw, _ := json.Marshal(ShipInput{Request: requestID(30 + i), Source: source.ID, Head: g.facts.PushedHead})
			r := httptest.NewRequest("POST", shipPath(f)+"/claim", strings.NewReader(string(raw)))
			r = r.WithContext(tenant.WithPrincipal(r.Context(), f.person))
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)
			results <- w
		}(i)
	}
	for range 2 {
		select {
		case <-arrived:
		case <-t.Context().Done():
			t.Fatal("reads never overlapped")
		}
	}
	close(release)
	claimed := 0
	for range 2 {
		w := <-results
		if w.Code != 200 {
			t.Fatalf("claim: %d %s", w.Code, w.Body.String())
		}
		var out ShipDecision
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Claimed {
			claimed++
		} else if out.Reason != "action_already_claimed" {
			t.Fatalf("unexpected rejection: %+v", out)
		}
	}
	if claimed != 1 || shippingEvents(t, f) != 2 {
		t.Fatal("concurrent decisions lost single ownership")
	}
}
