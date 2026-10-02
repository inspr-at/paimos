// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestMeDevMode(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	pid, iid := signinPerson(t, tid, "person", "Person", "person@example.com", "person@example.com", "member")
	for _, environment := range []string{"dev", "prod", "test", "", "DEV"} {
		t.Run("env="+environment, func(t *testing.T) {
			mod := newMod(t, Config{Env: environment})
			app := startApp(t, mod)
			for _, hdr := range []http.Header{nil, {"Cookie": {"aeon_session=invalid"}}, {"Authorization": {"Bearer invalid"}}} {
				status, body, res := do(t, newHTTPClient(), http.MethodGet, app.URL+"/api/me", "", hdr)
				var got struct {
					Error   string `json:"error"`
					DevMode *bool  `json:"dev_mode"`
				}
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if status != 401 || got.Error != "unauthorized" || got.DevMode == nil || *got.DevMode != (environment == "dev") || res.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("unauthorized response %d %s", status, body)
				}
			}
			token, err := mod.startSession(t.Context(), iid, tid, pid)
			if err != nil {
				t.Fatal(err)
			}
			status, body, _ := do(t, newHTTPClient(), http.MethodGet, app.URL+"/api/me", "", http.Header{"Cookie": {"aeon_session=" + token}})
			var got struct {
				DevMode *bool `json:"dev_mode"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if status != 200 || got.DevMode == nil || *got.DevMode != (environment == "dev") {
				t.Fatalf("authenticated response %d %s", status, body)
			}
		})
	}
}

// Fixtures use the production tenant boundary and append creation events.
func signinPerson(t *testing.T, tid, subject, name, email, identityEmail, role string) (string, string) {
	t.Helper()
	var pid, iid string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,email,display_name) VALUES('test-signin',$1,$2,$3) RETURNING id::text`, subject, identityEmail, name).Scan(&iid); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,identity_id,name,email,roles) VALUES($1,'person',$2,$3,$4,ARRAY[$5]::text[]) RETURNING id::text`, tid, iid, name, email, role).Scan(&pid); err != nil {
			return err
		}
		_, err := events.Append(t.Context(), tx, tenant.Principal{ID: pid, TenantID: tid}, events.Change{Type: "tenant.principal_bound", After: map[string]any{"principal_id": pid, "identity_id": iid}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return pid, iid
}

func TestLinkedSigninProfile(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	other := insertTenant(t, "other", "Other")
	source, sourceIdentity := signinPerson(t, tid, "imported", "Old imported name", "same@example.com", "old@example.com", "member")
	target, targetIdentity := signinPerson(t, tid, "canonical", "Canonical name", "same@example.com", "provider@example.com", "admin")
	outside, _ := signinPerson(t, other, "outside", "Outside tenant", "same@example.com", "outside@example.com", "admin")
	if _, err := principallink.New(appPool).Link(t.Context(), "inspr", source, target); err != nil {
		t.Fatal(err)
	}
	mod := newMod(t, Config{Env: envDev})
	app := startApp(t, mod)
	token, err := mod.startSession(t.Context(), sourceIdentity, tid, source)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodeSessionToken(token)
	if err != nil {
		t.Fatal(err)
	}
	principal, ok, err := mod.authenticateSession(t.Context(), raw)
	if err != nil || !ok || principal.ID != source || principal.Name != "Canonical name" || len(principal.Roles) != 1 || principal.Roles[0] != "member" {
		t.Fatalf("session authority/profile: %+v %v %v", principal, ok, err)
	}
	hdr := http.Header{"Cookie": {"aeon_session=" + token}}
	status, body, _ := do(t, newHTTPClient(), http.MethodGet, app.URL+"/api/me", "", hdr)
	var me meJSON
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatal(err)
	}
	if status != 200 || me.Principal.ID != source || me.Principal.Name != "Canonical name" || deref(me.Principal.Email) != "same@example.com" || me.Identity == nil || me.Identity.ID != sourceIdentity || deref(me.Identity.Email) != "same@example.com" || deref(me.Identity.DisplayName) != "Canonical name" || me.Principal.Roles[0] != "member" {
		t.Fatalf("linked me %d %s", status, body)
	}
	// Both persons share the email; the older imported source must not win.
	c := newHTTPClient()
	status, body, _ = do(t, c, http.MethodPost, app.URL+"/api/auth/dev-login", `{"email":"SAME@example.com"}`, nil)
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatal(err)
	}
	if status != 200 || me.Principal.ID != target || me.Identity == nil || me.Identity.ID != targetIdentity || !me.DevMode {
		t.Fatalf("canonical dev login %d %s", status, body)
	}
	status, body, _ = do(t, c, http.MethodGet, app.URL+"/api/me", "", nil)
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatal(err)
	}
	if status != 200 || me.Principal.ID != target {
		t.Fatalf("canonical session %d %s", status, body)
	}
	status, body, _ = do(t, newHTTPClient(), http.MethodPost, app.URL+"/api/auth/dev-login", `{"email":"same@example.com","tenant":"other"}`, nil)
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatal(err)
	}
	if status != 200 || me.Principal.ID != outside || me.Tenant.ID != other {
		t.Fatalf("tenant isolation %d %s", status, body)
	}
	// Existing sessions pick up unlinking without being recreated.
	if _, err := principallink.New(appPool).Unlink(t.Context(), "inspr", source); err != nil {
		t.Fatal(err)
	}
	status, body, _ = do(t, newHTTPClient(), http.MethodGet, app.URL+"/api/me", "", hdr)
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatal(err)
	}
	if status != 200 || me.Principal.ID != source || me.Principal.Name != "Old imported name" || me.Identity == nil || deref(me.Identity.DisplayName) != "Old imported name" {
		t.Fatalf("unlinked session %d %s", status, body)
	}
}

type signinLog struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *signinLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *signinLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }

func TestCallbackFailureRedirects(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	issuer := startFakeOIDC(t, "aeon-public")
	for _, tc := range []struct{ name, code, reason string }{
		{"denied", "denied", "provider_denied"},
		{"server_error", "unavailable", "provider_unavailable"},
		{"temporarily_unavailable", "unavailable", "provider_unavailable"},
		{"unknown_error", "failed", "provider_error"},
		{"missing_cookie", "expired", "invalid_state_cookie"},
		{"expired_cookie", "expired", "invalid_state_cookie"},
		{"state_mismatch", "expired", "invalid_state_cookie"},
		{"missing_code", "failed", "missing_code"},
		{"discovery", "unavailable", "provider_discovery"},
		{"exchange", "failed", "token_exchange"},
		{"token_unavailable", "unavailable", "token_endpoint_unavailable"},
		{"nonce", "expired", "nonce_mismatch"},
		{"not_member", "not_member", "tenant_membership"},
		{"imported_account", "imported_account", "tenant_membership"},
		{"invalid_token", "failed", "id_token_verification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &signinLog{}
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
			defer slog.SetDefault(previous)
			mod := newMod(t, Config{Env: envDev, OIDCIssuer: issuer.issuer, OIDCClientID: "aeon-public"})
			app := httptest.NewServer((&httpapi.Server{Modules: []httpapi.Module{mod}, Middleware: []func(http.Handler) http.Handler{mod.Middleware}}).Handler())
			defer app.Close()
			mod.cfg.PublicURL = app.URL
			payload := oidcPayload{State: "state", Nonce: "nonce", Verifier: oauth2.GenerateVerifier(), Tenant: "inspr", Exp: time.Now().Add(time.Minute).Unix()}
			q := url.Values{"code": {tc.name}, "state": {payload.State}, "error_description": {"untrusted-provider-detail"}}
			switch tc.name {
			case "denied":
				q.Set("error", "access_denied")
			case "server_error", "temporarily_unavailable":
				q.Set("error", tc.name)
			case "unknown_error":
				q.Set("error", "untrusted-provider-detail")
			case "expired_cookie":
				payload.Exp = time.Now().Add(-time.Minute).Unix()
			case "state_mismatch":
				q.Set("state", "wrong")
			case "missing_code":
				q.Del("code")
			case "discovery":
				mod.cfg.OIDCIssuer = ""
			case "nonce", "not_member", "imported_account":
				if tc.name == "imported_account" {
					if err := db.InTenant(dbtest.Seed(t.Context()), appPool, tid, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name,email) VALUES($1::uuid,'person','Classic Stranger','stranger@example.com')`, tid)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				issuer.allow(tc.name, oauth2.S256ChallengeFromVerifier(payload.Verifier), payload.Nonce, "stranger", "stranger@example.com", "Stranger", tc.name == "nonce")
			case "token_unavailable", "invalid_token":
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tc.name == "token_unavailable" {
						writeFakeJSON(w, 503, map[string]string{"error": "temporarily_unavailable", "error_description": "untrusted-provider-detail"})
					} else {
						writeFakeJSON(w, 200, map[string]string{"access_token": "test-access", "token_type": "Bearer", "id_token": "invalid"})
					}
				}))
				defer endpoint.Close()
				if _, _, err := mod.oidcProvider(t.Context()); err != nil {
					t.Fatal(err)
				}
				mod.oauth.Endpoint.TokenURL = endpoint.URL
			}
			sealed, err := mod.seal(payload)
			if err != nil {
				t.Fatal(err)
			}
			hdr := http.Header{"X-Request-ID": {"b6-" + tc.name}}
			if tc.name != "missing_cookie" {
				hdr.Set("Cookie", oidcCookieName+"="+sealed)
			}
			status, body, res := do(t, newHTTPClient(), http.MethodGet, app.URL+"/api/auth/callback?"+q.Encode(), "", hdr)
			if status != 302 || res.Header.Get("Location") != "/signin?error="+tc.code || res.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("redirect %d %q", status, res.Header.Get("Location"))
			}
			cleared := false
			for _, cookie := range res.Cookies() {
				if cookie.Name == oidcCookieName && cookie.MaxAge < 0 {
					cleared = true
				}
				if cookie.Name == sessionCookieName && cookie.MaxAge > 0 {
					t.Fatal("failed callback issued a session")
				}
			}
			if !cleared {
				t.Fatal("OIDC cookie retained")
			}
			text := logs.String()
			requestID := res.Header.Get("X-Request-ID")
			if requestID == "" || requestID == "b6-"+tc.name || !strings.Contains(text, `"reason":"`+tc.reason+`"`) || !strings.Contains(text, `"request_id":"`+requestID+`"`) || strings.Contains(text, `"request_id":"b6-`+tc.name+`"`) {
				t.Fatalf("missing correlated reason: %s", text)
			}
			if strings.Contains(text, "untrusted-provider-detail") || strings.Contains(string(body), "untrusted-provider-detail") {
				t.Fatal("provider text exposed")
			}
		})
	}
}

func TestLinkedIdentityEmailFallback(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	source, _ := signinPerson(t, tid, "old", "Old", "", "same@example.com", "member")
	target, identity := signinPerson(t, tid, "new", "New", "", "same@example.com", "admin")
	if _, err := principallink.New(appPool).Link(t.Context(), "inspr", source, target); err != nil {
		t.Fatal(err)
	}
	mod := newMod(t, Config{Env: envDev})
	principal, iid, err := mod.personByEmail(t.Context(), tid, "same@example.com")
	if err != nil || principal.ID != target || iid != identity {
		t.Fatalf("identity email preference: %s %s %v", principal.ID, iid, err)
	}
	view, err := mod.loadMe(t.Context(), tenant.Principal{ID: source, TenantID: tid})
	if err != nil || deref(view.Email) != "same@example.com" || view.Identity == nil || deref(view.Identity.Email) != "same@example.com" {
		t.Fatalf("identity email fallback: %+v %v", view, err)
	}
}
