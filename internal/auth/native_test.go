// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/devicesessions"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

type nativeFixture struct {
	t                                *testing.T
	m                                *Module
	issuer                           *fakeOIDC
	appURL, host, tid, other, person string
	now                              time.Time
}
type nativePending struct {
	client                      *http.Client
	state, verifier, tx, cookie string
	provider                    url.Values
}
type nativeResponse struct {
	status int
	body   []byte
	header http.Header
	err    error
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	reset(t)
	f := &nativeFixture{t: t, host: "native.example", tid: insertTenant(t, "inspr", "INSPR"), other: insertTenant(t, "other", "Other")}
	f.issuer = startFakeOIDC(t, "aeon-public")
	var err error
	f.person, err = tenantbootstrap.BindOIDC(t.Context(), appPool, "inspr", f.issuer.issuer, "native-person", "Person", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := adminPool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&f.now); err != nil {
		t.Fatal(err)
	}
	f.m = newMod(t, Config{Env: envDev, PublicURL: "https://native.example", OIDCIssuer: f.issuer.issuer, OIDCClientID: "aeon-public", BootstrapTenantSlug: "inspr"})
	f.m.DeviceSessions = devicesessions.New(appPool, func() time.Time { return f.now })
	f.appURL = startApp(t, f.m).URL
	return f
}

// The test transport is HTTP; Host still proves the canonical HTTPS issuer
// binding seen by a server behind its TLS reverse proxy.
func (f *nativeFixture) request(c *http.Client, method, path, body, cookie string) nativeResponse {
	req, err := http.NewRequestWithContext(f.t.Context(), method, f.appURL+path, strings.NewReader(body))
	if err != nil {
		return nativeResponse{err: err}
	}
	req.Host = f.host
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := c.Do(req)
	if err != nil {
		return nativeResponse{err: err}
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	return nativeResponse{status: res.StatusCode, body: b, header: res.Header.Clone(), err: err}
}
func (f *nativeFixture) check(res nativeResponse, want int) {
	f.t.Helper()
	if res.err != nil || res.status != want {
		f.t.Fatalf("native response: status=%d want=%d error=%v", res.status, want, res.err)
	}
}
func (f *nativeFixture) begin() nativePending {
	f.t.Helper()
	state, err := randomString(16)
	if err != nil {
		f.t.Fatal(err)
	}
	p := nativePending{client: newHTTPClient(), state: state, verifier: oauth2.GenerateVerifier()}
	q := url.Values{"tenant": {"inspr"}, "state": {p.state}, "code_challenge": {oauth2.S256ChallengeFromVerifier(p.verifier)}, "code_challenge_method": {"S256"}, "redirect_uri": {devicesessions.RedirectURI}}
	res := f.request(p.client, "GET", "/api/auth/native/start?"+q.Encode(), "", "")
	f.check(res, 302)
	if res.header.Get("Cache-Control") != "no-store" {
		f.t.Fatal("start can be cached")
	}
	p.provider = assertAuthURL(f.t, "https://native.example", res.header.Get("Location"))
	for _, c := range (&http.Response{Header: res.header}).Cookies() {
		if c.Name != oidcCookieName {
			continue
		}
		payload, err := f.m.open(c.Value)
		if err != nil || payload.Native == nil || payload.Native.Tx == "" {
			f.t.Fatal("missing native transaction reference")
		}
		p.tx, p.cookie = payload.Native.Tx, c.Name+"="+c.Value
		b, _ := json.Marshal(payload)
		if bytes.Contains(b, []byte(p.state)) || bytes.Contains(b, []byte(p.verifier)) {
			f.t.Fatal("app binding leaked into OIDC cookie")
		}
	}
	if p.tx == "" || p.provider.Get("state") == p.state || p.provider.Get("code_challenge") == q.Get("code_challenge") {
		f.t.Fatal("PKCE hops or states were conflated")
	}
	return p
}
func (f *nativeFixture) allow(p nativePending, code string) string {
	f.issuer.allow(code, p.provider.Get("code_challenge"), p.provider.Get("nonce"), "native-person", "native@example.com", "Person", false)
	return "/api/auth/callback?" + url.Values{"code": {code}, "state": {p.provider.Get("state")}}.Encode()
}
func (f *nativeFixture) handoff(p nativePending) string {
	f.t.Helper()
	res := f.request(p.client, "GET", f.allow(p, "provider-code"), "", "")
	f.check(res, 302)
	u, err := url.Parse(res.header.Get("Location"))
	if err != nil || u.Scheme+"://"+u.Host+u.Path != devicesessions.RedirectURI || u.Query().Get("state") != p.state || u.Query().Get("code") == "" {
		f.t.Fatal("native callback lost its exact app state or code")
	}
	for _, c := range (&http.Response{Header: res.header}).Cookies() {
		if c.Name == sessionCookieName {
			f.t.Fatal("native callback issued a browser session cookie")
		}
	}
	var state *string
	var at, expires time.Time
	if err := adminPool.QueryRow(f.t.Context(), `SELECT state_app,callback_at,code_expires_at FROM native_login_transactions WHERE tenant_id=$1 AND id=$2`, f.tid, p.tx).Scan(&state, &at, &expires); err != nil {
		f.t.Fatal(err)
	}
	if state != nil || !at.Equal(f.now) || !expires.Equal(f.now.Add(time.Minute)) {
		f.t.Fatal("callback redirect preceded durable state clearing and 60s code binding")
	}
	return u.Query().Get("code")
}
func nativeBody(p nativePending, code string) map[string]string {
	return map[string]string{"code": code, "code_verifier": p.verifier, "state": p.state, "redirect_uri": devicesessions.RedirectURI, "device_label": "Test phone"}
}
func (f *nativeFixture) exchange(in map[string]string, suffix string) nativeResponse {
	b, err := json.Marshal(in)
	if err != nil {
		return nativeResponse{err: err}
	}
	return f.request(newHTTPClient(), "POST", "/api/auth/native/token"+suffix, string(b), "")
}

// AEON-1731 risk: callback state is lost/leaked, a browser session is created,
// or an initial device family is minted without its full PKCE/account binding.
func TestNativeLoginHandoffAndExchange(t *testing.T) {
	f := newNativeFixture(t)
	var logs bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	p := f.begin()
	code := f.handoff(p)
	res := f.exchange(nativeBody(p, code), "")
	f.check(res, 200)
	var out devicesessions.Tokens
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatal(err)
	}
	if out.TenantID != f.tid || out.PersonID != f.person || !nativeID.MatchString(out.DeviceSessionID) || out.ExpiresIn != 900 || !strings.HasPrefix(out.AccessToken, "pdv_") || !strings.HasPrefix(out.RefreshToken, "pdr_") || out.AccessToken == out.RefreshToken {
		t.Fatal("incorrect device family/account response")
	}
	if res.header.Get("Cache-Control") != "no-store" || res.header.Get("Set-Cookie") != "" {
		t.Fatal("exchange cached or set a browser cookie")
	}
	var access, refresh []byte
	var created, last, accessExpiry, idle, absolute time.Time
	var label string
	if err := adminPool.QueryRow(t.Context(), `SELECT access_token_hash,refresh_token_hash,created_at,last_used_at,access_expires_at,idle_expires_at,absolute_expires_at,device_label FROM device_sessions WHERE tenant_id=$1 AND id=$2`, f.tid, out.DeviceSessionID).Scan(&access, &refresh, &created, &last, &accessExpiry, &idle, &absolute, &label); err != nil {
		t.Fatal(err)
	}
	ah := sha256.Sum256([]byte(out.AccessToken))
	rh := sha256.Sum256([]byte(out.RefreshToken))
	if !bytes.Equal(access, ah[:]) || !bytes.Equal(refresh, rh[:]) || !created.Equal(f.now) || !last.Equal(created) || !accessExpiry.Equal(created.Add(devicesessions.AccessTTL)) || !idle.Equal(created.Add(devicesessions.IdleTTL)) || !absolute.Equal(created.Add(devicesessions.AbsoluteTTL)) || label != "Test phone" {
		t.Fatal("incorrect stored credential hashes or family leases")
	}
	if scalar(t, adminPool, `SELECT count(*) FROM sessions`) != 0 || scalar(t, adminPool, `SELECT count(*) FROM agent_keys`) != 0 {
		t.Fatal("native sign-in minted browser/agent credentials")
	}
	f.check(f.exchange(nativeBody(p, code), ""), 401)
	// Replaying the signed login with another valid provider code must also fail.
	replay := f.request(newHTTPClient(), "GET", f.allow(p, "provider-replay"), "", p.cookie)
	f.check(replay, 302)
	if replay.header.Get("Location") != "/signin?error=failed" {
		t.Fatal("second native callback issued a code")
	}
	for _, secret := range []string{p.state, p.verifier, code, out.AccessToken, out.RefreshToken} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("authentication material appeared in logs")
		}
	}
	for _, table := range []string{"native_login_transactions", "device_sessions"} {
		if scalar(t, appPool, "SELECT count(*) FROM "+table) != 0 || countInTenant(t, f.other, "SELECT count(*) FROM "+table) != 0 || countInTenant(t, f.tid, "SELECT count(*) FROM "+table) != 1 {
			t.Fatal("native table tenant isolation failed")
		}
		var enabled, forced bool
		if err := adminPool.QueryRow(t.Context(), `SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE oid=$1::regclass`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatal("native table RLS is not forced")
		}
	}
	// B1b alone may admit a device bearer; prefixes must not become agent keys.
	status, _, _ := do(t, newHTTPClient(), "GET", f.appURL+"/api/me", "", http.Header{"Authorization": {"Bearer " + out.AccessToken}})
	if status != 401 {
		t.Fatal("B1a widened bearer admission")
	}
}

// AEON-1731 risk: a stolen/replayed code crosses a state, verifier, redirect,
// origin or tenant boundary, or survives loss of membership/person authority.
func TestNativeLoginRejectsBindingsAndAuthorityLoss(t *testing.T) {
	for _, name := range []string{"wrong-state", "missing-state", "wrong-verifier", "redirect", "other-instance", "other-tenant", "tenant-prefix", "expired-code", "expired-login", "removed-membership", "inactive-person", "changed-identity"} {
		t.Run(name, func(t *testing.T) {
			f := newNativeFixture(t)
			p := f.begin()
			code := f.handoff(p)
			in := nativeBody(p, code)
			suffix := ""
			want := 401
			switch name {
			case "wrong-state":
				in["state"] = strings.Repeat("x", 22)
			case "missing-state":
				delete(in, "state")
				want = 400
			case "wrong-verifier":
				in["code_verifier"] = strings.Repeat("x", 43)
			case "redirect":
				in["redirect_uri"] = "paimos-app://attacker/callback"
			case "other-instance":
				f.m.cfg.PublicURL = "https://other.example"
				f.host = "other.example"
			case "other-tenant":
				suffix = "?tenant=other"
			case "tenant-prefix":
				in["code"] = "pnl_" + strings.ReplaceAll(f.other, "-", "") + code[36:]
			case "expired-code":
				f.now = f.now.Add(devicesessions.CodeTTL)
			case "expired-login":
				f.now = f.now.Add(devicesessions.LoginTTL)
			case "removed-membership":
				if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.tid, f.person); err != nil {
					t.Fatal(err)
				}
			case "inactive-person":
				if _, err := adminPool.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE tenant_id=$1 AND id=$2`, f.tid, f.person); err != nil {
					t.Fatal(err)
				}
			case "changed-identity":
				if _, err := adminPool.Exec(t.Context(), `UPDATE principals SET identity_id=NULL WHERE tenant_id=$1 AND id=$2`, f.tid, f.person); err != nil {
					t.Fatal(err)
				}
			}
			res := f.exchange(in, suffix)
			f.check(res, want)
			if !bytes.Contains(res.body, []byte(map[int]string{400: "invalid exchange request", 401: "native login code refused"}[want])) {
				t.Fatal("exchange failed for the wrong reason")
			}
			var used *time.Time
			if err := adminPool.QueryRow(t.Context(), `SELECT used_at FROM native_login_transactions WHERE tenant_id=$1 AND id=$2`, f.tid, p.tx).Scan(&used); err != nil {
				t.Fatal(err)
			}
			if used != nil || scalar(t, adminPool, `SELECT count(*) FROM device_sessions`) != 0 {
				t.Fatal("refused exchange consumed a code or minted a family")
			}
		})
	}
}

// AEON-1731 risk: concurrent real callbacks or exchanges produce two codes or
// families. A held write and observed DB lock prove the operations overlap.
func TestNativeLoginConcurrentCallbackAndExchange(t *testing.T) {
	f := newNativeFixture(t)
	p := f.begin()
	pool, barrier, ctx := dbtest.BarrierPool(t, appPool, func(sql string) bool { return strings.Contains(sql, "SELECT state_app FROM native_login_transactions") })
	f.m.DeviceSessions = devicesessions.New(pool, func() time.Time { return f.now })
	firstPath, secondPath := f.allow(p, "provider-first"), f.allow(p, "provider-second")
	first, second := make(chan nativeResponse, 1), make(chan nativeResponse, 1)
	go func() { first <- f.request(newHTTPClient(), "GET", firstPath, "", p.cookie) }()
	pid := barrier.Wait(t, ctx)
	go func() { second <- f.request(newHTTPClient(), "GET", secondPath, "", p.cookie) }()
	dbtest.WaitForLock(t, ctx, testDB, pid, "transactionid")
	barrier.Release()
	a, b := dbtest.Await(t, ctx, first), dbtest.Await(t, ctx, second)
	f.check(a, 302)
	f.check(b, 302)
	u, err := url.Parse(a.header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("state") != p.state || u.Query().Get("code") == "" || b.header.Get("Location") != "/signin?error=failed" {
		t.Fatal("concurrent callbacks did not produce exactly one handoff")
	}
	code := u.Query().Get("code")
	var raw *string
	if err := adminPool.QueryRow(t.Context(), `SELECT state_app FROM native_login_transactions WHERE tenant_id=$1 AND id=$2`, f.tid, p.tx).Scan(&raw); err != nil || raw != nil {
		t.Fatal("callback failed to clear committed state")
	}
	pool, exchangeBarrier, exchangeCtx := dbtest.BarrierPool(t, appPool, func(sql string) bool { return strings.Contains(sql, "INSERT INTO device_sessions") })
	f.m.DeviceSessions = devicesessions.New(pool, func() time.Time { return f.now })
	go func() { first <- f.exchange(nativeBody(p, code), "") }()
	pid = exchangeBarrier.Wait(t, exchangeCtx)
	go func() { second <- f.exchange(nativeBody(p, code), "") }()
	dbtest.WaitForLock(t, exchangeCtx, testDB, pid, "transactionid")
	if scalar(t, adminPool, `SELECT count(*) FROM device_sessions`) != 0 {
		t.Fatal("family became visible before code consumption committed")
	}
	exchangeBarrier.Release()
	a, b = dbtest.Await(t, exchangeCtx, first), dbtest.Await(t, exchangeCtx, second)
	f.check(a, 200)
	f.check(b, 401)
	if !bytes.Contains(b.body, []byte("native login code refused")) || scalar(t, adminPool, `SELECT count(*) FROM device_sessions`) != 1 {
		t.Fatal("concurrent code use did not create exactly one family")
	}
}

// AEON-1731 risk: an exchange trusts a callback-era grant while a revocation
// commits. The final tenant fence must wait, then recheck current authority.
func TestNativeLoginExchangeWaitsForMembershipRevocation(t *testing.T) {
	f := newNativeFixture(t)
	p := f.begin()
	code := f.handoff(p)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tx, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := db.LockTenant(ctx, tx, f.tid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.tid, f.person); err != nil {
		t.Fatal(err)
	}
	var pid uint32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan nativeResponse, 1)
	go func() { done <- f.exchange(nativeBody(p, code), "") }()
	dbtest.WaitForLock(t, ctx, testDB, pid, "transactionid")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	res := dbtest.Await(t, ctx, done)
	f.check(res, 401)
	if !bytes.Contains(res.body, []byte("native login code refused")) || scalar(t, adminPool, `SELECT count(*) FROM device_sessions`) != 0 {
		t.Fatal("exchange survived membership revocation")
	}
}

// AEON-1731 risk: abandoned raw states persist after expiry, or cleanup crosses
// tenants. All handoff expiry decisions use the same injected fixture clock.
func TestNativeLoginExpiryCleanupAndLimits(t *testing.T) {
	f := newNativeFixture(t)
	p := f.begin()
	f.now = f.now.Add(devicesessions.LoginTTL - time.Nanosecond)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := f.m.DeviceSessions.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if countInTenant(t, f.tid, `SELECT count(*) FROM native_login_transactions`) != 1 {
		t.Fatal("purge removed an unexpired abandoned login")
	}
	f.now = f.now.Add(time.Nanosecond)
	if err := f.m.DeviceSessions.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar(t, adminPool, `SELECT count(*) FROM native_login_transactions`) != 0 {
		t.Fatal("purge retained expired raw state")
	}
	// A valid IdP callback cannot revive the removed transaction.
	res := f.request(p.client, "GET", f.allow(p, "expired-callback"), "", "")
	f.check(res, 302)
	if res.header.Get("Location") != "/signin?error=failed" {
		t.Fatal("callback revived an expired login")
	}
	// The per-peer limiter is bounded and the minute window uses injected time.
	var limits nativeLimits
	for range 20 {
		if !limits.allow("start:peer", f.now, 20) {
			t.Fatal("limit refused an eligible attempt")
		}
	}
	if limits.allow("start:peer", f.now, 20) || !limits.allow("token:peer", f.now, 60) || !limits.allow("start:peer", f.now.Add(time.Minute), 20) {
		t.Fatal("incorrect native rate window")
	}
	for i := range 4096 {
		if !limits.allow(fmt.Sprintf("peer:%d", i), f.now.Add(2*time.Minute), 1) {
			t.Fatal("limiter exhausted capacity early")
		}
	}
	if limits.allow("extra", f.now.Add(2*time.Minute), 1) {
		t.Fatal("limiter memory is unbounded")
	}
	// The start limit is shared across processes through transaction rows.
	if _, err := adminPool.Exec(t.Context(), `INSERT INTO native_login_transactions(tenant_id,tenant_slug,challenge_app,redirect_uri,issuing_origin,state_app,state_hash,created_at,expires_at)
		SELECT $1,'inspr','challenge',$2,'https://native.example','abandoned',decode(repeat('00',32),'hex'),$3,$4 FROM generate_series(1,120)`, f.tid, devicesessions.RedirectURI, f.now, f.now.Add(devicesessions.LoginTTL)); err != nil {
		t.Fatal(err)
	}
	start := url.Values{"tenant": {"inspr"}, "state": {p.state}, "code_challenge": {oauth2.S256ChallengeFromVerifier(p.verifier)}, "code_challenge_method": {"S256"}, "redirect_uri": {devicesessions.RedirectURI}}
	res = f.request(newHTTPClient(), "GET", "/api/auth/native/start?"+start.Encode(), "", "")
	f.check(res, 429)
	if res.header.Get("Retry-After") != "60" {
		t.Fatal("rate refusal omitted retry bound")
	}
}

// AEON-1731 risk: malformed native inputs trigger expensive work, unsafe
// redirects, HTTP-origin issuance, or cookie/agent-key scope changes.
func TestNativeLoginInputBounds(t *testing.T) {
	f := newNativeFixture(t)
	q := url.Values{"tenant": {"inspr"}, "state": {strings.Repeat("s", 22)}, "code_challenge": {oauth2.S256ChallengeFromVerifier(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}, "redirect_uri": {devicesessions.RedirectURI}}
	for _, name := range []string{"http-origin", "wrong-host", "state", "challenge", "plain", "redirect", "duplicate", "oversize"} {
		t.Run(name, func(t *testing.T) {
			in := url.Values{}
			for k, v := range q {
				in[k] = append([]string{}, v...)
			}
			prior := f.m.cfg.PublicURL
			defer func() { f.m.cfg.PublicURL = prior }()
			switch name {
			case "http-origin":
				f.m.cfg.PublicURL = "http://native.example"
			case "wrong-host":
				f.m.cfg.PublicURL = "https://wrong.example"
			case "state":
				in.Set("state", "small")
			case "challenge":
				in.Set("code_challenge", "short")
			case "plain":
				in.Set("code_challenge_method", "plain")
			case "redirect":
				in.Set("redirect_uri", "https://wrong.example")
			case "duplicate":
				in.Add("tenant", "other")
			case "oversize":
				in.Set("state", strings.Repeat("s", 3000))
			}
			res := f.request(newHTTPClient(), "GET", "/api/auth/native/start?"+in.Encode(), "", "")
			f.check(res, 400)
			if res.header.Get("Set-Cookie") != "" {
				t.Fatal("invalid native start set a cookie")
			}
		})
	}
	if scalar(t, adminPool, `SELECT count(*) FROM native_login_transactions`) != 0 {
		t.Fatal("invalid native starts wrote transaction rows")
	}
	for _, body := range []string{`{}`, `{"code":"x","state":"x"}`, strings.Repeat("x", 3000), `{} {}`, `{"unknown":true}`} {
		f.check(f.request(newHTTPClient(), "POST", "/api/auth/native/token", body, ""), 400)
	}
	for _, operation := range []string{"start", "token"} {
		limit := 20
		if operation == "token" {
			limit = 60
		}
		for i := 0; i <= limit; i++ {
			req := httptest.NewRequest("POST", "https://native.example/api/auth/native/"+operation, strings.NewReader(`{}`))
			req.RemoteAddr = "throttle-peer:1234"
			rec := httptest.NewRecorder()
			if operation == "start" {
				f.m.handleNativeStart(rec, req)
			} else {
				f.m.handleNativeToken(rec, req)
			}
			want := 400
			if i == limit {
				want = 429
			}
			if rec.Code != want {
				t.Fatalf("%s throttle: status=%d want=%d", operation, rec.Code, want)
			}
		}
	}
}
