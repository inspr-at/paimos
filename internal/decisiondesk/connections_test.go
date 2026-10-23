// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
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
		w := request(p, "POST", "/api/decision-desk/connections", map[string]string{"origin": sibling.Origin, "state": state}, "", "")
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
		if err != nil || fragment.Get("state") != state || fragment.Get("instance") != "ppm" || fragment.Get("token") == "" {
			t.Fatal("grant not in fragment with bound state")
		}
		var rows struct {
			Connections []Connection `json:"connections"`
		}
		list := request(p, "GET", "/api/decision-desk/connections", nil, "", "")
		if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &rows) != nil || len(rows.Connections) == 0 {
			t.Fatalf("list: %d", list.Code)
		}
		for _, c := range rows.Connections {
			if c.CreatedAt.UTC().Format(time.RFC3339) == fragment.Get("created_at") && c.ExpiresAt.UTC().Format(time.RFC3339) == fragment.Get("expires_at") {
				return fragment.Get("token"), c.ID
			}
		}
		t.Fatal("issued connection absent")
		return "", ""
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
	if w := request(member, "POST", "/api/decision-desk/connections", map[string]string{"origin": sibling.Origin, "state": state}, "", ""); w.Code != 409 {
		t.Fatalf("state reused: %d", w.Code)
	}
	for _, origin := range []string{"https://evil.example", sibling.Origin + "/", sibling.Origin + ".evil.example"} {
		if w := request(member, "POST", "/api/decision-desk/connections", map[string]string{"origin": origin, "state": strings.Repeat("b", 43)}, "", ""); w.Code != 403 {
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
	for _, origin := range []string{"", "https://evil.example"} {
		w = request(tenant.Principal{}, "GET", "/api/decision-desk/owner-summary", nil, origin, token)
		if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("unconfigured origin: %d", w.Code)
		}
	}
	for _, p := range []tenant.Principal{f.agent, func() tenant.Principal { p := owner; p.KeyCreatorID = owner.ID; return p }(), func() tenant.Principal { p := owner; p.BrowserSession = false; return p }()} {
		if w = request(p, "POST", "/api/decision-desk/connections", map[string]string{"origin": sibling.Origin, "state": strings.Repeat("c", 43)}, "", ""); w.Code != 403 {
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
