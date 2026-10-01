// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/journal"
	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/intake"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const hostOrigin = "https://host.example"
const mockCredential = "synthetic-service-jwt-for-tests-only"

type fixture struct {
	t       *testing.T
	db      *dbtest.DB
	m       *Module
	p       tenant.Principal
	plugin  tenant.Principal
	project string
	s       Settings
	private ed25519.PrivateKey
	service *httptest.Server
	mux     *http.ServeMux
}

func newFixture(t *testing.T, handler http.HandlerFunc) *fixture {
	t.Helper()
	f := &fixture{t: t, db: dbtest.Open(t)}
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		}
	}
	f.service = httptest.NewServer(handler)
	t.Cleanup(f.service.Close)
	var tid string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('host','host') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		p    *tenant.Principal
		kind tenant.PrincipalKind
		role string
	}{{&f.p, tenant.Person, "owner"}, {&f.plugin, tenant.Agent, "member"}} {
		target.p.TenantID = tid
		target.p.Kind = target.kind
		if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,'host test') RETURNING id::text`, tid, string(target.kind)).Scan(&target.p.ID); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, f.db, tid, target.p.ID, target.role)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,body) SELECT $1,'HOST-1',id,'Host','' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&f.project); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, tid, f.project); err != nil {
		t.Fatal(err)
	}
	store, err := journal.NewStore(f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := tokens.New(t.Context(), &tokens.MemoryStore{TenantID: tid}, bytes.Repeat([]byte{3}, 32), tokens.Config{Issuer: hostOrigin, Audience: hostOrigin})
	if err != nil {
		t.Fatal(err)
	}
	f.m, err = New(f.db.App, store, keys, bytes.Repeat([]byte{5}, 32), hostOrigin)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.private = private
	script := sha256.Sum256([]byte("picker-test"))
	f.s = Settings{ServiceURL: f.service.URL, Location: "operator", PluginPrincipal: f.plugin.ID, PreviewKeys: tokens.JWKS{Keys: []tokens.JWK{{Type: "OKP", Curve: "Ed25519", Algorithm: "EdDSA", Use: "sig", ID: "preview-key", X: base64.RawURLEncoding.EncodeToString(public)}}}, PickerSHA256: base64.StdEncoding.EncodeToString(script[:]), Currency: "EUR", SessionCap: 1000, PrincipalDayCap: 10000, TenantDayCap: 100000}
	credential := secret(mockCredential)
	if _, err := f.m.saveSettings(f.ctx(), f.p, settingsWrite{Settings: f.s, ServiceJWT: &credential}); err != nil {
		t.Fatal(err)
	}
	reg, err := plugins.Builtin(Plugin)
	if err != nil {
		t.Fatal(err)
	}
	plugin, _ := Plugin()
	if _, err := plugins.NewWithRegistry(f.db.App, reg).Configure(f.ctx(), f.p, "aithema", plugins.InstallationWrite{ManifestDigestSHA256: plugin.Manifest.DigestSHA256, Enabled: true, Permissions: []string{"intake.read", "intake.write"}}); err != nil {
		t.Fatal(err)
	}
	f.mux = http.NewServeMux()
	f.m.Mount(f.mux)
	store.HostAuthorization = f.m.CheckJournal
	store.AuthorityProjection = f.m.CheckAuthority
	return f
}
func (f *fixture) ctx() context.Context { return tenant.WithPrincipal(f.t.Context(), f.p) }
func (f *fixture) call(method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Origin", hostOrigin)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func (f *fixture) session() sessionTokens {
	f.t.Helper()
	a := map[string]any{"contract": "aithema.authz", "major": 1, "minor": 0, "min_reader": 0, "tid": f.p.TenantID, "pid": f.project, "sid": newID(), "epoch": 1, "participants": []any{map[string]any{"participant_ref": f.p.ID, "role": "owner", "notice_ref": "notice-test"}}, "purposes": []string{"intake", "specification"}, "processors": []any{}, "settings_sha256": strings.Repeat("a", 64), "basis_label": "contract", "created_at": time.Now().UTC().Format(time.RFC3339Nano), "withdrawn_at": nil}
	w := f.call("POST", "/api/projects/"+f.project+"/aithema/sessions", map[string]any{"authorization": a, "host_mode": "review"})
	if w.Code != 202 {
		f.t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var out sessionTokens
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		f.t.Fatal("create response")
	}
	return out
}
func (f *fixture) deliver() {
	f.t.Helper()
	ok, err := f.m.DeliverOne(db.AllProjects(f.t.Context(), "host test delivery"), f.p.TenantID)
	if err != nil || !ok {
		f.t.Fatalf("delivery: %t %v", ok, err)
	}
}
func (f *fixture) refresh(sid string) sessionTokens {
	f.t.Helper()
	w := f.call("POST", "/api/projects/"+f.project+"/aithema/sessions/"+sid+"/tokens", map[string]any{})
	if w.Code != 200 {
		f.t.Fatalf("refresh: %d %s", w.Code, w.Body.String())
	}
	var out sessionTokens
	json.Unmarshal(w.Body.Bytes(), &out)
	return out
}
func (f *fixture) cap(sid, design string, mutate func(map[string]any)) string {
	now := time.Now().Unix()
	payload := map[string]any{"iss": f.s.ServiceURL, "aud": hostOrigin, "tid": f.p.TenantID, "sid": sid, "design_rev": design, "iat": now, "exp": now + 300}
	if mutate != nil {
		mutate(payload)
	}
	h, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "preview-key", "typ": "JWT"})
	b, _ := json.Marshal(payload)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(b)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.private, []byte(input)))
}

func TestSessionCreationRefusesNonObjectAuthorization(t *testing.T) {
	f := newFixture(t, nil)
	for _, authorization := range []any{nil, []any{}, "record"} {
		w := f.call("POST", "/api/projects/"+f.project+"/aithema/sessions", map[string]any{"authorization": authorization, "host_mode": "review"})
		if w.Code != 400 {
			t.Fatal("non-object authorization was not rejected")
		}
	}
	var sessions int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM aithema_sessions WHERE tenant_id=$1`, f.p.TenantID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("invalid authorization created a session")
	}
}

func TestSessionCreationNormalizesHostClockToContractPrecision(t *testing.T) {
	f := newFixture(t, nil)
	// Linux clocks provide nanoseconds; the pinned timestamp profile permits
	// at most six fractional digits. Exercise that precision on every OS.
	now := time.Date(2026, time.October, 1, 9, 0, 0, 123456789, time.FixedZone("host", 2*60*60))
	f.m.clock = func() time.Time { return now }
	s := f.session()
	state, err := f.m.Journal.Current(t.Context(), f.p.TenantID, f.project, s.Session)
	if err != nil {
		t.Fatal(err)
	}
	var authorization struct {
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(state.Authorization, &authorization); err != nil {
		t.Fatal(err)
	}
	if authorization.CreatedAt != "2026-10-01T07:00:00.123456Z" {
		t.Fatal("host timestamp did not use the contract's UTC microsecond profile")
	}
}

func TestSecretSettingsMaskedEncryptedAuditedAndRLS(t *testing.T) {
	f := newFixture(t, nil)
	w := f.call("GET", "/api/plugins/aithema/settings", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), mockCredential) || !strings.Contains(w.Body.String(), `"service_jwt":"********"`) {
		t.Fatal("secret read contract")
	}
	var config, cipher, events []byte
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT settings,service_credential FROM aithema_host_settings WHERE tenant_id=$1`, f.p.TenantID).Scan(&config, &cipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(config, []byte(mockCredential)) || bytes.Contains(cipher, []byte(mockCredential)) {
		t.Fatal("plaintext persisted")
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT after FROM events WHERE tenant_id=$1 AND type='aithema.settings_changed' LIMIT 1`, f.p.TenantID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(events, []byte(mockCredential)) || bytes.Contains(events, []byte(f.s.ServiceURL)) {
		t.Fatal("audit leaked values")
	}
	restarted, err := New(f.db.App, f.m.Journal, f.m.Keys, bytes.Repeat([]byte{5}, 32), hostOrigin)
	if err != nil {
		t.Fatal(err)
	}
	_, value, err := restarted.settings(f.ctx(), f.p.TenantID)
	if err != nil || string(value) != mockCredential {
		t.Fatal("restart lost credential")
	}
	if _, err := f.m.saveSettings(f.ctx(), f.p, settingsWrite{Settings: f.s}); err != nil {
		t.Fatal(err)
	}
	_, value, err = f.m.settings(f.ctx(), f.p.TenantID)
	if err != nil || string(value) != mockCredential {
		t.Fatal("omission did not preserve credential")
	}
	if !strings.Contains(fmt.Sprintf("%#v", secret(mockCredential)), "********") {
		t.Fatal("secret diagnostic not masked")
	}
	if raw, _ := json.Marshal(settingsWrite{Settings: f.s, ServiceJWT: func() *secret { s := secret(mockCredential); return &s }()}); bytes.Contains(raw, []byte(mockCredential)) {
		t.Fatal("write type serialized secret")
	}
	foreign := newID()
	if err := db.InTenant(t.Context(), f.db.App, foreign, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM aithema_host_settings`).Scan(&n)
		if n != 0 {
			t.Fatal("foreign settings visible")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	bad := f.s
	bad.ServiceURL = "https://user:pass@service.example"
	if _, err := f.m.saveSettings(f.ctx(), f.p, settingsWrite{Settings: bad}); err == nil {
		t.Fatal("userinfo accepted")
	}
	clear := secret("")
	if _, err := f.m.saveSettings(f.ctx(), f.p, settingsWrite{Settings: f.s, ServiceJWT: &clear}); err != nil {
		t.Fatal(err)
	}
	w = f.call("GET", "/api/plugins/aithema/settings", nil)
	if !strings.Contains(w.Body.String(), `"service_jwt_set":false`) {
		t.Fatal("clear failed")
	}
}

func TestCallbackRetryIdempotencyAndLifecycleAtomicity(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var ids []string
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+mockCredential {
			t.Error("callback service authentication missing")
		}
		mu.Lock()
		ids = append(ids, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	s := f.session()
	w := f.call("POST", "/api/projects/"+f.project+"/aithema/sessions/"+s.Session+"/tokens", nil)
	if w.Code != 409 {
		t.Fatal("tokens before creation")
	}
	f.deliver()
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE aithema_callbacks SET next_attempt_at=now() WHERE tenant_id=$1`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	f.deliver()
	pair := f.refresh(s.Session)
	if pair.Generation != 1 || pair.SessionToken == "" || pair.DelegatedToken == "" {
		t.Fatal("token pair missing")
	}
	mu.Lock()
	same := len(ids) == 2 && ids[0] == ids[1]
	mu.Unlock()
	if !same {
		t.Fatal("retry changed idempotency key")
	}
	path := "/api/projects/" + f.project + "/aithema/sessions/" + s.Session + "/control"
	key := newID()
	request := map[string]string{"action": "suspend", "idempotency_key": key}
	w = f.call("POST", path, request)
	if w.Code != 202 {
		t.Fatalf("suspend: %d %s", w.Code, w.Body.String())
	}
	first := w.Body.String()
	w = f.call("POST", path, request)
	if w.Code != 202 || w.Body.String() != first {
		t.Fatal("control retry not stable")
	}
	w = f.call("POST", path, map[string]string{"action": "purge", "idempotency_key": key})
	if w.Code != 409 {
		t.Fatal("changed retry accepted")
	}
	st, err := f.m.Journal.Current(t.Context(), f.p.TenantID, f.project, s.Session)
	if err != nil || !st.Suspended || st.Epoch != 1 {
		t.Fatal("suspend authority")
	}
	w = f.call("POST", path, map[string]string{"action": "resume", "idempotency_key": newID()})
	if w.Code != 202 {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	st, err = f.m.Journal.Current(t.Context(), f.p.TenantID, f.project, s.Session)
	if err != nil || st.Suspended || st.Generation != 2 {
		t.Fatal("resume generation")
	}
	w = f.call("POST", path, map[string]string{"action": "purge", "idempotency_key": newID()})
	if w.Code != 202 {
		t.Fatal("purge failed")
	}
	st, err = f.m.Journal.Current(t.Context(), f.p.TenantID, f.project, s.Session)
	if err != nil || !st.Tombstone || st.Epoch != 2 {
		t.Fatal("purge did not tombstone first")
	}
	// A callback insert failure rolls back the earlier host projection change.
	f2 := newFixture(t, nil)
	s2 := f2.session()
	f2.deliver()
	if _, err := f2.db.Admin.Exec(t.Context(), `ALTER TABLE aithema_callbacks ADD CONSTRAINT test_reject_suspend CHECK(operation<>'suspend')`); err != nil {
		t.Fatal(err)
	}
	w = f2.call("POST", "/api/projects/"+f2.project+"/aithema/sessions/"+s2.Session+"/control", map[string]string{"action": "suspend", "idempotency_key": newID()})
	if w.Code != 503 {
		t.Fatal("failed enqueue accepted")
	}
	st, err = f2.m.Journal.Current(t.Context(), f2.p.TenantID, f2.project, s2.Session)
	if err != nil || st.Suspended {
		t.Fatal("failed enqueue did not roll back journal")
	}
}

func TestPreviewExactHeadersCapsBrandsAndNoOracle(t *testing.T) {
	var upstream atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		if r.URL.Query().Get("resource") != "" {
			w.Header().Set("Content-Type", "text/css")
			w.Write([]byte("body{}"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Set-Cookie", "upstream=bad")
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.Write([]byte("<!doctype html><p>Brand</p>"))
	})
	s := f.session()
	server := &httpapi.Server{Modules: []httpapi.Module{f.m}, AithemaOrigin: hostOrigin}
	handler := server.Handler()
	design := strings.Repeat("a", 64)
	cap := f.cap(s.Session, design, nil)
	call := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	base := "/aithema/preview/" + design
	for _, path := range []string{base, base + "?cap=invalid", base + "?cap=" + url.QueryEscape(f.cap(s.Session, design, func(c map[string]any) { c["tid"] = newID() })), base + "?cap=" + url.QueryEscape(f.cap(s.Session, design, func(c map[string]any) { c["exp"] = time.Now().Unix() - 1 })), base + "?cap=" + url.QueryEscape(f.cap(s.Session, design, func(c map[string]any) { c["exp"] = time.Now().Unix() + 301 })), base + "?cap=" + url.QueryEscape(cap) + "&cap=" + url.QueryEscape(cap)} {
		w := call(path)
		if w.Code != 404 || w.Body.String() != "404 page not found\n" {
			t.Fatal("preview rejection differs")
		}
	}
	if upstream.Load() != 0 {
		t.Fatal("invalid cap reached upstream")
	}
	for _, d := range []string{design, strings.Repeat("b", 64)} {
		w := call("/aithema/preview/" + d + "?cap=" + url.QueryEscape(f.cap(s.Session, d, nil)))
		expected := "default-src 'none'; style-src 'self'; img-src data:; font-src data:; script-src 'sha256-" + f.s.PickerSHA256 + "'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'"
		if w.Code != 200 || w.Header().Get("Content-Security-Policy") != expected || w.Header().Get("X-Frame-Options") != "SAMEORIGIN" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("final preview policy: status %d", w.Code)
		}
	}
	w := call(base + "?resource=tokens.css&cap=" + url.QueryEscape(cap))
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatal("stylesheet cap route")
	}
	w = call("/p/HOST/journey")
	if w.Header().Get("Permissions-Policy") != "camera=(), microphone=(self), geolocation=()" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self' wss://host.example; frame-src 'self'") {
		t.Fatal("native view policy")
	}
	w = call("/portal/public")
	if w.Header().Get("Permissions-Policy") != "camera=(), microphone=(), geolocation=()" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("unrelated view policy changed")
	}
	w = call("/settings/workspace")
	if w.Header().Get("Permissions-Policy") != "camera=(), microphone=(self), geolocation=()" {
		t.Fatal("SPA navigation cannot enter voice intake")
	}
	if err := f.m.Journal.Revoke(t.Context(), f.p.TenantID, f.project, s.Session, "purge"); err != nil {
		t.Fatal(err)
	}
	w = call(base + "?cap=" + url.QueryEscape(cap))
	if w.Code != 404 {
		t.Fatal("purged cap usable")
	}
}

func TestLiveDelegationThroughRealAuthGenerationAndRemoval(t *testing.T) {
	f := newFixture(t, nil)
	s := f.session()
	f.deliver()
	pair := f.refresh(s.Session)
	authMod, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{8}, 32)}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	authMod.Delegated = f.m.DelegatedIntake
	server := &httpapi.Server{Modules: []httpapi.Module{intake.NewDelegated(f.db.App, f.m.Keys, f.m), &journal.Module{Store: f.m.Journal, Keys: f.m.Keys}}, Middleware: []func(http.Handler) http.Handler{authMod.Middleware}}
	handler := server.Handler()
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/projects/" + f.project + "/intake"
	w := call("GET", path, pair.DelegatedToken, "")
	if w.Code != 200 {
		t.Fatalf("live read: %d %s", w.Code, w.Body.String())
	}
	body := `{"kind":"note","label":"Source","content_sha256":"` + strings.Repeat("a", 64) + `","idempotency_key":"test"}`
	w = call("POST", path+"/sources", pair.DelegatedToken, body)
	if w.Code != 201 {
		t.Fatalf("live write: %d %s", w.Code, w.Body.String())
	}
	w = call("POST", path+"/drafts/"+newID()+"/accept", pair.DelegatedToken, `{}`)
	if w.Code != 403 {
		t.Fatal("delegated acceptance not refused")
	}
	w = call("GET", "/api/me", pair.DelegatedToken, "")
	if w.Code != 401 {
		t.Fatal("delegated token escaped exact route handoff")
	}
	if _, err := f.m.Journal.Takeover(t.Context(), f.p.TenantID, f.project, s.Session, 1); err != nil {
		t.Fatal(err)
	}
	w = call("POST", path+"/sources", pair.DelegatedToken, body)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "fenced_generation") {
		t.Fatal("stale write not fenced")
	}
	w = call("GET", path, pair.DelegatedToken, "")
	if w.Code != 200 {
		t.Fatal("read incorrectly fenced by generation")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE plugin_installations SET enabled=false WHERE tenant_id=$1 AND plugin_id='aithema'`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	w = call("GET", path, pair.DelegatedToken, "")
	if w.Code != 409 {
		t.Fatal("plugin removal did not revoke intake")
	}
	w = call("GET", "/api/aithema/journal/sessions/"+s.Session+"/cursor", pair.DelegatedToken, "")
	if w.Code != 409 {
		t.Fatalf("plugin removal journal: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/aithema/journal/sessions/"+s.Session+"/authority", pair.DelegatedToken, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"tombstone":true`) {
		t.Fatal("polling missed live plugin removal before reconciliation")
	}
	if err := f.m.reconcile(db.AllProjects(t.Context(), "host test reconciliation"), f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	state, err := f.m.Journal.Current(t.Context(), f.p.TenantID, f.project, s.Session)
	if err != nil || !state.Tombstone {
		t.Fatal("offboarding reconciliation did not tombstone")
	}
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_permission_grants WHERE tenant_id=$1`, f.p.TenantID).Scan(&n); err != nil || n != 0 {
		t.Fatal("delegation created persistent grant")
	}
}

func TestOutboundAddressPolicyAndNoRedirect(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "192.0.2.1", "fe80::1", "fc00::1", "::ffff:127.0.0.1"} {
		if publicIP(net.ParseIP(address)) {
			t.Fatalf("private/special address accepted: %s", address)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address refused")
	}
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	resp, err := serviceClient(Settings{Location: "operator"}).Get(redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if targetCalls.Load() != 0 || resp.StatusCode != 302 {
		t.Fatal("redirect followed")
	}
	if _, err := serviceClient(Settings{Location: "cloud"}).Get(target.URL); err == nil {
		t.Fatal("cloud transport allowed loopback")
	}
}

func TestCallbackPermanentFailureBoundAndLeaseRecovery(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
	s := f.session()
	for range 6 {
		f.deliver()
		if _, err := f.db.Admin.Exec(t.Context(), `UPDATE aithema_callbacks SET next_attempt_at=now() WHERE tenant_id=$1`, f.p.TenantID); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := f.m.DeliverOne(t.Context(), f.p.TenantID); err != nil || ok {
		t.Fatal("retry bound exceeded")
	}
	w := f.call("GET", "/api/aithema/callbacks/"+s.Callback, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"failed"`) || !strings.Contains(w.Body.String(), `"attempts":6`) {
		t.Fatal("terminal delivery evidence")
	}
	if calls.Load() != 6 {
		t.Fatal("unexpected callback sends")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE aithema_callbacks SET state='pending',lease_until=now()-interval '1 second' WHERE tenant_id=$1`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.m.DeliverOne(t.Context(), f.p.TenantID); err != nil || ok {
		t.Fatal("last-attempt crash retried")
	}
	// No upstream bodies or credential values occur in status replies.
	if strings.Contains(w.Body.String(), mockCredential) {
		t.Fatal("status leaked credential")
	}
}
