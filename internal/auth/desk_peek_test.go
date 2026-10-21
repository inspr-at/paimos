// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/decisiondesk"
)

// AEON-1495 risks: the cross-site summary cookie is issued to someone other
// than the workspace owner, authenticates any route but the owner summary,
// works from an origin outside the sibling configuration, or a stale copy
// signs the owner out of this instance (the unreadable-instance path).
func TestDeskPeekCookieReadsOnlyTheOwnerSummaryFromASibling(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	ownerID, ownerIdentity := signinPerson(t, tid, "owner", "Owner", "owner@example.com", "owner@example.com", "member")
	adminID, adminIdentity := signinPerson(t, tid, "admin", "Admin", "admin@example.com", "admin@example.com", "member")
	dbtest.BindRoleWith(t, adminPool, tid, ownerID, "owner")
	dbtest.BindRoleWith(t, adminPool, tid, adminID, "admin")
	const sibling, stranger = "https://pma.example", "https://elsewhere.example"
	mod := newMod(t, Config{Env: envDev})
	mod.SetDeskPeekOrigins([]string{sibling})
	mux := http.NewServeMux()
	mod.Mount(mux)
	decisiondesk.New(appPool).WithInstances(config.Instances{Here: "ppm", Siblings: []config.Sibling{{Tag: "pma", Origin: sibling}}}, mod).Mount(mux)
	secured := mod.Middleware(mux)
	serve := func(method, path, origin string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		return w
	}
	session := func(identity, principal string) *http.Cookie {
		token, err := mod.startSession(t.Context(), identity, tid, principal)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookieName, Value: token}
	}
	peekFrom := func(w *httptest.ResponseRecorder) *http.Cookie {
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == deskPeekCookieName {
				return cookie
			}
		}
		return nil
	}
	owner, admin := session(ownerIdentity, ownerID), session(adminIdentity, adminID)

	// The owner's same-site read issues the copy: the same session, one path, SameSite=None.
	issued := peekFrom(serve("GET", "/api/decision-desk/instances", "", owner))
	if issued == nil || issued.Value != owner.Value || issued.Path != "/api/decision-desk/owner-summary" || issued.SameSite != http.SameSiteNoneMode || !issued.Secure || !issued.HttpOnly || issued.MaxAge <= 0 {
		t.Fatalf("owner peek cookie %+v", issued)
	}
	// An admin who is not the owner gets none, and a copy it still holds is cleared.
	if got := peekFrom(serve("GET", "/api/decision-desk/instances", "", admin)); got != nil {
		t.Fatalf("admin received a peek cookie %+v", got)
	}
	adminPeek := &http.Cookie{Name: deskPeekCookieName, Value: admin.Value}
	if got := peekFrom(serve("GET", "/api/decision-desk/instances", "", admin, adminPeek)); got == nil || got.MaxAge >= 0 {
		t.Fatalf("admin's held peek cookie was not cleared: %+v", got)
	}
	peek := &http.Cookie{Name: deskPeekCookieName, Value: issued.Value}

	// The sibling's cross-site read carries only the copy and gets the summary with a CORS grant.
	w := serve("GET", "/api/decision-desk/owner-summary", sibling, peek)
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != sibling || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("sibling summary %d %s", w.Code, w.Body.String())
	}
	// Any other origin, any other route and a bearer header ignore the copy.
	for name, w := range map[string]*httptest.ResponseRecorder{
		"other origin": serve("GET", "/api/decision-desk/owner-summary", stranger, peek),
		"no origin":    serve("GET", "/api/decision-desk/owner-summary", "", peek),
		"projection":   serve("GET", "/api/decision-desk/projection", sibling, peek),
		"instances":    serve("GET", "/api/decision-desk/instances", sibling, peek),
		"me":           serve("GET", "/api/me", sibling, peek),
	} {
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s accepted the peek cookie: %d", name, w.Code)
		}
	}
	bearer := httptest.NewRequest("GET", "/api/decision-desk/owner-summary", nil)
	bearer.Header.Set("Origin", sibling)
	bearer.Header.Set("Authorization", "Bearer aeon_x_y")
	bearer.AddCookie(peek)
	_, bearer.Pattern = mux.Handler(bearer)
	w = httptest.NewRecorder()
	secured.ServeHTTP(w, bearer)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bearer request used the peek cookie: %d", w.Code)
	}
	// A crafted copy of another person's session authenticates, but the summary is owner only.
	if w = serve("GET", "/api/decision-desk/owner-summary", sibling, adminPeek); w.Code != http.StatusForbidden {
		t.Fatalf("non-owner summary %d", w.Code)
	}

	// Signing out on this instance ends the copy too: the sibling's read now
	// fails (its unreadable line), and the failure never clears a session here.
	out := serve("POST", "/api/auth/logout", "", owner, peek)
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout %d", out.Code)
	}
	if got := peekFrom(out); got == nil || got.MaxAge >= 0 {
		t.Fatalf("logout kept the peek cookie: %+v", got)
	}
	w = serve("GET", "/api/decision-desk/owner-summary", sibling, peek)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked copy still reads: %d", w.Code)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			t.Fatalf("a stale sibling read cleared this instance's session cookie: %+v", cookie)
		}
	}
}
