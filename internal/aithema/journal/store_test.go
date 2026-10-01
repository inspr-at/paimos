// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

type world struct {
	t        *testing.T
	database *dbtest.DB
	store    *Store
	config   SessionConfig
	claims   tokens.Claims
	keys     *tokens.KeySet
}

func TestHTTPRechecksTokenExpiryAfterSessionLock(t *testing.T) {
	f := newWorld(t, generous())
	var clock atomic.Int64
	clock.Store(f.claims.IssuedAt)
	keys, err := tokens.New(t.Context(), &tokens.MemoryStore{TenantID: f.claims.TenantID}, bytes.Repeat([]byte{8}, 32), tokens.Config{
		Issuer: f.claims.Issuer, Audience: f.claims.Audience,
		Clock: func() time.Time { return time.Unix(clock.Load(), 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := keys.MintDelegated(t.Context(), f.claims)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.database.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), `SELECT sid FROM aithema_sessions WHERE sid=$1 FOR UPDATE`, f.claims.SessionID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Module{Store: f.store, Keys: keys}).Mount(mux)
	req := httptest.NewRequest("POST", "/api/aithema/journal/sessions/"+f.claims.SessionID+"/records", bytes.NewReader(marshal(f.record("turn"))))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); mux.ServeHTTP(rec, req) }()
	// Observe the actual database lock wait before advancing the signer's clock.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := f.database.Admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM aithema_sessions%FOR UPDATE%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("request did not reach session lock")
		case <-done:
			t.Fatal("request escaped session lock")
		case <-tick.C:
		}
	}
	clock.Store(f.claims.ExpiresAt + tokens.MaxSkewSeconds + 1)
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish after lock released")
	}
	if rec.Code != 401 {
		t.Fatalf("expired credential used after lock wait: %d", rec.Code)
	}
	cursor := f.success("journal", "cursor", "GET", nil, nil)
	if number(cursor["seq"]) != 0 {
		t.Fatal("expired request appended a journal record")
	}
}

func TestLockAuthoritySharesTheLifecycleFence(t *testing.T) {
	f := newWorld(t, generous())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := db.InTenant(t.Context(), f.database.App, f.claims.TenantID, func(tx pgx.Tx) error {
		state, err := f.store.LockAuthority(t.Context(), tx, f.claims.TenantID, f.claims.SessionID)
		if err != nil {
			return err
		}
		if state.PluginPrincipal != f.claims.Subject || state.Generation != f.claims.Generation || state.Epoch != f.claims.AuthEpoch {
			t.Fatal("adapter authority lost host binding")
		}
		_, err = f.store.Takeover(ctx, f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, f.claims.Generation)
		if err == nil {
			t.Fatal("takeover bypassed the caller-owned authority lock")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.store.Takeover(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, f.claims.Generation)
	if err != nil || state.Generation != f.claims.Generation+1 {
		t.Fatal("takeover failed after transaction released authority")
	}
}

func newWorld(t *testing.T, caps Caps) *world {
	t.Helper()
	d := dbtest.Open(t)
	var tenantID string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('journal-owner','journal-owner') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(d.App)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 8, 15, 0, 0, time.UTC)
	st.clock = func() time.Time { return now }
	authz := fixture(t, "valid/authz.record.json")
	authz["tid"] = tenantID
	authz["pid"] = "project-1"
	config := SessionConfig{TenantID: tenantID, Authorization: marshal(authz), PluginPrincipal: "plugin-aithema", Generation: 2, HostMode: "review", Currency: "EUR", Evidence: true, Caps: caps, LocalLanes: []string{"reaction"}}
	if err := st.CreateSession(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	keys, err := tokens.New(t.Context(), &tokens.MemoryStore{TenantID: tenantID}, bytes.Repeat([]byte{7}, 32), tokens.Config{Issuer: "https://host.example", Audience: "https://host.example", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	c := tokens.Claims{Issuer: "https://host.example", Audience: "https://host.example", Subject: config.PluginPrincipal, Actor: &tokens.Actor{Subject: text(object(array(authz["participants"])[0])["participant_ref"])}, TenantID: tenantID, ProjectID: "project-1", SessionID: text(authz["sid"]), Generation: 2, AuthEpoch: number(authz["epoch"]), IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 900, Capabilities: []string{"aithema.journal.read", "aithema.journal.write", "aithema.authority.read", "aithema.ledger"}}
	return &world{t: t, database: d, store: st, config: config, claims: c, keys: keys}
}
func generous() Caps { return Caps{Session: 10000, PrincipalDay: 100000, TenantDay: 1000000} }
func (f *world) request(area, action, method string, raw []byte, q url.Values) (Result, error) {
	return f.store.Request(f.t.Context(), f.claims, area, action, method, raw, q)
}
func (f *world) success(area, action, method string, raw []byte, q url.Values) map[string]any {
	f.t.Helper()
	result, err := f.request(area, action, method, raw, q)
	if err != nil || result.Status != 200 {
		f.t.Fatalf("%s/%s: status %d err %v", area, action, result.Status, err)
	}
	doc, err := decode(result.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return doc
}
func errorIs(t *testing.T, err error, status int, code string) {
	t.Helper()
	var got *Fault
	if !errors.As(err, &got) || got.Status != status || got.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}
func (f *world) record(name string) map[string]any {
	f.t.Helper()
	doc := fixture(f.t, "valid/record."+name+".json")
	delete(doc, "seq")
	doc["client_event_id"] = newID()
	doc["sid"] = f.claims.SessionID
	if writer := object(doc["writer"]); writer["kind"] == "worker" {
		writer["generation"] = f.claims.Generation
	}
	return doc
}
func (f *world) snapshot(prev, consumed int64) map[string]any {
	doc := fixture(f.t, "valid/snapshot.persisted.json")
	delete(doc, "seq")
	doc["client_event_id"] = newID()
	doc["working_rev"] = prev + 1
	doc["expected_prev_rev"] = prev
	doc["consumed_seq"] = consumed
	doc["worker_generation"] = f.claims.Generation
	return doc
}
func (f *world) admitBody(n, max int64) []byte {
	return budget("admit_request", map[string]any{"attempt_id": fmt.Sprintf("%s:%d:spec:%d", f.claims.SessionID, f.claims.Generation, n), "sid": f.claims.SessionID, "worker_generation": f.claims.Generation, "auth_epoch": f.claims.AuthEpoch, "lane": "spec", "max_micro": max, "currency": "EUR"})
}
func (f *world) hold(n, max int64) string {
	return text(object(f.success("ledger", "admit", "POST", f.admitBody(n, max), nil)["body"])["hold_id"])
}
func (f *world) claimBody(id string) []byte {
	return budget("claim_request", map[string]any{"hold_id": id, "request_sha256": strings.Repeat("a", 64), "worker_generation": f.claims.Generation, "auth_epoch": f.claims.AuthEpoch})
}
func (f *world) claim(id string) string {
	return text(object(f.success("ledger", "claim", "POST", f.claimBody(id), nil)["body"])["claim_id"])
}
func (f *world) recoverBody(id string) []byte {
	return budget("recover_request", map[string]any{"hold_id": id, "worker_generation": f.claims.Generation, "auth_epoch": f.claims.AuthEpoch})
}
func (f *world) recover(id string) map[string]any {
	return object(f.success("ledger", "recover", "POST", f.recoverBody(id), nil)["body"])
}

func TestJournalCASReplayRestartAndClosureHydration(t *testing.T) {
	f := newWorld(t, generous())
	source, turn, design := f.record("source"), f.record("turn"), f.record("design-input")
	original := []byte(" \n" + string(marshal(source)) + "\n")
	got := f.success("journal", "records", "POST", original, url.Values{"format": {"stored"}})
	if got["bytes"] != string(original) || number(object(got["document"])["seq"]) != 1 {
		t.Fatal("ack lost original source bytes or sequence")
	}
	turnAck := f.success("journal", "records", "POST", marshal(turn), nil)
	designAck := f.success("journal", "records", "POST", marshal(design), nil)
	if number(turnAck["seq"]) != 2 || number(designAck["seq"]) != 3 {
		t.Fatal("sequence order")
	}
	snap := f.snapshot(0, 3)
	spec := object(snap["spec"])
	// This closure includes an earlier cited turn, source segments, provenance
	// summary leaves and a preview's immutable input even after consumed_seq.
	for _, value := range array(spec["items"]) {
		item := object(value)
		item["citations"] = []any{map[string]any{"record_seq": 1, "locator": "seg:p1"}, map[string]any{"record_seq": 2, "locator": "turn:0"}}
		object(item["provenance"])["derived_from"] = []any{1, 2}
	}
	spec["screens"] = []any{map[string]any{"screen_ref": "S-export", "design_input_seq": 3}}
	ack := f.success("journal", "snapshots", "POST", marshal(snap), nil)
	if number(ack["seq"]) != 4 || number(ack["working_rev"]) != 1 {
		t.Fatal("snapshot did not commit")
	}
	tail := f.record("turn")
	tailAck := f.success("journal", "records", "POST", marshal(tail), nil)
	if number(tailAck["seq"]) != 5 {
		t.Fatal("tail seq")
	}
	// Lost snapshot acknowledgement retries return the original revision despite
	// current CAS state and later records; byte differences are conflicts.
	retry := f.success("journal", "snapshots", "POST", marshal(snap), nil)
	if !bytes.Equal(marshal(ack), marshal(retry)) {
		t.Fatal("snapshot replay changed")
	}
	_, err := f.request("journal", "snapshots", "POST", append(marshal(snap), ' '), nil)
	errorIs(t, err, 409, "idempotency_conflict")
	_, err = f.request("journal", "snapshots", "POST", marshal(f.snapshot(0, 5)), nil)
	errorIs(t, err, 409, "snapshot_conflict")
	replica, err := NewStore(f.database.App)
	if err != nil {
		t.Fatal(err)
	}
	replica.clock = f.store.clock
	f.store = replica
	if _, err := f.store.Takeover(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, 2); err != nil {
		t.Fatal(err)
	}
	old := f.claims
	f.claims.Generation = 3
	// Existing event retries retain their original writer generation.
	replay := f.success("journal", "records", "POST", original, url.Values{"format": {"stored"}})
	if replay["bytes"] != string(original) {
		t.Fatal("restart bytes changed")
	}
	stale, err := replica.Request(t.Context(), old, "journal", "records", "POST", original, nil)
	_ = stale
	errorIs(t, err, 409, "fenced_generation")
	cursor := f.success("journal", "cursor", "GET", nil, url.Values{"format": {"stored"}})
	if number(cursor["seq"]) != 5 || number(cursor["working_rev"]) != 1 || number(object(object(cursor["snapshot"])["document"])["seq"]) != 4 {
		t.Fatal("cursor lost snapshot")
	}
	result, err := f.request("journal", "records", "GET", nil, url.Values{"ids": {"1,2,3"}, "after": {"4"}, "format": {"stored"}})
	if err != nil {
		t.Fatal(err)
	}
	var closure []Record
	if err := json.Unmarshal(result.Body, &closure); err != nil {
		t.Fatal(err)
	}
	if len(closure) != 3 || closure[0].Bytes != string(original) || closure[1].Bytes != string(marshal(turn)) || closure[2].Bytes != string(marshal(design)) {
		t.Fatal("hydration lost citation/preview bytes")
	}
	// Read-only hydration intentionally survives a generation change.
	if _, err := replica.Request(t.Context(), old, "journal", "cursor", "GET", nil, nil); err != nil {
		t.Fatal(err)
	}
	tailResult, err := f.request("journal", "records", "GET", nil, url.Values{"after": {"4"}})
	if err != nil {
		t.Fatal(err)
	}
	var records []json.RawMessage
	if err := json.Unmarshal(tailResult.Body, &records); err != nil || len(records) != 1 {
		t.Fatal("replay tail incorrect")
	}
}

func TestLedgerCrashBoundariesAndRepeatedRecovery(t *testing.T) {
	f := newWorld(t, generous())
	for _, scenario := range []struct {
		name          string
		claim, settle bool
		reason        string
		charged       int64
	}{{"crash-before-claim", false, false, "void", 0}, {"crash-after-claim", true, false, "unknown", 100}, {"lost-settlement-response", true, true, "settled", 27}} {
		t.Run(scenario.name, func(t *testing.T) {
			id := f.hold(int64(len(scenario.name)), 100)
			var claim string
			if scenario.claim {
				claim = f.claim(id)
			}
			if scenario.settle {
				raw := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "settled", "actual_micro": 27})
				first := f.success("ledger", "settle", "POST", raw, nil)
				retry := f.success("ledger", "settle", "POST", raw, nil)
				if !bytes.Equal(marshal(first), marshal(retry)) {
					t.Fatal("settle replay changed")
				}
				_, err := f.request("ledger", "settle", "POST", append(raw, ' '), nil)
				errorIs(t, err, 409, "idempotency_conflict")
			}
			// Restart uses only persistent ledger state, with no journal hold record.
			restarted, err := NewStore(f.database.App)
			if err != nil {
				t.Fatal(err)
			}
			restarted.clock = f.store.clock
			f.store = restarted
			open := object(f.success("ledger", "holds", "GET", nil, url.Values{"state": {"open"}})["body"])
			if len(array(open["holds"])) != map[bool]int{true: 0, false: 1}[scenario.settle] {
				t.Fatal("open enumeration wrong")
			}
			got := f.recover(id)
			again := f.recover(id)
			if got["closed_reason"] != scenario.reason || number(got["charged_micro"]) != scenario.charged || !bytes.Equal(marshal(got), marshal(again)) {
				t.Fatal("recovery outcome changed")
			}
			_, err = f.request("ledger", "claim", "POST", f.claimBody(id), nil)
			errorIs(t, err, 409, "hold_closed")
		})
	}
	id := f.hold(99, 100)
	claim := f.claim(id)
	_, err := f.request("ledger", "claim", "POST", f.claimBody(id), nil)
	errorIs(t, err, 409, "already_claimed")
	raw := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "unknown"})
	got := object(f.success("ledger", "settle", "POST", raw, nil)["body"])
	if number(got["charged_micro"]) != 100 {
		t.Fatal("unknown not charged at maximum")
	}
	bad := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "unknown", "actual_micro": 10})
	_, err = f.request("ledger", "settle", "POST", bad, nil)
	errorIs(t, err, 400, "invalid_request")
}

func TestConcurrentCASClaimsRecoveryAndTakeover(t *testing.T) {
	f := newWorld(t, generous())
	const workers = 8
	run := func(fn func(int) (Result, error)) ([]Result, []error) {
		results := make([]Result, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range workers {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; results[i], errs[i] = fn(i) }()
		}
		close(start)
		wg.Wait()
		return results, errs
	}
	snapshots := make([][]byte, workers)
	for i := range snapshots {
		snapshots[i] = marshal(f.snapshot(0, 0))
	}
	_, errs := run(func(i int) (Result, error) { return f.request("journal", "snapshots", "POST", snapshots[i], nil) })
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else {
			errorIs(t, err, 409, "snapshot_conflict")
		}
	}
	if wins != 1 {
		t.Fatalf("CAS winners %d", wins)
	}
	id := f.hold(1, 100)
	_, errs = run(func(i int) (Result, error) { return f.request("ledger", "claim", "POST", f.claimBody(id), nil) })
	wins = 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else {
			errorIs(t, err, 409, "already_claimed")
		}
	}
	if wins != 1 {
		t.Fatalf("claim winners %d", wins)
	}
	results, errs := run(func(i int) (Result, error) { return f.request("ledger", "recover", "POST", f.recoverBody(id), nil) })
	for i, err := range errs {
		if err != nil || !bytes.Equal(results[i].Body, results[0].Body) {
			t.Fatalf("recovery race %v", err)
		}
	}
	second := f.hold(2, 100)
	results, errs = run(func(i int) (Result, error) {
		if i%2 == 0 {
			return f.request("ledger", "claim", "POST", f.claimBody(second), nil)
		}
		return f.request("ledger", "recover", "POST", f.recoverBody(second), nil)
	})
	claimed := false
	for i, err := range errs {
		if i%2 == 0 && err == nil {
			claimed = true
		}
		if err != nil {
			if publicError(err).Code != "already_claimed" && publicError(err).Code != "hold_closed" {
				t.Fatal(err)
			}
		}
	}
	closed := f.recover(second)
	if claimed != (closed["closed_reason"] == "unknown") {
		t.Fatal("claim/recover transaction boundary violated")
	}
	_, errs = run(func(i int) (Result, error) {
		_, err := f.store.Takeover(context.Background(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, 2)
		return Result{}, err
	})
	wins = 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else {
			errorIs(t, err, 409, "fenced_generation")
		}
	}
	if wins != 1 {
		t.Fatalf("takeover winners %d", wins)
	}
}

func TestAtomicAggregateCapsReplayAndKeysetDrain(t *testing.T) {
	f := newWorld(t, Caps{Session: 500, PrincipalDay: 500, TenantDay: 500})
	// Two sessions share principal/day and tenant/day caps and one policy lock.
	otherAuth, _ := decode(f.config.Authorization)
	otherAuth["sid"] = newID()
	otherConfig := f.config
	otherConfig.Authorization = marshal(otherAuth)
	if err := f.store.CreateSession(t.Context(), otherConfig); err != nil {
		t.Fatal(err)
	}
	claims := []tokens.Claims{f.claims, f.claims}
	claims[1].SessionID = text(otherAuth["sid"])
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := []struct {
		c  tokens.Claims
		id string
	}{}
	denied := 0
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := claims[i%2]
			raw := budget("admit_request", map[string]any{"attempt_id": fmt.Sprintf("%s:2:spec:%d", c.SessionID, i), "sid": c.SessionID, "worker_generation": 2, "auth_epoch": c.AuthEpoch, "lane": "spec", "max_micro": 100, "currency": "EUR"})
			result, err := f.store.Request(context.Background(), c, "ledger", "admit", "POST", raw, nil)
			if err != nil {
				t.Error(err)
				return
			}
			doc, _ := decode(result.Body)
			mu.Lock()
			defer mu.Unlock()
			if result.Status == 200 {
				accepted = append(accepted, struct {
					c  tokens.Claims
					id string
				}{c, text(object(doc["body"])["hold_id"])})
			} else if result.Status == 402 {
				denied++
			} else {
				t.Errorf("status %d", result.Status)
			}
			replay, err := f.store.Request(context.Background(), c, "ledger", "admit", "POST", raw, nil)
			if err != nil || replay.Status != result.Status || !bytes.Equal(replay.Body, result.Body) {
				t.Error("admit replay changed")
			}
		}()
	}
	wg.Wait()
	if len(accepted) != 5 || denied != 7 {
		t.Fatalf("aggregate overbooking admitted=%d denied=%d", len(accepted), denied)
	}
	for _, h := range accepted {
		_, err := f.store.Request(t.Context(), h.c, "ledger", "recover", "POST", budget("recover_request", map[string]any{"hold_id": h.id, "worker_generation": 2, "auth_epoch": h.c.AuthEpoch}), nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Filling three pages then recovering earlier pages must not skip holds.
	for i := range 5 {
		f.hold(int64(100+i), 10)
	}
	var cursor string
	seen := []string{}
	for {
		q := url.Values{"state": {"open"}, "limit": {"2"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		body := object(f.success("ledger", "holds", "GET", nil, q)["body"])
		for _, value := range array(body["holds"]) {
			id := text(object(value)["hold_id"])
			seen = append(seen, id)
			f.recover(id)
		}
		cursor = text(body["next_cursor"])
		if cursor == "" {
			break
		}
	}
	sort.Strings(seen)
	if len(seen) != 5 {
		t.Fatalf("keyset skipped holds %d", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] == seen[i-1] {
			t.Fatal("duplicate page hold")
		}
	}
}

func TestHTTPRouteMatrixAndStableStatuses(t *testing.T) {
	f := newWorld(t, generous())
	mod := &Module{Store: f.store, Keys: f.keys}
	core, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{4}, 32)}, f.database.App)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{Pool: f.database.App, Modules: []httpapi.Module{mod}, Middleware: []func(http.Handler) http.Handler{core.Middleware}}).Handler()
	mint := func(c tokens.Claims) string {
		t.Helper()
		token, err := f.keys.MintDelegated(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	send := func(method, area, action string, c *tokens.Claims, raw []byte, query string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/aithema/"+area+"/sessions/"+f.claims.SessionID+"/"+action+query, bytes.NewReader(raw))
		if c != nil {
			req.Header.Set("Authorization", "Bearer "+mint(*c))
		}
		req.AddCookie(&http.Cookie{Name: "aeon_session", Value: "cookie-does-not-grant-journal"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Set-Cookie") != "" {
			t.Fatal("unsafe caching or cookie mutation")
		}
		return rec
	}
	for _, route := range []struct{ area, action, method, cap string }{{"journal", "records", "POST", "aithema.journal.write"}, {"journal", "snapshots", "POST", "aithema.journal.write"}, {"journal", "op.result", "POST", "aithema.journal.write"}, {"journal", "records", "GET", "aithema.journal.read"}, {"journal", "cursor", "GET", "aithema.journal.read"}, {"journal", "authority", "GET", "aithema.authority.read"}, {"ledger", "admit", "POST", "aithema.ledger"}, {"ledger", "claim", "POST", "aithema.ledger"}, {"ledger", "settle", "POST", "aithema.ledger"}, {"ledger", "recover", "POST", "aithema.ledger"}, {"ledger", "holds", "GET", "aithema.ledger"}} {
		t.Run(route.area+"/"+route.action+"/"+route.method, func(t *testing.T) {
			if got := send(route.method, route.area, route.action, nil, []byte(`{}`), ""); got.Code != 401 {
				t.Fatalf("anonymous status %d", got.Code)
			}
			c := f.claims
			c.Capabilities = []string{"intake.read"}
			if got := send(route.method, route.area, route.action, &c, []byte(`{}`), ""); got.Code != 403 {
				t.Fatalf("missing exact capability status %d", got.Code)
			}
			c = f.claims
			c.Generation = 1
			c.AuthEpoch--
			got := send(route.method, route.area, route.action, &c, []byte(`{}`), "")
			want := 409
			if route.action == "authority" {
				want = 200
			}
			if got.Code != want {
				t.Fatalf("stale status %d want %d", got.Code, want)
			}
		})
	}
	token := f.claims
	turn := f.record("turn")
	host := f.record("authz-epoch")
	future := f.record("turn")
	future["min_reader"] = 2
	future["minor"] = 2
	for _, tc := range []struct {
		name, area, action, method, query string
		raw                               []byte
		status                            int
		code                              string
	}{
		{"invalid-json", "journal", "records", "POST", "", []byte(`{"x":`), 400, "invalid_request"},
		{"host-impersonation", "journal", "records", "POST", "", marshal(host), 403, "forbidden"},
		{"contract-too-new", "journal", "records", "POST", "", marshal(future), 422, "contract_too_new"},
		{"body-limit", "journal", "records", "POST", "", bytes.Repeat([]byte("x"), (1<<20)+1), 413, "too_large"},
		{"unknown-hold", "ledger", "claim", "POST", "", f.claimBody(newID()), 404, "not_found"},
		{"bad-query", "ledger", "holds", "GET", "?state=closed", nil, 400, "invalid_request"},
		{"foreign-cursor", "ledger", "holds", "GET", "?state=open&cursor=foreign:1", nil, 400, "invalid_request"},
		{"empty-ids", "journal", "records", "GET", "?ids=", nil, 400, "invalid_request"},
		{"invalid-format", "journal", "cursor", "GET", "?format=unknown", nil, 400, "invalid_request"},
		{"success", "journal", "records", "POST", "", marshal(turn), 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(tc.method, tc.area, tc.action, &token, tc.raw, tc.query)
			if rec.Code != tc.status {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if tc.code != "" {
				body, err := decode(rec.Body.Bytes())
				if err != nil || body["code"] != tc.code || body["error"] == nil {
					t.Fatalf("missing stable error %s", tc.code)
				}
			}
		})
	}
	authority := send("GET", "journal", "authority", &token, nil, "")
	body, err := decode(authority.Body.Bytes())
	if err != nil || body["issued_at"] != "2026-09-30T08:15:00.000000Z" {
		t.Fatal("authority lacks current host timestamp")
	}
	c := token
	c.ProjectID = "foreign-project"
	if got := send("GET", "journal", "authority", &c, nil, ""); got.Code != 200 {
		t.Fatal("authority incorrectly checks project")
	}
	if got := send("GET", "journal", "cursor", &c, nil, ""); got.Code != 403 {
		t.Fatal("foreign project read")
	}
	// A nil signer remains fail-closed in HTTP-only development.
	mux := http.NewServeMux()
	(&Module{Store: f.store}).Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/aithema/journal/sessions/"+f.claims.SessionID+"/authority", nil))
	if rec.Code != 503 {
		t.Fatal("nil signer not unavailable")
	}
}

func TestGenerationEpochTombstonesAndRLS(t *testing.T) {
	f := newWorld(t, generous())
	unclaimed := f.hold(1, 100)
	claimed := f.hold(2, 100)
	claim := f.claim(claimed)
	prior := f.claims
	if _, err := f.store.Takeover(t.Context(), prior.TenantID, prior.ProjectID, prior.SessionID, 2); err != nil {
		t.Fatal(err)
	}
	f.claims.Generation = 3
	_, err := f.request("ledger", "claim", "POST", f.claimBody(unclaimed), nil)
	errorIs(t, err, 409, "fenced_generation")
	_, err = f.store.Request(t.Context(), prior, "ledger", "recover", "POST", f.recoverBody(claimed), nil)
	errorIs(t, err, 409, "fenced_generation")
	if got := f.recover(unclaimed); got["closed_reason"] != "void" {
		t.Fatal("new generation cannot recover old unclaimed hold")
	}
	// An older already committed dispatch is charged at maximum on settlement.
	raw := budget("settle_request", map[string]any{"claim_id": claim, "outcome": "settled", "actual_micro": 1})
	if got := object(f.success("ledger", "settle", "POST", raw, nil)["body"]); got["closed_reason"] != "unknown" || number(got["charged_micro"]) != 100 {
		t.Fatal("takeover discounted an old claim")
	}
	if err := f.store.Revoke(t.Context(), prior.TenantID, prior.ProjectID, prior.SessionID, "purge"); err != nil {
		t.Fatal(err)
	}
	authority := f.success("journal", "authority", "GET", nil, nil)
	if authority["tombstone"] != true || number(authority["auth_epoch"]) != prior.AuthEpoch+1 {
		t.Fatal("tombstone projection missing")
	}
	_, err = f.request("journal", "records", "POST", marshal(f.record("turn")), nil)
	errorIs(t, err, 409, "revoked")
	_, err = f.store.Takeover(t.Context(), prior.TenantID, prior.ProjectID, prior.SessionID, 3)
	errorIs(t, err, 409, "revoked")
	if err := f.store.Revoke(t.Context(), prior.TenantID, prior.ProjectID, prior.SessionID, "purge"); err != nil {
		t.Fatal(err)
	}
	current, err := f.store.Current(t.Context(), prior.TenantID, prior.ProjectID, prior.SessionID)
	if err != nil || current.Generation != 3 || !current.Tombstone {
		t.Fatal("P04 generation seam")
	}
	var other string
	if err := f.database.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('other','other') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"aithema_sessions", "aithema_journal_records", "aithema_budget_policy", "aithema_budget_holds", "aithema_budget_claims"} {
		t.Run(table, func(t *testing.T) {
			err := db.InTenant(t.Context(), f.database.App, other, func(tx pgx.Tx) error {
				var count int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					t.Fatal("foreign tenant rows visible")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			err = db.InTenant(t.Context(), f.database.App, other, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO `+table+` OVERRIDING SYSTEM VALUE SELECT * FROM `+table+` WHERE tenant_id=$1`, prior.TenantID)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			// A direct insert with a foreign tenant must hit WITH CHECK, including
			// relation ownership constraints. Clone only this tenant's existing row.
			err = db.InTenant(t.Context(), f.database.App, prior.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE `+table+` SET tenant_id=$1 WHERE tenant_id=$2`, other, prior.TenantID)
				return err
			})
			if err == nil {
				t.Fatal("cross-tenant mutation allowed")
			}
		})
	}
	// Host-owned controls were atomically journaled before their projection.
	var kinds []string
	if err := db.InTenant(t.Context(), f.database.App, prior.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT kind FROM aithema_journal_records ORDER BY seq`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind string
			if err := rows.Scan(&kind); err != nil {
				return err
			}
			kinds = append(kinds, kind)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "authz.epoch,session.control" {
		t.Fatal("control replay duplicated journal or missed write-ahead")
	}
}

func TestCapsDenialIdempotencyEvidenceAndLocalPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caps   Caps
		reason string
	}{{"session", Caps{10, 100, 100}, "session_cap"}, {"principal", Caps{100, 10, 100}, "principal_day_cap"}, {"tenant", Caps{100, 100, 10}, "tenant_day_cap"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorld(t, tc.caps)
			result, err := f.request("ledger", "admit", "POST", f.admitBody(1, 20), nil)
			if err != nil || result.Status != 402 {
				t.Fatalf("denial status %d %v", result.Status, err)
			}
			body, _ := decode(result.Body)
			if text(object(object(body["document"])["body"])["denied"]) != tc.reason {
				t.Fatal("wrong scope refusal")
			}
			f.store.clock = func() time.Time { return time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC) }
			again, err := f.request("ledger", "admit", "POST", f.admitBody(1, 20), nil)
			if err != nil || !bytes.Equal(result.Body, again.Body) {
				t.Fatal("denial changed across day")
			}
			_, err = f.request("ledger", "admit", "POST", f.admitBody(1, 21), nil)
			errorIs(t, err, 409, "idempotency_conflict")
		})
	}
	f := newWorld(t, generous())
	body := map[string]any{"attempt_id": f.claims.SessionID + ":2:reaction:1", "sid": f.claims.SessionID, "worker_generation": 2, "auth_epoch": f.claims.AuthEpoch, "lane": "reaction", "max_micro": 0, "currency": "EUR", "lane_kind": "operator_local"}
	id := text(object(f.success("ledger", "admit", "POST", budget("admit_request", body), nil)["body"])["hold_id"])
	f.claim(id)
	got := f.recover(id)
	if number(got["charged_micro"]) != 0 || got["lane_kind"] != "operator_local" {
		t.Fatal("local unknown zero lost policy")
	}
	body["attempt_id"] = f.claims.SessionID + ":2:spec:2"
	body["lane"] = "spec"
	_, err := f.request("ledger", "admit", "POST", budget("admit_request", body), nil)
	errorIs(t, err, 403, "forbidden")
	err = db.InTenant(t.Context(), f.database.App, f.claims.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE aithema_sessions SET evidence=false WHERE tenant_id=$1`, f.claims.TenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.request("ledger", "admit", "POST", f.admitBody(5, 10), nil)
	if err != nil || result.Status != 402 {
		t.Fatal("no evidence admitted paid work")
	}
	doc, _ := decode(result.Body)
	if object(object(doc["document"])["body"])["denied"] != "no_evidence" {
		t.Fatal("evidence denial code")
	}
}

func TestCursorExhaustionRollsBackTombstone(t *testing.T) {
	f := newWorld(t, generous())
	err := db.InTenant(t.Context(), f.database.App, f.claims.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE aithema_sessions SET seq=$2 WHERE tenant_id=$1`, f.claims.TenantID, tokens.MaxSafeInteger-1)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "purge")
	errorIs(t, err, 409, "sequence_exhausted")
	state, err := f.store.Current(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID)
	if err != nil || state.Tombstone || state.Epoch != f.claims.AuthEpoch {
		t.Fatal("projection changed despite failed write-ahead")
	}
	if err := db.InTenant(t.Context(), f.database.App, f.claims.TenantID, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM aithema_journal_records`).Scan(&count)
		if count != 0 {
			t.Fatal("failed control left partial journal")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
