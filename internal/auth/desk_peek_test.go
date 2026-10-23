// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/decisiondesk"
)

// AEON-1709 replaces the accepted-cookie tests by an explicit no-cookie proof.
// Risk: a retired peek cookie, or even a valid browser session, bypasses the
// summary capability boundary or gets refreshed into another cross-site copy.
func TestDeskSummaryRejectsSessionAndRetiredPeekCookies(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	id, identity := signinPerson(t, tid, "owner", "Owner", "owner@example.com", "owner@example.com", "member")
	dbtest.BindRoleWith(t, adminPool, tid, id, "owner")
	mod := newMod(t, Config{Env: envDev})
	token, err := mod.startSession(t.Context(), identity, tid, id)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mod.Mount(mux)
	decisiondesk.New(appPool).WithInstances(config.Instances{Here: "ppm", Siblings: []config.Sibling{{Tag: "pma", Origin: "https://pma.example"}}}).Mount(mux)
	secured := mod.Middleware(mux)
	for _, names := range [][]string{{"aeon_desk_peek"}, {sessionCookieName}, {"aeon_desk_peek", sessionCookieName}} {
		r := httptest.NewRequest("GET", "/api/decision-desk/owner-summary", nil)
		r.Header.Set("Origin", "https://pma.example")
		for _, name := range names {
			r.AddCookie(&http.Cookie{Name: name, Value: token})
		}
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("cookies authenticated summary: %d", w.Code)
		}
		if w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("summary allowed browser credentials")
		}
	}
	r := httptest.NewRequest("GET", "/api/decision-desk/instances", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	_, r.Pattern = mux.Handler(r)
	w := httptest.NewRecorder()
	secured.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("discovery: %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "aeon_desk_peek" && c.MaxAge >= 0 {
			t.Fatal("discovery issued a peek cookie")
		}
	}

	// A real summary grant cannot authenticate another API route.
	r = httptest.NewRequest("POST", "/api/decision-desk/connections", strings.NewReader(`{"origin":"https://pma.example","state":"`+strings.Repeat("a", 43)+`"}`))
	r.Header.Set("Origin", "http://example.com")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	_, r.Pattern = mux.Handler(r)
	w = httptest.NewRecorder()
	secured.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("grant issuance: %d", w.Code)
	}
	var out struct {
		Address string `json:"redirect_url"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("invalid grant response")
	}
	address, err := url.Parse(out.Address)
	if err != nil {
		t.Fatal("invalid return address")
	}
	fragment, err := url.ParseQuery(address.Fragment)
	if err != nil || fragment.Get("token") == "" {
		t.Fatal("missing fragment grant")
	}
	for _, path := range []string{"/api/me", "/api/decision-desk/projection", "/api/decision-desk/connections"} {
		r = httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+fragment.Get("token"))
		r.Header.Set("Origin", "https://pma.example")
		_, r.Pattern = mux.Handler(r)
		w = httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("summary grant authenticated %s: %d", path, w.Code)
		}
	}

}

func TestDeskPeekClearFollowsBrowserCookiePath(t *testing.T) {
	m := &Module{}
	w := httptest.NewRecorder()
	m.clearDeskPeek(w)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "aeon_desk_peek" || cookies[0].Value != "" || cookies[0].Path != "/api/decision-desk/owner-summary" || cookies[0].MaxAge >= 0 {
		t.Fatal("retired cookie not cleared on its historical path")
	}
}
