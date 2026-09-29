// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/inspr-at/paimos/internal/version"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const origin = "https://pairing.test"

func nixGuideFixture() *config.PairingNixGuide {
	return &config.PairingNixGuide{
		ModuleURL: "https://example.test/instance/module.nix", ServiceOption: "services.aeon.enable",
		Platforms: []string{"darwin"}, ServiceNote: "This module needs a paired-service update before this computer can connect.",
	}
}

type fixture struct {
	t        *testing.T
	db       *dbtest.DB
	h        http.Handler
	tenantID string
	person   string
	cookie   *http.Cookie
	profiles map[string]string
}
type proposal struct {
	id, device, runtime, lifecycle string
	request                        map[string]any
	review                         agentpairing.View
	code                           string
}

func nonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func uuid(t *testing.T, d *dbtest.DB) string {
	t.Helper()
	var id string
	if err := d.Admin.QueryRow(t.Context(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := dbtest.Open(t)
	id, err := tenantbootstrap.Create(t.Context(), d.App, "pairtest", "Pairing test")
	if err != nil {
		t.Fatal(err)
	}
	am, err := auth.New(auth.Config{Env: "dev", PublicURL: origin, SessionKey: []byte(nonce()), BootstrapTenantSlug: "pairtest", BootstrapAdminEmail: "pairing@example.test"}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: d.App, Modules: []httpapi.Module{am, agentpairing.New(d.App, origin, "pairtest"), agentaccounts.New(d.App), nodes.New(d.App, nil), modelregistry.New(d.App), harness.New(d.App), workorders.New(d.App), agentruns.New(d.App, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, r agentruns.Run, _ agentruns.Telemetry) error {
		return agentaccounts.Settle(ctx, tx, p, r.ID)
	})}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	f := &fixture{t: t, db: d, h: api.Handler(), tenantID: id, profiles: map[string]string{}}
	login := f.call("POST", "/api/auth/dev-login", map[string]string{"email": "pairing@example.test"}, false, "", 200)
	cookies := login.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie")
	}
	f.cookie = cookies[0]
	var me struct {
		Principal struct {
			ID string `json:"id"`
		} `json:"principal"`
	}
	decodeResult(t, login, &me)
	f.person = me.Principal.ID
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
		for _, h := range []string{"codex", "cursor", "claude", "grok"} {
			var profile string
			err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'1',$2,'openai','test-model','low','fast') RETURNING id::text`, id, h).Scan(&profile)
			if err != nil {
				return err
			}
			f.profiles[h] = profile
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) request(method, path string, body any, person bool, key string) *http.Request {
	if person && method == "POST" && strings.HasSuffix(path, "/disconnect") && strings.Contains(path, "/computers/") {
		b, _ := json.Marshal(body)
		var obj map[string]any
		_ = json.Unmarshal(b, &obj)
		if _, ok := obj["expected_revision"]; !ok {
			parts := strings.Split(strings.Trim(path, "/"), "/")
			var rev int64
			_ = f.db.Admin.QueryRow(f.t.Context(), `SELECT revision FROM agent_pairing_computers WHERE id=$1`, parts[3]).Scan(&rev)
			obj["expected_revision"] = rev
		}
		body = obj
	}
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	if person {
		r.AddCookie(f.cookie)
		r.Header.Set("Origin", origin)
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	return r
}
func (f *fixture) call(method, path string, body any, person bool, key string, status int) *httptest.ResponseRecorder {
	f.t.Helper()
	r := f.request(method, path, body, person, key)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != status {
		f.t.Fatalf("%s %s status %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}
func decodeResult(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) propose(harnesses ...string) *proposal {
	return f.proposePlatform("darwin", "arm64", harnesses...)
}
func (f *fixture) proposePlatform(platform, arch string, harnesses ...string) *proposal {
	f.t.Helper()
	p := &proposal{id: uuid(f.t, f.db), device: nonce(), runtime: nonce(), lifecycle: nonce()}
	accounts := []map[string]string{}
	for _, h := range harnesses {
		accounts = append(accounts, map[string]string{"account_key": h + "-local", "harness": h, "label": h + " personal test", "model_profile_id": f.profiles[h]})
	}
	p.request = map[string]any{"request_id": p.id, "tenant_id": f.tenantID, "device_hash": hash(p.device), "runtime_hash": hash(p.runtime), "lifecycle_hash": hash(p.lifecycle), "computer_name": "Test workstation", "platform": platform, "arch": arch, "workspace_path": "/tmp/pairing-fixture", "capabilities": []string{"managed_runs"}, "accounts": accounts}
	f.submit(p)
	return p
}
func (f *fixture) submit(p *proposal) {
	f.t.Helper()
	w := f.call("POST", "/api/agent-pairing/device", p.request, false, "", 200)
	var out struct {
		Code string `json:"user_code"`
	}
	decodeResult(f.t, w, &out)
	p.code = out.Code
	decodeResult(f.t, f.call("POST", "/api/agent-pairing/lookup", map[string]string{"user_code": p.code}, true, "", 200), &p.review)
}
func (f *fixture) approve(p *proposal, mode string) agentpairing.View {
	f.t.Helper()
	keys := []string{}
	for _, a := range p.review.Requested {
		keys = append(keys, a.AccountKey)
	}
	var v agentpairing.View
	decodeResult(f.t, f.call("POST", "/api/agent-pairing/requests/"+p.id+"/approve", map[string]any{"request_digest": p.review.Digest, "verification": mode, "selected_account_keys": keys}, true, "", 200), &v)
	return v
}
func (f *fixture) redeem(p *proposal) agentpairing.View {
	f.t.Helper()
	var v agentpairing.View
	decodeResult(f.t, f.call("POST", "/api/agent-pairing/redeem", map[string]string{"tenant_id": f.tenantID, "request_id": p.id, "device_secret": p.device}, false, "", 200), &v)
	return v
}
func (f *fixture) probe(v agentpairing.View, e agentpairing.Enrollment, key string, status int) {
	f.t.Helper()
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/probe", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "available": true}, false, key, status)
}
func (f *fixture) reserve(v agentpairing.View, e agentpairing.Enrollment, key string, status int) []string {
	f.t.Helper()
	w := f.call("POST", "/api/agent-accounts/route", map[string]any{"run_id": *e.VerificationRunID, "daemon_id": *v.DaemonID, "account_ids": []string{e.AccountID}, "estimated_units": map[string]int{"requests": 1}}, false, key, status)
	if status != 200 {
		return nil
	}
	var route agentaccounts.RouteResult
	decodeResult(f.t, w, &route)
	ids := []string{}
	for _, r := range route.Reservations {
		ids = append(ids, r.ReservationID)
	}
	return ids
}
func (f *fixture) claim(v agentpairing.View, e agentpairing.Enrollment, key string, res []string, status int) {
	f.t.Helper()
	f.call("POST", "/api/runs/"+*e.VerificationRunID+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": res}, false, key, status)
}
func (f *fixture) telemetry(v agentpairing.View, e agentpairing.Enrollment, key string, status int) {
	f.t.Helper()
	r := f.request("POST", "/api/runs/"+*e.VerificationRunID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "completed", "turn_count_delta": 1}, false, key)
	r.Header.Set(agentruns.DaemonHeader, *v.DaemonID)
	r.Header.Set(agentruns.GenerationHeader, "test-generation")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != status {
		f.t.Fatalf("telemetry status %d want %d: %s", w.Code, status, w.Body.String())
	}
}

func TestPairingApprovalRedemptionIsolationAndOneShot(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	if p.review.State != "pending" || p.review.Verification.Allowance != 1 || p.review.Verification.MaxParallel != 1 || p.review.Verification.MaxDuration != 60 {
		t.Fatal("wrong preapproval terms")
	}
	if f.redeem(p).RuntimePrefix != "" {
		t.Fatal("unapproved grant")
	}
	f.call("POST", "/api/agent-pairing/redeem", map[string]string{"tenant_id": f.tenantID, "request_id": p.id, "device_secret": nonce()}, false, "", 404)
	// Creation retry returns the same code and immutable model binding.
	old := p.code
	f.submit(p)
	if p.code != old {
		t.Fatal("request replay changed code")
	}
	r := f.request("POST", "/api/agent-pairing/requests/"+p.id+"/approve", map[string]any{"request_digest": p.review.Digest, "verification": "connect_only", "selected_account_keys": []string{"claude-local"}}, true, "")
	r.Header.Del("Origin")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing origin approved")
	}
	approved := f.approve(p, "one_per_harness")
	if approved.SetupState != "approved" || approved.Connectivity != "unknown" || approved.RuntimePrefix != "" {
		t.Fatal("approval falsely reports connected or exposes runtime prefix")
	}
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	if v.State != "redeemed" || len(v.Enrollments) != 1 {
		t.Fatal("missing redemption bindings")
	}
	retry := f.redeem(p)
	if retry.RuntimePrefix != v.RuntimePrefix || *retry.Enrollments[0].VerificationRunID != *v.Enrollments[0].VerificationRunID {
		t.Fatal("redemption replay minted new authority")
	}
	f.call("POST", "/api/agent-pairing/requests/"+p.id+"/deny", map[string]any{}, false, key, 403)
	for _, path := range []string{"/api/agent-accounts", "/api/agent-accounts/" + v.Enrollments[0].AccountID + "/windows", "/api/agent-pairing/requests/" + p.id + "/approve", "/api/agent-keys"} {
		f.call("POST", path, map[string]any{}, false, key, 403)
	}
	f.call("GET", "/api/agent-pairing/self", nil, false, key, 200)
	var runs []agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/queued", nil, false, key, 200), &runs)
	if len(runs) != 1 || runs[0].Purpose != "pairing_verification" || runs[0].VerificationPolicy != "read_only" || runs[0].RepositoryMutationAllowed || runs[0].MaxDurationSeconds != 60 || runs[0].VerificationTask != agentpairing.VerificationTask {
		t.Fatal("verification launch contract missing")
	}
	for _, e := range v.Enrollments {
		f.probe(v, e, key, 200)
	}
	e := v.Enrollments[0]
	res := f.reserve(v, e, key, 200)
	f.claim(v, e, key, res, 200)
	// Identical claim retries observe the same starting run, never another job.
	f.claim(v, e, key, res, 200)
	f.telemetry(v, e, key, 200)
	var held, used int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT reserved,used FROM account_allowance_windows WHERE account_id=$1`, e.AccountID).Scan(&held, &used); err != nil {
		t.Fatal(err)
	}
	if held != 0 || used != 1 {
		t.Fatalf("ledger held=%d used=%d", held, used)
	}
	f.approve(p, "one_per_harness")
	f.redeem(p)
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("retry created extra verification")
	}
	// Foreign tenant cannot lookup or redeem this request under its own RLS.
	foreign, err := tenantbootstrap.Create(t.Context(), f.db.App, "foreign", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/api/agent-pairing/redeem", map[string]string{"tenant_id": foreign, "request_id": p.id, "device_secret": p.device}, false, "", 404)
}

func TestPairingDrainSelectiveRevocationAndTombstone(t *testing.T) {
	f := newFixture(t)
	p, v := f.twoQualifiedEnrollments()
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	a, b := v.Enrollments[0], v.Enrollments[1]
	f.probe(v, a, key, 200)
	f.probe(v, b, key, 200)
	ra := f.reserve(v, a, key, 200)
	f.claim(v, a, key, ra, 200)
	f.reserve(v, b, key, 200)
	var draining agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/enrollments/"+a.AccountID+"/disconnect", map[string]string{"mode": "drain"}, true, "", 200), &draining)
	if len(draining.Enrollments[0].ActiveRunIDs) != 1 {
		t.Fatal("drain lost active work")
	}
	f.probe(v, a, key, 409)
	f.probe(v, b, key, 200)
	f.telemetry(v, a, key, 200)
	f.probe(v, a, key, 410)
	var revoked agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200), &revoked)
	if *revoked.ComputerState != "revoked" || revoked.Cleanup != "pending" || revoked.Processes != "unconfirmed" {
		t.Fatal("untruthful immediate revoke")
	}
	f.call("GET", "/api/runs/queued", nil, false, key, 401)
	var state string
	var reserved int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,w.reserved FROM agent_runs r JOIN account_allowance_windows w ON w.account_id=r.requested_account_id WHERE r.id=$1`, *b.VerificationRunID).Scan(&state, &reserved); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || reserved != 0 {
		t.Fatal("queued reservation not cancelled/released")
	}
	for i := 0; i < 2; i++ {
		var ack agentpairing.View
		decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "cleanup_confirmed_account_ids": []string{a.AccountID, b.AccountID}, "computer_cleanup_confirmed": true}, false, "", 200), &ack)
		if ack.Cleanup != "confirmed" || ack.RuntimePrefix != "" || ack.State != "revoked" {
			t.Fatal("tombstone granted authority or did not ack")
		}
	}
	if f.redeem(p).RuntimePrefix != "" {
		t.Fatal("stale setup revived revoked key")
	}
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "drain"}, true, "", 200)
}

func TestPairingImmediateRevokeRetainsActiveAccounting(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	e := v.Enrollments[0]
	f.probe(v, e, key, 200)
	res := f.reserve(v, e, key, 200)
	f.claim(v, e, key, res, 200)
	f.call("POST", "/api/agent-pairing/self/disconnect", map[string]string{"mode": "revoke_now"}, false, key, 200)
	f.telemetry(v, e, key, 401)
	var status string
	var held int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,w.reserved FROM agent_runs r JOIN account_allowance_windows w ON w.account_id=r.account_id WHERE r.id=$1`, *e.VerificationRunID).Scan(&status, &held); err != nil {
		t.Fatal(err)
	}
	if status != "starting" || held != 1 {
		t.Fatal("revocation falsely terminated active work or released uncertain usage")
	}
}

func TestPairingAddHarnessAndFreshRepair(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	q := &proposal{id: uuid(t, f.db), device: nonce(), runtime: p.runtime, lifecycle: p.lifecycle}
	q.request = map[string]any{}
	for k, x := range p.request {
		q.request[k] = x
	}
	q.request["request_id"] = q.id
	q.request["device_hash"] = hash(q.device)
	q.request["existing_computer_id"] = *v.ComputerID
	q.request["existing_lifecycle_secret"] = p.lifecycle
	q.request["accounts"] = []map[string]string{{"account_key": "claude-add", "harness": "claude", "label": "Second chosen account", "model_profile_id": f.profiles["claude"]}}
	f.submit(q)
	if q.review.ExistingComputerID != *v.ComputerID {
		t.Fatal("existing computer not identified before approval")
	}
	f.approve(q, "one_per_harness")
	added := f.redeem(q)
	if *added.ComputerID != *v.ComputerID || *added.PrincipalID != *v.PrincipalID || added.RuntimePrefix != v.RuntimePrefix || len(added.Enrollments) != 2 {
		t.Fatal("Add harness duplicated or rotated computer")
	}
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
	q.id = uuid(t, f.db)
	q.request["request_id"] = q.id
	q.request["device_hash"] = hash(nonce())
	f.call("POST", "/api/agent-pairing/device", q.request, false, "", 409)
	fresh := f.propose("codex")
	f.approve(fresh, "connect_only")
	paired := f.redeem(fresh)
	if *paired.PrincipalID == *v.PrincipalID || paired.RuntimePrefix == v.RuntimePrefix || paired.Enrollments[0].VerificationRunID != nil {
		t.Fatal("re-pair inherited old authority or verification")
	}
}

func TestPairingExpiryAttemptsAndRateLimit(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	for i := 0; i < 10; i++ {
		f.call("POST", "/api/agent-pairing/redeem", map[string]string{"tenant_id": f.tenantID, "request_id": p.id, "device_secret": nonce()}, false, "", 404)
	}
	if f.redeem(p).State != "expired" {
		t.Fatal("attempt bound not enforced")
	}
	q := f.propose("claude")
	f.approve(q, "one_per_harness")
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_requests SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, q.id); err != nil {
		t.Fatal(err)
	}
	expired := f.redeem(q)
	if expired.RuntimePrefix != "" || expired.State != "revoked" && expired.State != "expired" {
		t.Fatal("expired setup granted access")
	}
	var revoked bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM agent_keys WHERE principal_id=$1`, expired.PrincipalID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("expired provisional key stayed live")
	}
	for i := 0; i < 28; i++ {
		f.call("POST", "/api/agent-pairing/lookup", map[string]string{"user_code": "999-999-999"}, true, "", 404)
	}
	f.call("POST", "/api/agent-pairing/lookup", map[string]string{"user_code": "999-999-999"}, true, "", 429)
}

func TestPairingConcurrentApprovalAndRedemption(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	body := map[string]any{"request_digest": p.review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{"claude-local"}}
	var wg sync.WaitGroup
	codes := make(chan int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, f.request("POST", "/api/agent-pairing/requests/"+p.id+"/approve", body, true, ""))
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != 200 {
			t.Fatalf("concurrent approval %d", c)
		}
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, f.request("POST", "/api/agent-pairing/redeem", map[string]string{"tenant_id": f.tenantID, "request_id": p.id, "device_secret": p.device}, false, ""))
			if w.Code != 200 {
				t.Errorf("concurrent redeem %d", w.Code)
			}
		}()
	}
	wg.Wait()
	for _, table := range []string{"agent_pairing_computers", "agent_pairing_enrollments", "agent_keys", "agent_runs", "account_allowance_windows"} {
		var n int
		if err := f.db.Admin.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s count %d", table, n)
		}
	}
}

func TestPairingGuideIsAgentReadableAndPublicRoutesExact(t *testing.T) {
	h := agentpairing.GuidePage(http.NotFoundHandler(), fstest.MapFS{"index.html": {Data: []byte(`<html><body><div id="app"></div><script src="/assets/pinned.js"></script></body></html>`)}}, origin, nixGuideFixture())
	r := httptest.NewRequest("GET", "/agents/register-agent", nil)
	r.Host = "attacker.invalid"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	for _, want := range []string{"Connect a computer", "brew install inspr-at/tap/aeon-agentd", "/assets/pinned.js", "short code", "Server version:", "Nix / Home Manager", "aeon-agentd pair --url", "services.aeon.enable", "needs a paired-service update", "Cursor ask mode and an isolated config do not enforce a no-tools policy.", "Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.", "Qualified verification with enforced no-tools mode."} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("guide lacks %s", want)
		}
	}
	if strings.Contains(w.Body.String(), "attacker.invalid") {
		t.Fatal("guide trusts request Host")
	}
	for _, path := range []string{"/api/agent-pairing/redeem/extra", "/api/agent-pairing/requests/x/approve", "/api/agent-pairing/self"} {
		if agentpairing.PublicRoute("POST", path) {
			t.Fatal("public prefix bypass")
		}
	}
	if agentpairing.PublicRoute("GET", "/api/agent-pairing/device") {
		t.Fatal("public method bypass")
	}
}

func TestPairingCustomApproverAndProgress(t *testing.T) {
	f := newFixture(t)
	// Replace admin fixture with a precise custom delegation, no human key or
	// role management powers. Pairing relies on account.manage + live scopes.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'pairing_person','Pairing person') RETURNING id::text`, f.tenantID).Scan(&role); err != nil {
			return err
		}
		for _, scope := range append(slicesClone(agentpairing.RuntimePermissions), "account.manage") {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.tenantID, role, scope); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, f.person, role)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	if v.RuntimePrefix == "" {
		t.Fatal("eligible custom person could not pair")
	}
	var progress agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "progress": map[string]string{"state": "login_required", "error_code": "login_required"}}, false, "", 200), &progress)
	if progress.SetupState != "login_required" || progress.SetupError != "login_required" || progress.LastSeenAt == nil || progress.RuntimePrefix != "" {
		t.Fatal("setup progress missing or grants leaked")
	}
	f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "progress": map[string]string{"state": "connected", "error_code": "untrusted-log-text"}}, false, "", 400)
}
func slicesClone(s []string) []string { return append([]string{}, s...) }

func TestPairingClaimDisconnectRaceAndVerificationExpiry(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	e := v.Enrollments[0]
	f.probe(v, e, key, 200)
	res := f.reserve(v, e, key, 200)
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, op := range []string{"claim", "disconnect"} {
		wg.Add(1)
		go func(op string) {
			defer wg.Done()
			<-start
			var r *http.Request
			if op == "claim" {
				r = f.request("POST", "/api/runs/"+*e.VerificationRunID+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": res}, false, key)
			} else {
				r = f.request("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/enrollments/"+e.AccountID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "")
			}
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, r)
			results <- w.Code
		}(op)
	}
	close(start)
	wg.Wait()
	close(results)
	for code := range results {
		if code != 200 && code != 410 && code != 409 {
			t.Fatalf("race returned %d", code)
		}
	}
	var status, enrollment string
	var held int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,e.state,w.reserved FROM agent_runs r JOIN agent_pairing_enrollments e ON e.verification_run_id=r.id JOIN account_allowance_windows w ON w.account_id=e.account_id WHERE r.id=$1`, *e.VerificationRunID).Scan(&status, &enrollment, &held); err != nil {
		t.Fatal(err)
	}
	if enrollment != "revoked" || !(status == "starting" && held == 1 || status == "cancelled" && held == 0) {
		t.Fatalf("race lost fencing/accounting: %s %s %d", enrollment, status, held)
	}
	f.claim(v, e, key, res, 410)
	f.call("PATCH", "/api/agent-accounts/"+e.AccountID, map[string]string{"state": "available"}, true, "", 410)
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/windows", map[string]any{"starts_at": "2030-01-01T00:00:00Z", "ends_at": "2030-01-02T00:00:00Z", "unit": "requests", "allowance": 5, "pace_model": "unrestricted"}, true, "", 410)
	q := f.propose("claude")
	f.approve(q, "one_per_harness")
	fresh := f.redeem(q)
	next := fresh.Enrollments[0]
	k := "aeon_" + fresh.RuntimePrefix + "_" + q.runtime
	f.probe(fresh, next, k, 200)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET verification_expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, next.AccountID); err != nil {
		t.Fatal(err)
	}
	f.reserve(fresh, next, k, 409)
	var queue []agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/queued", nil, false, k, 200), &queue)
	if len(queue) != 0 {
		t.Fatal("expired verification still dispatched")
	}
}

func TestPairingZeroUsageCannotRefillOrSubstituteAccount(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	e := v.Enrollments[0]
	f.probe(v, e, key, 200)
	res := f.reserve(v, e, key, 200)
	f.claim(v, e, key, res, 200)
	r := f.request("POST", "/api/runs/"+*e.VerificationRunID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "failed", "turn_count_delta": 0, "error_code": "child_exit_failed"}, false, key)
	r.Header.Set(agentruns.DaemonHeader, *v.DaemonID)
	r.Header.Set(agentruns.GenerationHeader, "test-generation")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("zero settlement: %d", w.Code)
	}
	var run agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/"+*e.VerificationRunID, nil, false, key, 200), &run)
	// Even a person-created replacement cannot consume freed zero-turn capacity.
	var replacement agentruns.Run
	decodeResult(t, f.call("POST", "/api/work-orders/"+run.OrderID+"/runs", map[string]string{"agent_principal_id": *v.PrincipalID, "model_profile_id": e.ProfileID, "requested_account_id": e.AccountID}, true, "", 201), &replacement)
	var blockedQueue []agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/queued", nil, false, key, 200), &blockedQueue)
	if len(blockedQueue) != 0 {
		t.Fatal("verification-only account dispatched an unrelated run")
	}
	substitute := e
	substitute.VerificationRunID = &replacement.ID
	f.reserve(v, substitute, key, 409)
	// Future ongoing limits do not unlock the still-active verification window.
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/windows", map[string]any{"starts_at": v.Verification.ExpiresAt, "ends_at": v.Verification.ExpiresAt.Add(3600 * 1e9), "unit": "requests", "allowance": 10, "pace_model": "unrestricted"}, true, "", 201)
	f.reserve(v, substitute, key, 409)
	var count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_pairing_enrollments WHERE verification_claimed_at IS NOT NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("one-shot marker lost")
	}
}

func TestPairingTenantAndInputValidation(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	foreign, err := tenantbootstrap.Create(t.Context(), f.db.App, "foreign-person", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	am, err := auth.New(auth.Config{Env: "dev", PublicURL: origin, SessionKey: []byte(nonce()), BootstrapTenantSlug: "foreign-person", BootstrapAdminEmail: "foreign@example.test"}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{am, agentpairing.New(f.db.App, origin, "foreign-person")}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	other := &fixture{t: t, db: f.db, h: api.Handler(), tenantID: foreign}
	login := other.call("POST", "/api/auth/dev-login", map[string]string{"email": "foreign@example.test"}, false, "", 200)
	other.cookie = login.Result().Cookies()[0]
	other.call("POST", "/api/agent-pairing/lookup", map[string]string{"user_code": p.code}, true, "", 404)
	other.call("POST", "/api/agent-pairing/requests/"+p.id+"/approve", map[string]any{"request_digest": p.review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{"claude-local"}}, true, "", 404)
	f.call("POST", "/api/agent-pairing/requests/"+p.id+"/approve", map[string]any{"request_digest": p.review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{"unknown-local"}}, true, "", 400)
	changed := map[string]any{}
	for k, v := range p.request {
		changed[k] = v
	}
	changed["workspace_path"] = "/different-folder"
	f.call("POST", "/api/agent-pairing/device", changed, false, "", 409)
	changed["request_id"] = uuid(t, f.db)
	changed["capabilities"] = []string{"force_stop"}
	f.call("POST", "/api/agent-pairing/device", changed, false, "", 400)
	changed["capabilities"] = []string{"managed_runs"}
	changed["accounts"] = []map[string]string{{"account_key": "another", "label": "Other", "harness": "cursor", "model_profile_id": f.profiles["codex"]}}
	f.call("POST", "/api/agent-pairing/device", changed, false, "", 400)
	f.call("POST", "/api/agent-pairing/requests/"+p.id+"/deny", map[string]any{}, true, "", 200)
	if f.redeem(p).State != "denied" {
		t.Fatal("denial did not persist")
	}
}

func TestPairingRevokedCleanupPreservesUnconfirmedAccounting(t *testing.T) {
	f := newFixture(t)
	p, v := f.twoQualifiedEnrollments()
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	for _, e := range v.Enrollments {
		f.probe(v, e, key, 200)
		res := f.reserve(v, e, key, 200)
		f.claim(v, e, key, res, 200)
	}
	e := v.Enrollments[0]
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/enrollments/"+e.AccountID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
	f.telemetry(v, e, key, 410)
	var partial agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "cleanup_confirmed_account_ids": []string{e.AccountID}}, false, "", 200), &partial)
	if partial.Enrollments[0].LocalProcesses != "drained" || partial.Enrollments[0].AccountingState != "unconfirmed" || *partial.ComputerState != "connected" {
		t.Fatal("selective cleanup conflates process and accounting or disables shared computer")
	}
	f.probe(v, v.Enrollments[1], key, 200)
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
	var full agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "cleanup_confirmed_account_ids": []string{e.AccountID, v.Enrollments[1].AccountID}, "computer_cleanup_confirmed": true}, false, "", 200), &full)
	if full.Processes != "drained" || full.AccountingState != "unconfirmed" || full.Cleanup != "confirmed" {
		t.Fatal("safe local cleanup requires impossible revoked telemetry or erases uncertainty")
	}
	var active int
	var held int64
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs WHERE status='starting'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT sum(reserved) FROM account_allowance_windows`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if active != 2 || held != 2 {
		t.Fatal("lifecycle acknowledgement settled/released unconfirmed usage")
	}
}

func TestPairingDisconnectRequiresFreshScopePreview(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_computers SET revision=revision+1 WHERE id=$1`, *v.ComputerID); err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]any{"mode": "drain", "expected_revision": v.Revision}, true, "", 409)
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]any{"mode": "drain", "expected_revision": v.Revision + 1}, true, "", 200)
	// Response loss followed by the identical old confirmation remains safe.
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]any{"mode": "drain", "expected_revision": v.Revision + 1}, true, "", 200)
}

func TestPairingGuideReleaseContract(t *testing.T) {
	old := version.Version
	version.Version = "260927160212.0.0"
	t.Cleanup(func() { version.Version = old })
	mux := http.NewServeMux()
	agentpairing.New(nil, origin, "reviewed-tenant", nixGuideFixture()).Mount(mux)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/agent-pairing/guide", nil)
	r.Host = "attacker.invalid"
	mux.ServeHTTP(w, r)
	var guide struct {
		Capabilities  map[string]agentpairing.VerificationCapability `json:"verification_capabilities"`
		HelperVersion string                                         `json:"verification_helper_version"`
		Instance      string                                         `json:"instance_url"`
		Tenant        string                                         `json:"default_tenant_slug"`
		Command       string                                         `json:"setup_command"`
		Qualification string                                         `json:"platform_qualification"`
		Targets       []agentpairing.InstallTarget                   `json:"install_targets"`
		Managed       agentpairing.ManagedSetup                      `json:"managed_setup"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &guide); err != nil {
		t.Fatal(err)
	}
	if guide.HelperVersion != version.Version || !guide.Capabilities["claude"].Supported || guide.Capabilities["codex"].Supported || guide.Capabilities["cursor"].Supported || guide.Capabilities["grok"].Supported || guide.Instance != origin || guide.Tenant != "reviewed-tenant" || len(guide.Targets) != 4 || !strings.Contains(guide.Command, " pair --url '") || !strings.Contains(guide.Qualification, "candidate") {
		t.Fatalf("guide release contract mismatch: %s", w.Body.String())
	}
	if guide.Managed.Command != "aeon-agentd pair --url '"+origin+"'" || guide.Managed.ServiceOption != "services.aeon.enable" || guide.Managed.ModuleURL != nixGuideFixture().ModuleURL || strings.Contains(guide.Managed.Command, "attacker.invalid") {
		t.Fatal("Nix guide is not bound to the server origin and owning module")
	}
	for _, target := range guide.Targets {
		if !strings.HasPrefix(target.ArtifactURL, "https://github.com/inspr-at/paimos/releases/download/v260927160212.0.0/paimos-agentd-") || !strings.Contains(target.Command, "mkdir \"$aeon_pairing_dir\"") || !strings.Contains(target.Command, "if (n != 1) exit 1") || strings.Contains(target.Command, "attacker.invalid") {
			t.Fatalf("unsafe install contract: %+v", target)
		}
		check := strings.Index(target.Command, " -c selected.SHA256SUMS")
		install := strings.Index(target.Command, "install -m 0700")
		if check < 0 || install < check {
			t.Fatal("artifact becomes executable before checksum verification")
		}
	}
}

func (f *fixture) twoQualifiedEnrollments() (*proposal, agentpairing.View) {
	f.t.Helper()
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	q := &proposal{id: uuid(f.t, f.db), device: nonce(), runtime: p.runtime, lifecycle: p.lifecycle, request: map[string]any{}}
	for k, x := range p.request {
		q.request[k] = x
	}
	q.request["request_id"] = q.id
	q.request["device_hash"] = hash(q.device)
	q.request["existing_computer_id"] = *v.ComputerID
	q.request["existing_lifecycle_secret"] = p.lifecycle
	q.request["accounts"] = []map[string]string{{"account_key": "claude-second", "harness": "claude", "label": "Second selected account", "model_profile_id": f.profiles["claude"]}}
	f.submit(q)
	f.approve(q, "one_per_harness")
	f.redeem(q)
	return p, f.redeem(p)
}

func expireVerification(t *testing.T, f *fixture, e agentpairing.Enrollment) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(t.Context(), tx); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET verification_expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, e.AccountID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPairingExpiredReservationReleasesSlotAndAllowsOngoing(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, e, key, 200)
	ids := f.reserve(v, e, key, 200)
	expireVerification(t, f, e)
	for i := 0; i < 2; i++ {
		var queue []agentruns.Run
		decodeResult(t, f.call("GET", "/api/runs/queued", nil, false, key, 200), &queue)
		if len(queue) != 0 {
			t.Fatal("expired queued verification dispatched")
		}
	}
	var state, holdState string
	var held int64
	var started, claimed, expired bool
	err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,res.state,w.reserved,r.started_at IS NOT NULL,e.verification_claimed_at IS NOT NULL,e.verification_expired_at IS NOT NULL FROM agent_runs r JOIN agent_pairing_enrollments e ON e.verification_run_id=r.id JOIN account_reservations res ON res.run_id=r.id JOIN account_allowance_windows w ON w.id=res.window_id WHERE r.id=$1`, *e.VerificationRunID).Scan(&state, &holdState, &held, &started, &claimed, &expired)
	if err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || holdState != "released" || held != 0 || started || claimed || !expired {
		t.Fatalf("expiry leaked reservation or invented process: %s %s held=%d started=%v claimed=%v expired=%v", state, holdState, held, started, claimed, expired)
	}
	retry := f.redeem(p)
	if retry.Enrollments[0].VerificationState != "expired" || *retry.Enrollments[0].VerificationRunID != *e.VerificationRunID {
		t.Fatal("expiry projection/retry binding wrong")
	}
	f.claim(v, e, key, ids, 409)
	f.reserve(v, e, key, 409)
	var counts struct{ runs, windows int }
	if err = f.db.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent_runs),(SELECT count(*) FROM account_allowance_windows)`).Scan(&counts.runs, &counts.windows); err != nil {
		t.Fatal(err)
	}
	if counts.runs != 1 || counts.windows != 1 {
		t.Fatal("expiry/refusal refilled verification")
	}
	// Move the existing verification window into the past, as the real clock
	// would do. A separately approved current window must recover the slot.
	if _, err = f.db.Admin.Exec(t.Context(), `UPDATE account_allowance_windows SET starts_at=clock_timestamp()-interval '1 minute',ends_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, e.AccountID); err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/windows", map[string]any{"starts_at": time.Now(), "ends_at": time.Now().Add(time.Hour), "unit": "requests", "allowance": 2, "pace_model": "unrestricted"}, true, "", 201)
	var old, ongoing agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/"+*e.VerificationRunID, nil, false, key, 200), &old)
	decodeResult(t, f.call("POST", "/api/work-orders/"+old.OrderID+"/runs", map[string]string{"agent_principal_id": *v.PrincipalID, "model_profile_id": e.ProfileID, "requested_account_id": e.AccountID}, true, "", 201), &ongoing)
	next := e
	next.VerificationRunID = &ongoing.ID
	f.claim(v, next, key, f.reserve(v, next, key, 200), 200)
	f.telemetry(v, next, key, 200)
	var drained agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "drain"}, true, "", 200), &drained)
	if *drained.ComputerState != "revoked" || drained.AccountingState != "settled" {
		t.Fatal("expired row blocked completed drain")
	}
}

func TestPairingExpiryClaimRaceKeepsClaimedAccounting(t *testing.T) {
	for _, claimedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("claimed_first_%v", claimedFirst), func(t *testing.T) {
			f := newFixture(t)
			p := f.propose("claude")
			f.approve(p, "one_per_harness")
			v := f.redeem(p)
			e := v.Enrollments[0]
			key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
			f.probe(v, e, key, 200)
			ids := f.reserve(v, e, key, 200)
			if claimedFirst {
				f.claim(v, e, key, ids, 200)
			}
			start := make(chan struct{})
			codes := make(chan int, 1)
			errs := make(chan error, 1)
			go func() {
				<-start
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, f.request("POST", "/api/runs/"+*e.VerificationRunID+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": ids}, false, key))
				codes <- w.Code
			}()
			go func() {
				<-start
				errs <- db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
					if err := agentpairing.Lock(t.Context(), tx); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET verification_expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, e.AccountID); err != nil {
						return err
					}
					return agentpairing.ExpireUnclaimedVerifications(t.Context(), tx)
				})
			}()
			close(start)
			code := <-codes
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			if code != 200 && code != 409 {
				t.Fatalf("claim/expiry race status %d", code)
			}
			f.call("GET", "/api/runs/queued", nil, false, key, 200)
			var state string
			var held int64
			var claimed bool
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,w.reserved,e.verification_claimed_at IS NOT NULL FROM agent_pairing_enrollments e JOIN agent_runs r ON r.id=e.verification_run_id JOIN account_allowance_windows w ON w.account_id=e.account_id WHERE e.account_id=$1`, e.AccountID).Scan(&state, &held, &claimed); err != nil {
				t.Fatal(err)
			}
			if code == 200 {
				if state != "starting" || held != 1 || !claimed {
					t.Fatal("expiry cancelled claimed work or lost uncertain usage")
				}
				var drain agentpairing.View
				decodeResult(t, f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "drain"}, true, "", 200), &drain)
				if *drain.ComputerState != "draining" || drain.AccountingState != "unconfirmed" {
					t.Fatal("claim uncertainty incorrectly treated as process exit")
				}
				f.telemetry(v, e, key, 200)
			} else {
				if state != "cancelled" || held != 0 || claimed {
					t.Fatal("expiry winner leaked hold or claim marker")
				}
				var drain agentpairing.View
				decodeResult(t, f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "drain"}, true, "", 200), &drain)
				if *drain.ComputerState != "revoked" {
					t.Fatal("unclaimed expired run blocked drain")
				}
			}
		})
	}
}

func TestPairingVerificationCapabilitiesEnforcedAndProgressTruthful(t *testing.T) {
	f := newFixture(t)
	for _, h := range []string{"codex", "cursor"} {
		p := f.propose(h, "claude")
		if p.review.VerificationCapabilities[h].Supported || !p.review.VerificationCapabilities["claude"].Supported || p.review.VerificationCapabilities["claude"].Policy != "no_tools" {
			t.Fatal("wrong preapproval qualification")
		}
		reason := p.review.VerificationCapabilities[h].Reason
		if h == "codex" && !strings.Contains(reason, "startup hooks") || h == "cursor" && !strings.Contains(reason, "no-tools policy") {
			t.Fatal("review lost the specific isolation blocker")
		}
		w := f.call("POST", "/api/agent-pairing/requests/"+p.id+"/approve", map[string]any{"request_digest": p.review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{h + "-local", "claude-local"}}, true, "", 409)
		if !strings.Contains(w.Body.String(), `"code":"verification_unavailable"`) {
			t.Fatal("missing typed unsupported result")
		}
		var count int
		if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_pairing_enrollments WHERE request_id=$1`, p.id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 || f.redeem(p).State != "pending" {
			t.Fatal("unsupported verification silently changed selection or granted authority")
		}
		// Explicit Connect only still pairs these accounts; no verification is minted.
		f.approve(p, "connect_only")
		v := f.redeem(p)
		key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
		for _, e := range v.Enrollments {
			if e.VerificationRunID != nil {
				t.Fatal("connect only created run")
			}
			f.probe(v, e, key, 200)
		}
		var progress agentpairing.View
		decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "progress": map[string]string{"state": "connected", "error_code": "verification_unavailable"}}, false, "", 200), &progress)
		if progress.SetupState != "connected" || progress.SetupError != "verification_unavailable" || *progress.ComputerState != "connected" || progress.Connectivity != "online" {
			t.Fatal("verification unavailable falsely reports installation/disconnection failure")
		}
	}
	// A person may instead explicitly leave the unavailable account out.
	p := f.propose("claude", "codex")
	var approved agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/requests/"+p.id+"/approve", map[string]any{"request_digest": p.review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{"claude-local"}}, true, "", 200), &approved)
	if len(approved.Enrollments) != 1 || approved.Enrollments[0].Harness != "claude" || approved.Enrollments[0].VerificationRunID == nil {
		t.Fatal("explicit supported subset not honored")
	}
}

func TestNativeGrokVerificationQualifiedOnlyOnMacOSArm64(t *testing.T) {
	f := newFixture(t)
	var guide struct {
		Capabilities map[string]agentpairing.VerificationCapability `json:"verification_capabilities"`
	}
	decodeResult(t, f.call("GET", "/api/agent-pairing/guide", nil, false, "", 200), &guide)
	if c := guide.Capabilities["grok"]; c.Supported || c.Policy != "unavailable" || !strings.Contains(c.Reason, "macOS arm64") {
		t.Fatal("global guide falsely advertises native Grok verification")
	}
	targets := agentpairing.VerificationTargets()
	if !slices.Contains(targets, "darwin/arm64/grok") || slices.Contains(targets, "darwin/amd64/grok") || slices.Contains(targets, "linux/arm64/grok") || slices.Contains(targets, "linux/amd64/grok") {
		t.Fatal("dispatch targets differ from Grok platform qualification")
	}
	qualified := f.proposePlatform("darwin", "arm64", "grok")
	if c := qualified.review.VerificationCapabilities["grok"]; !c.Supported || c.Policy != "no_tools" || c.Reason != "" {
		t.Fatal("qualified platform did not advertise enforced no-tools Grok verification")
	}
	v := f.approve(qualified, "one_per_harness")
	if len(v.Enrollments) != 1 || v.Enrollments[0].VerificationRunID == nil || v.Verification.Mode == nil || *v.Verification.Mode != "one_per_harness" ||
		v.Verification.RunsPerAccount != 1 || v.Verification.MaxParallel != 1 || v.Verification.MaxDuration != agentpairing.VerificationSeconds || v.Verification.Allowance != 1 || v.Verification.Unit != "requests" {
		t.Fatal("qualified approval did not create exactly one bounded verification")
	}
	var runs, allowance, duration int
	var unit, purpose string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_pairing_enrollments e JOIN agent_runs r ON r.id=e.verification_run_id WHERE e.request_id=$1`, qualified.id).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT allowance,unit FROM account_allowance_windows WHERE account_id=$1 AND pairing_verification`, v.Enrollments[0].AccountID).Scan(&allowance, &unit); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.purpose,w.max_duration_seconds FROM agent_runs r JOIN work_orders w ON w.node_id=r.work_order_id WHERE r.id=$1`, *v.Enrollments[0].VerificationRunID).Scan(&purpose, &duration); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || allowance != 1 || unit != "requests" || purpose != "pairing_verification" || duration != agentpairing.VerificationSeconds {
		t.Fatal("qualified Grok grant escaped the one-run, one-request, 60-second bounds")
	}

	resources := []string{"agent_pairing_computers", "agent_pairing_enrollments", "agent_accounts", "agent_keys", "agent_runs", "account_allowance_windows", "work_orders", "nodes"}
	counts := func(t *testing.T) []int {
		t.Helper()
		out := make([]int, len(resources))
		for i, table := range resources {
			if err := f.db.Admin.QueryRow(t.Context(), "SELECT count(*) FROM "+table+" WHERE tenant_id=$1", f.tenantID).Scan(&out[i]); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	for _, target := range []struct{ platform, arch string }{{"darwin", "amd64"}, {"linux", "arm64"}, {"linux", "amd64"}} {
		t.Run(target.platform+"/"+target.arch, func(t *testing.T) {
			p := f.proposePlatform(target.platform, target.arch, "grok")
			if c := p.review.VerificationCapabilities["grok"]; c.Supported || c.Policy != "unavailable" || !strings.Contains(c.Reason, "macOS arm64") {
				t.Fatal("unsupported platform advertised Grok verification")
			}
			before := counts(t)
			w := f.call("POST", "/api/agent-pairing/requests/"+p.id+"/approve", map[string]any{"request_digest": p.review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{"grok-local"}}, true, "", 409)
			if !strings.Contains(w.Body.String(), `"code":"verification_unavailable"`) {
				t.Fatal("unsupported Grok approval lacked typed rejection")
			}
			if f.redeem(p).State != "pending" {
				t.Fatal("unsupported Grok approval advanced pairing state")
			}
			after := counts(t)
			for i := range resources {
				if before[i] != after[i] {
					t.Fatalf("unsupported Grok approval created %s", resources[i])
				}
			}
		})
	}
}

func TestPairingLegacyUnsupportedVerificationRemainsPairedUntilExpiry(t *testing.T) {
	f := newFixture(t)
	p := f.propose("codex")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	e := v.Enrollments[0]
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	// Historical releases issued Codex verification before the qualification
	// restriction. Seed that durable shape; production has no capability override.
	var run string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		var project, order string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,aeon_next_node_key($1,k.short_prefix),k.id,'Historical connection check' FROM node_kinds k WHERE k.slug='project' RETURNING id::text`, f.tenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,aeon_next_node_key($1,k.short_prefix),k.id,'Historical Codex verification',$2 FROM node_kinds k WHERE k.slug='work_order' RETURNING id::text`, f.tenantID, project).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,assignee_principal_id,status,max_duration_seconds) VALUES($1,$2,$3,$4,'ready',60)`, f.tenantID, order, f.person, *v.PrincipalID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_account_id,purpose) VALUES($1,$2,$3,$4,$5,'pairing_verification') RETURNING id::text`, f.tenantID, order, *v.PrincipalID, e.ProfileID, e.AccountID).Scan(&run); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_pairing_enrollments SET verification_run_id=$2 WHERE account_id=$1`, e.AccountID, run); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,pairing_verification) VALUES($1,$2,clock_timestamp(),$3,'requests',1,'unrestricted',true)`, f.tenantID, e.AccountID, v.Verification.ExpiresAt); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_pairing_requests SET verification='one_per_harness' WHERE id=$1`, p.id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.VerificationRunID = &run
	f.probe(v, e, key, 200)
	var view agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "progress": map[string]string{"state": "connected", "error_code": "verification_unavailable"}}, false, "", 200), &view)
	if view.Enrollments[0].VerificationState != "unavailable" || view.Enrollments[0].VerificationError != "verification_unavailable" || view.Connectivity != "online" || view.SetupState != "connected" || *view.ComputerState != "connected" {
		t.Fatal("legacy unsupported verification misreported")
	}
	var queue []agentruns.Run
	decodeResult(t, f.call("GET", "/api/runs/queued", nil, false, key, 200), &queue)
	if len(queue) != 0 {
		t.Fatal("unsupported legacy verification dispatched")
	}
	w := f.call("POST", "/api/agent-accounts/route", map[string]any{"run_id": run, "daemon_id": *v.DaemonID, "account_ids": []string{e.AccountID}, "estimated_units": map[string]int{"requests": 1}}, false, key, 409)
	if !strings.Contains(w.Body.String(), "verification_unavailable") {
		t.Fatal("unsupported legacy route did not fail closed")
	}
	expireVerification(t, f, e)
	f.call("GET", "/api/runs/queued", nil, false, key, 200)
	var status string
	if err = f.db.Admin.QueryRow(t.Context(), `SELECT status FROM agent_runs WHERE id=$1`, run).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatal("unavailable legacy verification never expires")
	}
}
