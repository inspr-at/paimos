// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// AEON-1414 risks: account polling stalls ticket saves; removing its write
// fences must retain capacity, quota and routing decisions from one snapshot.
func TestCapacityReadSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	f := limitWorldAt(t, "capacity-read-snapshot", 3, now)
	f.report(t, 30, now.Add(-time.Minute), now.Add(80*time.Hour))
	seedExistingLimit(t, f, 80, "percent", "day", nil)
	var kind, nodeID string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE slug='work'`).Scan(&kind); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title) VALUES($1,$2,'Polling must not block edits') RETURNING id::text`, f.admin.TenantID, kind).Scan(&nodeID)
	}); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/api/agent-accounts", "/api/agent-accounts/capacity", "/api/agent-accounts/groups", "/api/agent-accounts/pins?ticket_id=" + nodeID, "/api/agent-accounts/capacity/schedule", "/api/agent-accounts/" + f.account.ID + "/readings", "/api/agent-accounts/overview", "/api/agent-accounts/capacity/next?harness=codex"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			pool, barrier, ctx := dbtest.BarrierPool(t, appPool, func(sql string) bool {
				return strings.Contains(sql, "FROM agent_accounts") || strings.Contains(sql, "FROM account_groups") || strings.Contains(sql, "FROM account_ticket_pins") || strings.Contains(sql, "FROM account_capacity_schedules")
			})
			mod := fixedClockModule{Module: &Module{pool: pool}, at: now}
			read := make(chan *httptest.ResponseRecorder, 1)
			go func() { read <- evidenceHTTP(mod, ctx, f.admin, "", "GET", path, "", now) }()
			holder := barrier.Wait(t, ctx)
			patch := make(chan *httptest.ResponseRecorder, 1)
			done := make(chan struct{})
			// One second bounds a hung save; the database barrier and lock graph
			// establish overlap, without a sleep or elapsed-time assertion.
			patchCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			go func() {
				patch <- evidenceHTTP(nodes.New(appPool, nil), patchCtx, f.admin, "", "PATCH", "/api/nodes/"+nodeID, `{"title":"Saved while reading capacity"}`, now)
				close(done)
			}()
			if lock := dbtest.BlockedOrDone(t, patchCtx, adminPool, holder, done); lock != "" {
				t.Fatalf("account GET blocks node PATCH on %s", lock)
			}
			if got := dbtest.Await(t, ctx, patch); got.Code != http.StatusOK {
				t.Fatalf("node PATCH: %d %s", got.Code, got.Body.String())
			}
			barrier.Release()
			if got := dbtest.Await(t, ctx, read); got.Code != http.StatusOK {
				t.Fatalf("account GET: %d %s", got.Code, got.Body.String())
			}
		})
	}
}

func TestCapacityProjectionBatchAndGolden(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	f := limitWorldAt(t, "capacity-batch", 3, now)
	seed := func(a Account, index int) {
		t.Helper()
		f2 := f
		f2.account = a
		f2.report(t, 30, now.Add(-time.Minute), now.Add(time.Duration(80+index)*time.Hour))
		seedExistingLimit(t, f2, 80, "percent", "day", nil)
	}
	seed(f.account, 0)
	project := func() ([]accountCapacity, dbtest.ReadWork) {
		t.Helper()
		var out []accountCapacity
		var work dbtest.ReadWork
		ctx := context.WithValue(tenant.WithPrincipal(t.Context(), f.admin), clockKey{}, now)
		if err := db.InTenantReadSnapshot(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
			var err error
			out, err = projectCapacity(ctx, dbtest.CountReads(tx, &work), f.admin.ID, nil, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out, work
	}
	one, small := project()
	if len(one) != 1 || one[0].Routing == nil || one[0].Routing.Rank != 1 || one[0].Routing.AvailableSlots < 1 {
		t.Fatalf("fixture must exercise live routing: %+v", one)
	}
	// Stable identities and audit timestamps leave every computed field in the
	// golden, including pacing, limits, learning and routing advice.
	one[0].AccountID = "account"
	one[0].Limit.ID = "limit"
	one[0].Limit.AccountID = "account"
	one[0].Limit.CreatedAt = now
	raw, err := json.MarshalIndent(one, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/capacity-projection.golden.json")
	if err != nil || strings.TrimSpace(string(golden)) != string(raw) {
		t.Errorf("capacity projection golden mismatch (%v):\n%s", err, raw)
	}
	for i := 1; i < 20; i++ {
		var a Account
		callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", fmt.Sprintf(`{"account_key":"batch-%d","harness":"codex","daemon_id":"daemon-a","label":"Batch","max_parallel_runs":3}`, i), 201, &a)
		ownFixtureAccount(t, f.admin, &a)
		callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
		s := one[0].Schedule
		seedExistingSchedule(t, f.admin, scheduleOverride{Scope: "account", AccountID: a.ID, Schedule: &s})
		seed(a, i)
	}
	all, large := project()
	if len(all) != 20 {
		t.Fatalf("lost accounts: %d", len(all))
	}
	if large.Statements != small.Statements {
		t.Errorf("capacity statements grew with accounts: 1=%d, 20=%d", small.Statements, large.Statements)
	}
	if large.Rows > 30*small.Rows+100 {
		t.Errorf("capacity rows grew beyond account data: 1=%d, 20=%d", small.Rows, large.Rows)
	}
	t.Logf("capacity read work: 1=%+v 20=%+v", small, large)
}

// capacityWorld registers one account per harness with a fresh weekly reading.
func capacityWorld(t *testing.T, slug string, harnesses ...string) (tenant.Principal, tenant.Principal, string, map[string]Account) {
	t.Helper()
	reset(t)
	admin := makePrincipal(t, slug, "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	now := time.Now().UTC()
	out := map[string]Account{}
	for _, h := range harnesses {
		var a Account
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"`+h+`","harness":"`+h+`","daemon_id":"daemon-a","label":"`+h+`"}`, 201, &a)
		ownFixtureAccount(t, admin, &a)
		r := capacity.Reading{WindowKind: "weekly", WindowMinutes: 7 * 24 * 60, UsedPercent: 30, ResetsAt: now.Add(80 * time.Hour), ReadAt: now.Add(-time.Minute), Source: "harness"}
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
		out[h] = a
	}
	return admin, runner, token, out
}

func byAccount(list []accountCapacity) map[string]accountCapacity {
	out := map[string]accountCapacity{}
	for _, c := range list {
		out[c.AccountID] = c
	}
	return out
}

// Review blocker 3 (and 1): the preview follows exactly the inheritance a save
// applies. A deliberate 3-day Grok pool schedule stays; a Hold that only rides
// on the person's schedule follows it; an ended Sprint is removed. The save is
// one transaction with carry_overrides.
func TestCapacityPreviewMatchesSavedInheritance(t *testing.T) {
	admin, _, _, accounts := capacityWorld(t, "carry", "codex", "grok")
	mod := accountsMod()
	put := func(body scheduleOverride, status int) {
		t.Helper()
		callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, body), status, nil)
	}
	user := capacity.DefaultSchedule("Europe/Vienna")
	put(scheduleOverride{Scope: "user", Schedule: &user}, 204)
	grok := user
	grok.Week = capacity.Preset(3)
	put(scheduleOverride{Scope: "pool", Pool: "grok", Schedule: &grok}, 204)
	hold := user
	hold.Override = "hold"
	seedExistingSchedule(t, admin, scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &hold})
	// An ended Sprint left behind on a pool without accounts.
	ended := user
	ended.Override = "sprint"
	past := time.Now().Add(-time.Hour)
	ended.OverrideUntil = &past
	raw, _ := json.Marshal(ended)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,schedule) VALUES($1,$2,'pool','cursor',$3)`, admin.TenantID, admin.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	draft := capacity.DefaultSchedule("Europe/Vienna")
	draft.Week = capacity.Preset(7)
	var preview []accountCapacity
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", encoded(t, map[string]any{"schedule": draft}), 200, &preview)
	put(scheduleOverride{Scope: "user", Schedule: &draft, CarryOverrides: true}, 204)
	var saved []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &saved)

	before, after := byAccount(preview), byAccount(saved)
	for h, a := range accounts {
		p, s := before[a.ID], after[a.ID]
		if !sameShape(p.Schedule, s.Schedule) || p.Schedule.Override != s.Schedule.Override || len(p.Windows) != 1 || len(s.Windows) != 1 {
			t.Fatalf("%s: preview %+v, saved %+v", h, p.Schedule, s.Schedule)
		}
		// Usable hours count from now, which moves between the two reads.
		if pw, sw := p.Windows[0].Pacing, s.Windows[0].Pacing; pw.BudgetPercent != sw.BudgetPercent || pw.TonightPercent != sw.TonightPercent || math.Abs(pw.UsableHours-sw.UsableHours) > 0.01 {
			t.Fatalf("%s: preview paced %+v, saved paced %+v", h, pw, sw)
		}
	}
	if d := workDays(after[accounts["grok"].ID].Schedule); d != 3 {
		t.Fatal("deliberate Grok schedule was overwritten", d)
	}
	if c := after[accounts["codex"].ID].Schedule; workDays(c) != 7 || c.Override != "hold" {
		t.Fatal("Hold did not follow the new week", c)
	}
	if n := scalar(t, admin, `SELECT count(*) FROM account_capacity_schedules WHERE scope='pool' AND scope_key='cursor'`); n != 0 {
		t.Fatal("ended Sprint entry kept", n)
	}
	put(scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &draft, CarryOverrides: true}, 400)
}

// The limiting reset includes the 5-hour window; legacy Sprint end dates
// survive unchanged round-trips and rejected deadline extensions.
func TestCapacityLimitingResetPreservesExistingSprintEnd(t *testing.T) {
	admin, runner, token, accounts := capacityWorld(t, "limiting", "claude")
	mod := accountsMod()
	now := time.Now().UTC()
	five := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 40, ResetsAt: now.Add(2 * time.Hour).Truncate(time.Second), ReadAt: now.Add(-time.Second), Source: "harness"}
	weekly := capacity.Reading{WindowKind: "weekly", WindowMinutes: 7 * 24 * 60, UsedPercent: 60, ResetsAt: now.Add(90 * time.Hour), ReadAt: now.Add(-time.Second), Source: "harness"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+accounts["claude"].ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{five, weekly}}), 204, nil)
	var list []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &list)
	limit := list[0].LimitingReset
	if limit == nil || !limit.Equal(five.ResetsAt) {
		t.Fatalf("limiting reset %v, want the 5-hour reset %v", limit, five.ResetsAt)
	}
	sprint := capacity.DefaultSchedule()
	sprint.Override = "sprint"
	sprint.OverrideUntil = limit
	seedExistingSchedule(t, admin, scheduleOverride{Scope: "pool", Pool: "claude", Schedule: &sprint})
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "pool", Pool: "claude", Schedule: &sprint}), 204, nil)
	// Only new or changed overrides are retired (AEON-1177 M1). Keep the
	// rejection assertion on an actual change, without changing the fixture.
	extended := sprint
	later := limit.Add(time.Hour)
	extended.OverrideUntil = &later
	var retired struct{ Error, Code string }
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "pool", Pool: "claude", Schedule: &extended}), 400, &retired)
	if retired.Code != "override_retired" || retired.Error == "" {
		t.Fatalf("Sprint extension rejected for the wrong reason: %+v", retired)
	}
	var entries []scheduleOverride
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity/schedule", "", 200, &entries)
	if len(entries) != 1 || entries[0].Schedule.OverrideUntil == nil || !entries[0].Schedule.OverrideUntil.Equal(*limit) {
		t.Fatalf("Sprint end %+v differs from limiting reset %v", entries, limit)
	}
	if entries[0].Scope != "pool" || entries[0].Pool != "claude" || encoded(t, entries[0].Schedule) != encoded(t, sprint) {
		t.Fatalf("Sprint replay or rejected extension changed the stored schedule: %+v", entries)
	}
}

// Review blocker 2: the capacity projection names why the last probe failed;
// only a confirmed sign-out is auth_failed.
func TestCapacityProbeFailureCause(t *testing.T) {
	admin, runner, token, accounts := capacityWorld(t, "probe-cause", "codex")
	mod := accountsMod()
	path := "/api/agent-accounts/" + accounts["codex"].ID + "/probe"
	cause := func() string {
		t.Helper()
		var list []accountCapacity
		callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &list)
		return list[0].ProbeFailure
	}
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":false,"failure":"auth_failed"}`, 200, nil)
	if c := cause(); c != "auth_failed" {
		t.Fatal(c)
	}
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":false}`, 200, nil)
	if c := cause(); c != "unavailable" {
		t.Fatal("an unexplained failure must not claim a sign-out", c)
	}
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	if c := cause(); c != "" {
		t.Fatal(c)
	}
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true,"failure":"auth_failed"}`, 400, nil)
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":false,"failure":"expired"}`, 400, nil)
}

// Review blocker 6: the preview is bounded per person, in concurrency, in time
// and in total integration work.
func TestCapacityPreviewBounds(t *testing.T) {
	admin, _, _, _ := capacityWorld(t, "preview-bounds", "codex")
	body := encoded(t, map[string]any{"schedule": capacity.DefaultSchedule("Europe/Vienna")})
	path := "/api/agent-accounts/capacity/preview"
	rate := &Module{pool: appPool, preview: newPreviewGuard(2, time.Minute, 4, previewBudget, previewTimeout)}
	callStatus(t, rate, &admin, "", "POST", path, body, 200, nil)
	callStatus(t, rate, &admin, "", "POST", path, body, 200, nil)
	callStatus(t, rate, &admin, "", "POST", path, body, 429, nil)
	busy := newPreviewGuard(10, time.Minute, 1, previewBudget, previewTimeout)
	busy.acquire()
	callStatus(t, &Module{pool: appPool, preview: busy}, &admin, "", "POST", path, body, 503, nil)
	busy.release()
	callStatus(t, &Module{pool: appPool, preview: busy}, &admin, "", "POST", path, body, 200, nil)
	small := &Module{pool: appPool, preview: newPreviewGuard(10, time.Minute, 4, 10, previewTimeout)}
	callStatus(t, small, &admin, "", "POST", path, body, 413, nil)
	late := &Module{pool: appPool, preview: newPreviewGuard(10, time.Minute, 4, previewBudget, time.Nanosecond)}
	// One deadline covers body and work: it ends as 408 (body) or 503 (work).
	if status, _ := call(t, late, &admin, "", "POST", path, body); status != http.StatusRequestTimeout && status != http.StatusServiceUnavailable {
		t.Fatal("past deadline answered", status)
	}
	// A rejected preview holds no slot: the busy guard still serves.
	if !busy.acquire() {
		t.Fatal("slot leaked")
	}
	busy.release()
}

// previewStalledBody confirms that ServeHTTP has actually started reading,
// then blocks until the test expires its deadline or releases the pipe.
type previewStalledBody struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (b *previewStalledBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

// A slow or unfinished body is read under the preview deadline and before a
// slot is taken. Control deadline expiry explicitly so scheduler load cannot
// expire the second person's valid preview while proving that invariant.
func TestCapacityPreviewSlowBodyHoldsNoSlot(t *testing.T) {
	admin, _, _, _ := capacityWorld(t, "preview-slow", "codex")
	other := addPrincipal(t, admin.TenantID, "person", "Second", []string{"admin"})
	guard := newPreviewGuard(10, time.Minute, 1, previewBudget, previewTimeout)
	expire := make(chan context.CancelCauseFunc, 4)
	guard.withTimeout = func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
		if timeout != previewTimeout {
			panic("preview timeout changed")
		}
		ctx, cancel := context.WithCancelCause(parent)
		if p, _ := tenant.PrincipalFrom(parent); p.ID == admin.ID {
			expire <- cancel
		}
		return ctx, func() { cancel(context.Canceled) }
	}
	mod := &Module{pool: appPool, preview: guard}
	mux := http.NewServeMux()
	mod.Mount(mux)
	type slow struct {
		rec    *httptest.ResponseRecorder
		done   chan struct{}
		expire context.CancelCauseFunc
	}
	var stalled []slow
	for range 4 {
		reader, writer := io.Pipe()
		body := &previewStalledBody{ReadCloser: reader, started: make(chan struct{})}
		r := httptest.NewRequest("POST", "/api/agent-accounts/capacity/preview", body)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), admin))
		s := slow{rec: httptest.NewRecorder(), done: make(chan struct{})}
		go func() { mux.ServeHTTP(s.rec, r); close(s.done) }()
		t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
		select {
		case s.expire = <-expire:
		case <-time.After(5 * time.Second):
			t.Fatal("stalled request never established its deadline")
		}
		t.Cleanup(func() { s.expire(context.Canceled) })
		select {
		case <-body.started:
		case <-time.After(5 * time.Second):
			t.Fatal("stalled request never started reading its body")
		}
		stalled = append(stalled, s)
	}
	body := encoded(t, map[string]any{"schedule": capacity.DefaultSchedule("Europe/Vienna")})
	callStatus(t, mod, &other, "", "POST", "/api/agent-accounts/capacity/preview", body, 200, nil)
	for _, s := range stalled {
		select {
		case <-s.done:
			t.Fatal("stalled body finished before its deadline expired")
		default:
		}
		s.expire(context.DeadlineExceeded)
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Fatal("a stalled body outlived the preview deadline")
		}
		if s.rec.Code != http.StatusRequestTimeout {
			t.Fatal("stalled body answered", s.rec.Code)
		}
	}
	if !guard.acquire() {
		t.Fatal("a slot leaked")
	}
	guard.release()
}
