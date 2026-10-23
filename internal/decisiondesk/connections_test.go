// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: a sibling credential broadens a person's live Desk visibility, leaks
// context/answers or another person's items, can be replayed, survives expiry
// or revocation, or authorizes cookies/agent keys or a non-configured origin.
func TestPersonConnectionsScopeExpiryAndRevocation(t *testing.T) {
	f := setup(t)
	sibling := config.Sibling{Tag: "pma", Origin: "https://pma.example"}
	f.m.WithInstances(config.Instances{Here: "ppm", Siblings: []config.Sibling{sibling}})
	owner, member := f.person, f.reader
	owner.BrowserSession, member.BrowserSession = true, true
	// Risk: an unauthenticated summary request queues behind the tenant's
	// writers (or turns an unknown tenant into a 500). Acquiring the fence is
	// the barrier: it stays held until each response has completed. The request
	// timeout only prevents a broken implementation from hanging the test.
	t.Run("invalid credentials never wait for the tenant fence", func(t *testing.T) {
		holder, err := f.d.Admin.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Rollback(t.Context())
		if err := db.LockTenant(t.Context(), holder, owner.TenantID); err != nil {
			t.Fatal(err)
		}
		for _, tid := range []string{owner.TenantID, "00000000-0000-4000-8000-000000000001"} {
			t.Run(tid, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				r := httptest.NewRequest("GET", "/api/decision-desk/owner-summary", nil).WithContext(ctx)
				r.Header.Set("Origin", sibling.Origin)
				r.Header.Set("Authorization", "Bearer desk_"+tid+"_"+owner.ID+"_"+strings.Repeat("a", 43))
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "connection ended") {
					t.Fatalf("invalid credential behind held fence: %d %s", w.Code, w.Body.String())
				}
			})
		}
	})
	q := f.ask(t, f.project, "carries_on", nil)
	other := f.ask(t, f.otherProject, "carries_on", nil)
	// The member can read only one project. Own-person manual steps make the
	// recipient distinction observable even for two people sharing a project.
	f.exec(t, `UPDATE nodes SET human_check='Only this person',fields=jsonb_set(fields,'{assignee}',to_jsonb($2::text)),state='done' WHERE id=$1`, f.ticket, f.person.ID)
	request := func(p tenant.Principal, method, path string, body any, origin, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), p))
		if origin == "" && (method == "POST" || method == "DELETE") {
			origin = "http://example.com"
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	issue := func(p tenant.Principal, state string) (string, string) {
		t.Helper()
		w := request(p, "POST", "/api/decision-desk/connections", map[string]string{"origin": sibling.Origin, "state": state, "code_challenge": testPKCEChallenge()}, "", "")
		if w.Code != 201 {
			t.Fatalf("connect: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			URL string `json:"redirect_url"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("connect response shape")
		}
		u, err := url.Parse(out.URL)
		if err != nil || u.Scheme+"://"+u.Host != sibling.Origin || u.Path != "/instances/connected" || u.RawQuery != "" || u.User != nil {
			t.Fatal("unsafe return address")
		}
		fragment, err := url.ParseQuery(u.Fragment)
		if err != nil || fragment.Get("state") != state || fragment.Get("instance") != "ppm" || fragment.Get("code") == "" || fragment.Has("token") {
			t.Fatal("callback must carry a code and bound state, never a bearer")
		}
		exchange := request(tenant.Principal{}, "POST", "/api/decision-desk/connections/exchange", map[string]string{"code": fragment.Get("code"), "state": state, "code_verifier": strings.Repeat("v", 43)}, sibling.Origin, "")
		var credential struct {
			Token     string    `json:"token"`
			CreatedAt time.Time `json:"created_at"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		if exchange.Code != 200 || json.Unmarshal(exchange.Body.Bytes(), &credential) != nil || credential.Token == "" {
			t.Fatalf("exchange: %d %s", exchange.Code, exchange.Body.String())
		}
		var rows struct {
			Connections []Connection `json:"connections"`
		}
		list := request(p, "GET", "/api/decision-desk/connections", nil, "", "")
		if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &rows) != nil || len(rows.Connections) == 0 {
			t.Fatalf("list: %d", list.Code)
		}
		for _, c := range rows.Connections {
			if c.CreatedAt.Equal(credential.CreatedAt) && c.ExpiresAt.Equal(credential.ExpiresAt) {
				return credential.Token, c.ID
			}
		}
		t.Fatal("issued connection absent")
		return "", ""
	}

	// The same entry rule as the home Desk includes a custom settings-only role.
	settingsPerson := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person, BrowserSession: true}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Settings-only person') RETURNING id::text`, owner.TenantID).Scan(&settingsPerson.ID); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'settings_only_desk','Settings-only Desk') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'settings.manage')`, owner.TenantID, role)
	f.exec(t, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, settingsPerson.ID, role)
	settingsToken, _ := issue(settingsPerson, strings.Repeat("z", 43))
	settingsPage := f.page(t, settingsPerson, 5, nil)
	settingsRead := request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary", nil, sibling.Origin, settingsToken)
	var settingsSummary OwnerSummary
	if settingsRead.Code != 200 || json.Unmarshal(settingsRead.Body.Bytes(), &settingsSummary) != nil || settingsSummary.Counts.Open != settingsPage.Counts.Open || len(settingsSummary.Items) != len(settingsPage.Items) {
		t.Fatal("custom person role differs from its home Desk")
	}
	state := strings.Repeat("a", 43)
	token, id := issue(member, state) // No owner role on this person.
	hash := sha256.Sum256([]byte(token))
	var stored []byte
	var lifespan time.Duration
	var seconds float64
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT token_hash,extract(epoch FROM expires_at-created_at)::float8 FROM desk_instance_connections WHERE tenant_id=$1 AND id=$2`, member.TenantID, id).Scan(&stored, &seconds); err != nil {
		t.Fatal(err)
	}
	lifespan = time.Duration(seconds) * time.Second
	if !bytes.Equal(stored, hash[:]) || bytes.Equal(stored, []byte(token)) || lifespan != 90*24*time.Hour {
		t.Fatal("grant not hashed or lifetime differs from 90 days")
	}
	if w := request(member, "POST", "/api/decision-desk/connections", map[string]string{"origin": sibling.Origin, "state": state, "code_challenge": testPKCEChallenge()}, "", ""); w.Code != 409 {
		t.Fatalf("state reused: %d", w.Code)
	}
	for _, origin := range []string{"https://evil.example", sibling.Origin + "/", sibling.Origin + ".evil.example"} {
		if w := request(member, "POST", "/api/decision-desk/connections", map[string]string{"origin": origin, "state": strings.Repeat("b", 43), "code_challenge": testPKCEChallenge()}, "", ""); w.Code != 403 {
			t.Fatalf("origin accepted: %d", w.Code)
		}
	}
	w := request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary", nil, sibling.Origin, token)
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != sibling.Origin || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("summary: %d %s", w.Code, w.Body.String())
	}
	var summary OwnerSummary
	if json.Unmarshal(w.Body.Bytes(), &summary) != nil {
		t.Fatal("summary shape")
	}
	page := f.page(t, member, 5, nil)
	if summary.Counts.Open != page.Counts.Open || len(summary.Items) != len(page.Items) || !slices.ContainsFunc(summary.Items, func(i SummaryItem) bool { return i.ID == q.ID }) {
		t.Fatal("summary differs from member's own Desk")
	}
	if slices.ContainsFunc(summary.Items, func(i SummaryItem) bool { return i.ID == other.ID || i.Kind == "action_request" }) {
		t.Fatal("summary disclosed another project or person's manual step")
	}
	for _, forbidden := range []string{"context", "findings", "options", "answer", "token_hash", "state_hash"} {
		if strings.Contains(w.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("summary disclosed %s", forbidden)
		}
	}

	// Show all expands first lines only and retains exactly the home Desk's scope.
	for range 6 {
		f.ask(t, f.project, "carries_on", nil)
	}
	full := f.page(t, member, 100, nil)
	w = request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary?limit=100", nil, sibling.Origin, token)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &summary) != nil || len(summary.Items) != len(full.Items) || summary.Counts.Open != full.Counts.Open {
		t.Fatal("expanded summary differs from home Desk")
	}
	w = request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary", nil, sibling.Origin, token)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &summary) != nil || len(summary.Items) != 5 || summary.Counts.Open != full.Counts.Open {
		t.Fatal("default summary is not bounded to five first lines")
	}
	if w = request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary?limit=101", nil, sibling.Origin, token); w.Code != 400 {
		t.Fatal("summary limit was not bounded before work")
	}
	for _, origin := range []string{"", "https://evil.example"} {
		w = request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary", nil, origin, token)
		if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("unconfigured origin: %d", w.Code)
		}
	}
	for _, p := range []tenant.Principal{f.agent, func() tenant.Principal { p := owner; p.KeyCreatorID = owner.ID; return p }(), func() tenant.Principal { p := owner; p.BrowserSession = false; return p }()} {
		if w = request(p, "POST", "/api/decision-desk/connections", map[string]string{"origin": sibling.Origin, "state": strings.Repeat("c", 43), "code_challenge": testPKCEChallenge()}, "", ""); w.Code != 403 {
			t.Fatalf("non-browser person connected: %d", w.Code)
		}
	}
	// Revoking a different person's row is idempotent and cannot end it.
	if w = request(owner, "DELETE", "/api/decision-desk/connections/"+id, nil, "", ""); w.Code != 204 {
		t.Fatal("revoke-other response")
	}
	if w = request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary", nil, sibling.Origin, token); w.Code != 200 {
		t.Fatal("another person revoked the member grant")
	}
	if w = request(member, "DELETE", "/api/decision-desk/connections/"+id, nil, "", ""); w.Code != 204 {
		t.Fatal("revoke failed")
	}
	if w = request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary", nil, sibling.Origin, token); w.Code != 401 {
		t.Fatalf("revoked grant: %d", w.Code)
	}
	token, id = issue(member, strings.Repeat("d", 43))
	f.exec(t, `UPDATE desk_instance_connections SET expires_at=now()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, member.TenantID, id)
	if w = request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary", nil, sibling.Origin, token); w.Code != 401 {
		t.Fatalf("expired grant: %d", w.Code)
	}
	token, _ = issue(member, strings.Repeat("e", 43))
	// A new read transaction starts only after last_used_at has committed. Hold
	// the tenant fence from that boundary through the response: a legitimate
	// summary must read without reacquiring it. NOWAIT rejects a retained fence
	// for the exact lock reason rather than relying on elapsed time.
	t.Run("summary snapshot releases the write fence", func(t *testing.T) {
		var holder pgx.Tx
		hash := sha256.Sum256([]byte(token))
		defer func() {
			if holder != nil {
				_ = holder.Rollback(t.Context())
			}
		}()
		ctx := db.WithTenantGuard(t.Context(), func(ctx context.Context, tx pgx.Tx, tid string) error {
			var used bool
			err := f.d.Admin.QueryRow(ctx, `SELECT last_used_at IS NOT NULL FROM desk_instance_connections WHERE tenant_id=$1 AND token_hash=$2`, tid, hash[:]).Scan(&used)
			if err != nil || !used || holder != nil {
				return err
			}
			holder, err = f.d.Admin.Begin(ctx)
			if err != nil {
				return err
			}
			_, err = holder.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, tid)
			return err
		})
		r := httptest.NewRequest("GET", "/api/decision-desk/owner-summary", nil).WithContext(ctx)
		r.Header.Set("Origin", sibling.Origin)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK || holder == nil {
			t.Fatalf("summary did not read independently of its write fence: %d %s (read barrier reached: %t)", w.Code, w.Body.String(), holder != nil)
		}
	})
	token, id = issue(member, strings.Repeat("f", 43))
	t.Run("revocation during the snapshot discards the summary", func(t *testing.T) {
		revoked := false
		ctx := db.WithTenantGuard(t.Context(), func(ctx context.Context, tx pgx.Tx, tid string) error {
			var used bool
			if err := f.d.Admin.QueryRow(ctx, `SELECT last_used_at IS NOT NULL FROM desk_instance_connections WHERE tenant_id=$1 AND id=$2`, tid, id).Scan(&used); err != nil {
				return err
			}
			if used && !revoked {
				// The read snapshot is established at transaction entry. Revoke
				// through the real write path after that barrier, with no sleeps.
				w := request(member, "DELETE", "/api/decision-desk/connections/"+id, nil, "", "")
				if w.Code != http.StatusNoContent {
					t.Fatalf("revoke during summary: %d %s", w.Code, w.Body.String())
				}
				revoked = true
			}
			return nil
		})
		r := httptest.NewRequest("GET", "/api/decision-desk/owner-summary", nil).WithContext(ctx)
		r.Header.Set("Origin", sibling.Origin)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if !revoked || w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "connection ended") {
			t.Fatalf("revocation did not discard the in-flight summary: %d %s (revocation barrier reached: %t)", w.Code, w.Body.String(), revoked)
		}
	})
	token, _ = issue(member, strings.Repeat("g", 43))
	// Commit an access change exactly between initial visibility setup and the
	// handler's fence. This proves a pre-fence repeatable-read snapshot cannot
	// keep old authority alive, without sleeps or a wall-clock interleaving.
	ctx := db.WithTenantGuard(t.Context(), func(ctx context.Context, tx pgx.Tx, tid string) error {
		_, err := f.d.Admin.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tid, member.ID)
		return err
	})
	r := httptest.NewRequest("GET", "/api/decision-desk/owner-summary", nil).WithContext(ctx)
	r.Header.Set("Origin", sibling.Origin)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("permission loss: %d", w.Code)
	}
}

func TestSummaryPreflightAllowsOnlyConfiguredReadOriginsAndHeaders(t *testing.T) {
	m := New(nil).WithInstances(config.Instances{Siblings: []config.Sibling{{Tag: "pma", Origin: "https://pma.example"}}})
	for _, tc := range []struct {
		origin, method, headers string
		status                  int
	}{
		{"https://pma.example", "GET", "authorization", 204},
		{"https://pma.example", "GET", "Authorization, Accept", 204},
		{"https://evil.example", "GET", "authorization", 403},
		{"https://pma.example", "POST", "authorization", 403},
		{"https://pma.example", "GET", "authorization, X-Other", 403},
		{"https://pma.example", "GET", "", 403},
	} {
		r := httptest.NewRequest(http.MethodOptions, "/api/decision-desk/owner-summary", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Access-Control-Request-Method", tc.method)
		r.Header.Set("Access-Control-Request-Headers", tc.headers)
		w := httptest.NewRecorder()
		m.summaryPreflight(w, r)
		if w.Code != tc.status || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("preflight: %d, want %d", w.Code, tc.status)
		}
	}
}

func testPKCEChallenge() string {
	hash := sha256.Sum256([]byte(strings.Repeat("v", 43)))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// Risk M1: browser history becomes a reusable bearer, or a stolen/replayed code
// can be exchanged without the initiating browser's state and PKCE verifier.
func TestConnectionHistoryCodeRequiresSingleUsePKCE(t *testing.T) {
	f := setup(t)
	sibling := config.Sibling{Tag: "pma", Origin: "https://pma.example"}
	f.m.WithInstances(config.Instances{Here: "ppm", Siblings: []config.Sibling{sibling}})
	person := f.person
	person.BrowserSession = true
	request := func(method, path, origin, token string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), person))
		r.Header.Set("Origin", origin)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	for n, scenario := range []string{"used", "expired"} {
		state := strings.Repeat(string(rune('a'+n)), 43)
		w := request("POST", "/api/decision-desk/connections", "http://example.com", "", map[string]string{"origin": sibling.Origin, "state": state, "code_challenge": testPKCEChallenge()})
		if w.Code != 201 {
			t.Fatalf("consent: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			URL string `json:"redirect_url"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("invalid redirect")
		}
		u, err := url.Parse(out.URL)
		if err != nil {
			t.Fatal(err)
		}
		params, err := url.ParseQuery(u.Fragment)
		if err != nil {
			t.Fatal(err)
		}
		code := params.Get("code")
		if code == "" || params.Has("token") || strings.Contains(out.URL, "desk_") {
			t.Fatal("history received a bearer instead of a single-use code")
		}
		for _, historyValue := range []string{code, strings.Replace(code, "code_", "desk_", 1)} {
			if w = request("GET", "/api/decision-desk/owner-summary", sibling.Origin, historyValue, nil); w.Code != 401 {
				t.Fatalf("history value granted a read: %d", w.Code)
			}
		}
		var stored []byte
		var seconds float64
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT exchange_code_hash,extract(epoch FROM exchange_expires_at-created_at)::float8 FROM desk_instance_connections WHERE tenant_id=$1 AND state_hash=$2`, person.TenantID, func() []byte { h := sha256.Sum256([]byte(state)); return h[:] }()).Scan(&stored, &seconds); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(code))
		if !bytes.Equal(stored, h[:]) || seconds < 59 || seconds > 65 {
			t.Fatal("code must be hashed and expire in about 60 seconds")
		}
		exchange := func(state, verifier string) *httptest.ResponseRecorder {
			return request("POST", "/api/decision-desk/connections/exchange", sibling.Origin, "", map[string]string{"code": code, "state": state, "code_verifier": verifier})
		}
		if w = exchange(state, strings.Repeat("w", 43)); w.Code != 401 {
			t.Fatalf("wrong verifier: %d %s", w.Code, w.Body.String())
		}
		if w = exchange(strings.Repeat("z", 43), strings.Repeat("v", 43)); w.Code != 401 {
			t.Fatalf("wrong state: %d", w.Code)
		}
		if scenario == "expired" {
			f.exec(t, `UPDATE desk_instance_connections SET exchange_expires_at=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND exchange_code_hash=$2`, person.TenantID, h[:])
			if w = exchange(state, strings.Repeat("v", 43)); w.Code != 401 {
				t.Fatalf("expired code: %d", w.Code)
			}
			continue
		}
		w = exchange(state, strings.Repeat("v", 43))
		var credential struct {
			Token string `json:"token"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &credential) != nil || credential.Token == "" {
			t.Fatalf("correct exchange: %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Vary"), "Origin") || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("exchange permits credential caching or cookies")
		}
		if w = exchange(state, strings.Repeat("v", 43)); w.Code != 401 {
			t.Fatalf("replayed code: %d", w.Code)
		}
		credentialRequest := httptest.NewRequest("GET", "/api/decision-desk/owner-summary", nil)
		credentialRequest.Header.Set("Authorization", "Bearer "+credential.Token)
		if principal, _, ok := summaryCredential(credentialRequest); !ok || principal.BrowserSession {
			t.Fatal("desk capability claims an interactive browser session")
		}

		// The exact value in history cannot read, even after successful exchange.
		if w = request("GET", "/api/decision-desk/owner-summary", sibling.Origin, code, nil); w.Code != 401 {
			t.Fatal("used history value granted a read")
		}
		if w = request("GET", "/api/decision-desk/owner-summary", sibling.Origin, credential.Token, nil); w.Code != 200 {
			t.Fatalf("body token cannot read: %d %s", w.Code, w.Body.String())
		}
		if w = request("DELETE", "/api/decision-desk/owner-summary", sibling.Origin, credential.Token, nil); w.Code != 204 {
			t.Fatalf("self-revoke: %d %s", w.Code, w.Body.String())
		}
		if w = request("GET", "/api/decision-desk/owner-summary", sibling.Origin, credential.Token, nil); w.Code != 401 {
			t.Fatal("self-revoked token still reads")
		}
	}
}

// Risk M2: normal parallel polls queue behind the tenant writer fence, or a
// valid bearer can load the Desk without a per-token request budget.
func TestSummaryParallelReadsAvoidFenceAndRateLimitBurst(t *testing.T) {
	f := setup(t)
	sibling := config.Sibling{Tag: "pma", Origin: "https://pma.example"}
	f.m.WithInstances(config.Instances{Here: "ppm", Siblings: []config.Sibling{sibling}})
	token := "desk_" + f.person.TenantID + "_" + f.person.ID + "_" + strings.Repeat("a", 43)
	hash := sha256.Sum256([]byte(token))
	stateHash := sha256.Sum256([]byte("hot-path"))
	// A recent usage timestamp makes the no-write claim independent of clocks.
	f.exec(t, `INSERT INTO desk_instance_connections(tenant_id,principal_id,origin,instance_tag,token_hash,state_hash,last_used_at) VALUES($1,$2,$3,'pma',$4,$5,clock_timestamp())`, f.person.TenantID, f.person.ID, sibling.Origin, hash[:], stateHash[:])
	read := func() *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		defer cancel()
		r := httptest.NewRequest("GET", "/api/decision-desk/owner-summary", nil).WithContext(ctx)
		r.Header.Set("Origin", sibling.Origin)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	// The held fence proves each simultaneous read completed without acquiring
	// it. The barrier releases both readers together; timeouts only guard hangs.
	holder, err := f.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(t.Context())
	if err := db.LockTenant(t.Context(), holder, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() { ready.Done(); <-start; results <- read() }()
	}
	ready.Wait()
	close(start)
	for range 2 {
		w := <-results
		if w.Code != 200 {
			t.Fatalf("recent usage read took held fence: %d %s", w.Code, w.Body.String())
		}
	}
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if w := read(); w.Code != 200 {
			t.Fatalf("within budget: %d %s", w.Code, w.Body.String())
		}
	}
	w := read()
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("burst has no per-token limit: %d %s", w.Code, w.Body.String())
	}
}

func TestExchangeAndDisconnectPreflight(t *testing.T) {
	m := New(nil).WithInstances(config.Instances{Siblings: []config.Sibling{{Tag: "pma", Origin: "https://pma.example"}}})
	for _, tc := range []struct {
		path, method, headers, origin string
		want                          int
	}{
		{"/api/decision-desk/connections/exchange", "POST", "Content-Type", "https://pma.example", 204},
		{"/api/decision-desk/connections/exchange", "POST", "Content-Type, Authorization", "https://pma.example", 403},
		{"/api/decision-desk/connections/exchange", "GET", "Content-Type", "https://pma.example", 403},
		{"/api/decision-desk/connections/exchange", "POST", "Content-Type", "https://evil.example", 403},
		{"/api/decision-desk/owner-summary", "DELETE", "Authorization", "https://pma.example", 204},
	} {
		r := httptest.NewRequest("OPTIONS", tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Access-Control-Request-Method", tc.method)
		r.Header.Set("Access-Control-Request-Headers", tc.headers)
		w := httptest.NewRecorder()
		mux := http.NewServeMux()
		m.Mount(mux)
		mux.ServeHTTP(w, r)
		if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Vary"), "Origin") || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("%s preflight: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}

// Risk: concurrent due polls all write usage, or a sliding-window boundary
// admits a burst early. This clock is isolated from all database timestamps.
func TestSummaryLimiterWindowAndUsageClaim(t *testing.T) {
	m := New(nil)
	now := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	m.summaryNow = func() time.Time { return now }
	hash := sha256.Sum256([]byte("limiter-test"))
	retry, finish := m.summaryAllowance(hash[:], true)
	if retry != 0 || finish == nil {
		t.Fatal("first due read must claim the usage write")
	}
	for range 11 {
		if retry, next := m.summaryAllowance(hash[:], true); retry != 0 || next != nil {
			t.Fatal("parallel read claimed another usage write or lost its read budget")
		}
	}
	finish(true)
	if retry, next := m.summaryAllowance(hash[:], true); retry != 60 || next != nil {
		t.Fatalf("burst limit: retry %d", retry)
	}
	now = now.Add(30 * time.Second)
	if retry, _ := m.summaryAllowance(hash[:], false); retry != 30 {
		t.Fatalf("retry after: %d", retry)
	}
	now = now.Add(31 * time.Second)
	if retry, next := m.summaryAllowance(hash[:], true); retry != 0 || next != nil {
		t.Fatal("recent usage must not write after the rate window expires")
	}
	now = now.Add(4 * time.Minute)
	if retry, next := m.summaryAllowance(hash[:], true); retry != 0 || next == nil {
		t.Fatal("usage write was not due after five minutes")
	} else {
		next(true)
	}
}
