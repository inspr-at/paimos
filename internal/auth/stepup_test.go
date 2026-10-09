// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"github.com/inspr-at/paimos/internal/stepup/server"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: fallback accepts an old/provider-claimed login without validating
// signed JWT, state, nonce, PKCE, current browser person and OIDC subject.
func TestStepupOIDCFallbackUsesExistingIdentityAndFreshVerifiedAuthTime(t *testing.T) {
	for _, scenario := range []string{"missing auth_time", "old auth_time", "wrong subject", "wrong nonce", "valid"} {
		t.Run(scenario, func(t *testing.T) {
			reset(t)
			tid := insertTenant(t, "inspr", "INSPR")
			issuer := startFakeOIDC(t, "aeon-public")
			mod := newMod(t, Config{Env: envDev, OIDCIssuer: issuer.issuer, OIDCClientID: "aeon-public", BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
			mux := http.NewServeMux()
			mod.Mount(mux)
			native := stepup.New(appPool, phoneapprovals.New(appPool, nil, "http://localhost", nil, nil), "")
			native.Reauthenticate = mod.BeginStepUp
			mod.StepUp = native
			native.Mount(mux)
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, r.Pattern = mux.Handler(r)
				mod.Middleware(mux).ServeHTTP(w, r)
			}))
			t.Cleanup(app.Close)
			mod.cfg.PublicURL = app.URL
			// Use the real cookie middleware and its existing authorization-code flow.
			c := newHTTPClient()
			login, err := c.Get(app.URL + "/api/auth/login")
			if err != nil {
				t.Fatal(err)
			}
			login.Body.Close()
			q := assertAuthURL(t, app.URL, login.Header.Get("Location"))
			issuer.allow("initial", q.Get("code_challenge"), q.Get("nonce"), "owner-subject", "admin@example.com", "Owner", false)
			status, _, _ := do(t, c, "GET", callbackURL(app.URL, "initial", q.Get("state")), "", nil)
			if status != 302 {
				t.Fatalf("initial callback %d", status)
			}
			agent := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Scopes: []string{"approvals.request", "nodes.read"}}
			if err := adminPool.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name)VALUES($1,'agent','Lead')RETURNING id::text`, tid).Scan(&agent.ID); err != nil {
				t.Fatal(err)
			}
			dbtest.BindRole(t, testDB, tid, agent.ID, "admin")
			before := json.RawMessage(`{"key":"workspace-summary","project_id":null,"override":null,"revision":0}`)
			request, err := native.Create(t.Context(), agent, stepup.Create{Payload: json.RawMessage(`{"kind":"feature","key":"workspace-summary","enabled":true,"expected_revision":0}`), BeforeHash: stepup.Hash(before)})
			if err != nil {
				t.Fatal(err)
			}
			// Every holder of the target permission is eligible, including a
			// minimal custom role that grants no unrelated profile permission.
			var roleID string
			if err := adminPool.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'stepup_only','Protected change only') RETURNING id::text`, tid).Scan(&roleID); err != nil {
				t.Fatal(err)
			}
			if _, err := adminPool.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'settings.manage')`, tid, roleID); err != nil {
				t.Fatal(err)
			}
			if _, err := adminPool.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE tenant_id=$1 AND principal_id IN (SELECT p.id FROM principals p JOIN identities i ON i.id=p.identity_id WHERE p.tenant_id=$1 AND i.subject='owner-subject') AND scope_type='workspace'`, tid, roleID); err != nil {
				t.Fatal(err)
			}
			// Mount a native module at this test origin; no configuration credential
			// changes or additional OIDC client are needed.
			native = stepup.New(appPool, phoneapprovals.New(appPool, nil, app.URL, nil, nil), app.URL)
			native.Reauthenticate = mod.BeginStepUp
			mod.StepUp = native
			nativeMux := http.NewServeMux()
			native.Mount(nativeMux)
			body, _ := json.Marshal(stepup.Decide{Digest: request.Digest, Revision: request.Revision})
			// options through the same middleware, with the authenticated browser cookie.
			optionsReq := httptest.NewRequest("POST", app.URL+"/api/stepup-requests/"+request.ID+"/options", strings.NewReader(string(body)))
			parsed, _ := url.Parse(app.URL)
			for _, cookie := range c.Jar.Cookies(parsed) {
				optionsReq.AddCookie(cookie)
			}
			optionsReq.Header.Set("Origin", app.URL)
			_, optionsReq.Pattern = nativeMux.Handler(optionsReq)
			optionsReply := httptest.NewRecorder()
			mod.Middleware(nativeMux).ServeHTTP(optionsReply, optionsReq)
			var result struct {
				Method string `json:"method"`
				URL    string `json:"authorize_url"`
			}
			if optionsReply.Code != 200 || json.Unmarshal(optionsReply.Body.Bytes(), &result) != nil || result.Method != "oidc_reauth" {
				t.Fatalf("fallback %d %s", optionsReply.Code, optionsReply.Body.String())
			}
			c.Jar.SetCookies(parsed, optionsReply.Result().Cookies())
			authURL, err := url.Parse(result.URL)
			if err != nil {
				t.Fatal(err)
			}
			q = authURL.Query()
			if q.Get("prompt") != "login" || q.Get("max_age") != "0" || q.Get("code_challenge_method") != "S256" {
				t.Fatal("not a fresh bound sign-in")
			}
			subject := "owner-subject"
			if scenario == "wrong subject" {
				subject = "other-subject"
			}
			issuer.allow("fresh", q.Get("code_challenge"), q.Get("nonce"), subject, "admin@example.com", "Owner", scenario == "wrong nonce")
			issuer.mu.Lock()
			pending := issuer.codes["fresh"]
			switch scenario {
			case "missing auth_time":
			case "old auth_time":
				pending.authTime = time.Now().Add(-time.Hour).Unix()
			default:
				pending.authTime = time.Now().Unix() + 1
			}
			issuer.codes["fresh"] = pending
			issuer.mu.Unlock()
			_, _, response := do(t, c, "GET", callbackURL(app.URL, "fresh", q.Get("state")), "", nil)
			out, err := native.Get(t.Context(), agent, request.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "valid" {
				if response.Header.Get("Location") != "/decision-desk?needs=s:"+request.ID || out.State != "applied" || out.Method == nil || *out.Method != "oidc_reauth" {
					t.Fatalf("OIDC did not apply %+v", out)
				}
			} else if out.State != "pending" || !strings.HasPrefix(response.Header.Get("Location"), "/signin?error=") {
				t.Fatalf("bad OIDC accepted %+v", out)
			}
		})
	}
}

func TestStepupAgentRouteCeiling(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{"POST", "/api/stepup-requests", "approvals.request"},
		{"GET", "/api/stepup-requests", "approvals.request"},
		{"GET", "/api/stepup-requests/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "approvals.request"},
		{"POST", "/api/stepup-requests/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/withdraw", "approvals.request"},
		{"POST", "/api/stepup-requests/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/approve", ""},
		{"POST", "/api/stepup-requests/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/decline", ""},
		{"POST", "/api/stepup-requests/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/options", ""},
	} {
		got, _ := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if got != tc.want {
			t.Fatalf("%s %s: %q", tc.method, tc.path, got)
		}
	}
}
