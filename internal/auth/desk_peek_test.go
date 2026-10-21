// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
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
	// An admin who is not the owner gets no usable copy. The browser does not
	// send the summary-path cookie on this route; the response still clears it.
	if got := peekFrom(serve("GET", "/api/decision-desk/instances", "", admin)); got == nil || got.MaxAge >= 0 || got.Path != deskPeekPath {
		t.Fatalf("admin discovery did not clear the peek cookie: %+v", got)
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

// AEON-1495 fix round 2: a browser sends the summary cookie only to its path.
// Discovery, logout and a new sign-in therefore never see it, and a conditional
// clear leaves the previous identity's copy in place. The jar below is the
// browser's path match: it must drop the copy from those responses anyway.
func TestDeskPeekClearFollowsBrowserCookiePath(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	ownerID, ownerIdentity := signinPerson(t, tid, "owner", "Owner", "owner@example.com", "owner@example.com", "member")
	adminID, adminIdentity := signinPerson(t, tid, "admin", "Admin", "admin@example.com", "admin@example.com", "member")
	dbtest.BindRoleWith(t, adminPool, tid, ownerID, "owner")
	dbtest.BindRoleWith(t, adminPool, tid, adminID, "admin")
	const sibling = "https://pma.example"
	mod := newMod(t, Config{Env: envDev})
	mod.SetDeskPeekOrigins([]string{sibling})
	mux := http.NewServeMux()
	mod.Mount(mux)
	decisiondesk.New(appPool).WithInstances(config.Instances{Here: "ppm", Siblings: []config.Sibling{{Tag: "pma", Origin: sibling}}}, mod).Mount(mux)
	ts := httptest.NewTLSServer(mod.Middleware(mux))
	t.Cleanup(ts.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := ts.Client()
	client.Jar = jar
	base, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	session := func(identity, principal string) string {
		t.Helper()
		token, err := mod.startSession(t.Context(), identity, tid, principal)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	setSession := func(token string) {
		t.Helper()
		jar.SetCookies(base, []*http.Cookie{{Name: sessionCookieName, Value: token, Path: "/", Secure: true}})
	}
	holds := func(path, name string) bool {
		t.Helper()
		target, err := url.Parse(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		for _, cookie := range jar.Cookies(target) {
			if cookie.Name == name {
				return true
			}
		}
		return false
	}
	do := func(method, path, body string, headers map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	cleared := func(resp *http.Response) bool {
		t.Helper()
		for _, cookie := range resp.Cookies() {
			if cookie.Name == deskPeekCookieName && cookie.Path == deskPeekPath && cookie.MaxAge < 0 {
				return true
			}
		}
		return false
	}
	summary := func() int {
		t.Helper()
		resp := do(http.MethodGet, "/api/decision-desk/owner-summary", "", map[string]string{"Origin": sibling})
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	// The session cookie matches every path, so a later summary would ignore a
	// surviving peek copy and answer as whoever is signed in. Drop it first:
	// only the summary-path copy can still authenticate.
	dropSession := func() {
		t.Helper()
		jar.SetCookies(base, []*http.Cookie{{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, Secure: true}})
		if holds("/", sessionCookieName) {
			t.Fatal("session cookie stayed in the jar")
		}
	}
	issue := func(token string) {
		t.Helper()
		setSession(token)
		resp := do(http.MethodGet, "/api/decision-desk/instances", "", nil)
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("instances %d", resp.StatusCode)
		}
		if !holds("/api/decision-desk/owner-summary", deskPeekCookieName) {
			t.Fatal("browser did not keep the summary cookie")
		}
		if holds("/api/decision-desk/instances", deskPeekCookieName) || holds("/api/auth/logout", deskPeekCookieName) || holds("/api/auth/dev-login", deskPeekCookieName) {
			t.Fatal("browser would send the summary cookie outside its path")
		}
	}

	ownerToken := session(ownerIdentity, ownerID)
	issue(ownerToken)
	dropSession()
	if summary() != http.StatusOK {
		t.Fatal("owner summary cookie did not read")
	}

	// Non-owner discovery never receives the copy and must still delete it.
	setSession(session(adminIdentity, adminID))
	if !holds("/api/decision-desk/owner-summary", deskPeekCookieName) {
		t.Fatal("replacing the session cookie in the jar dropped the summary copy")
	}
	resp := do(http.MethodGet, "/api/decision-desk/instances", "", nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK || !cleared(resp) || holds("/api/decision-desk/owner-summary", deskPeekCookieName) {
		t.Fatalf("non-owner discovery left the summary cookie: status %d cleared %v held %v", resp.StatusCode, cleared(resp), holds("/api/decision-desk/owner-summary", deskPeekCookieName))
	}
	dropSession()
	if summary() != http.StatusUnauthorized {
		t.Fatal("summary cookie survived non-owner discovery")
	}

	// A new sign-in replaces the session and must drop the previous identity's copy.
	issue(ownerToken)
	resp = do(http.MethodPost, "/api/auth/dev-login", `{"email":"admin@example.com","tenant":"inspr"}`, map[string]string{"Content-Type": "application/json"})
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
	if resp.StatusCode != http.StatusOK || !cleared(resp) || holds("/api/decision-desk/owner-summary", deskPeekCookieName) {
		t.Fatalf("session replacement left the summary cookie: status %d cleared %v held %v body %s", resp.StatusCode, cleared(resp), holds("/api/decision-desk/owner-summary", deskPeekCookieName), raw)
	}
	dropSession()
	if summary() != http.StatusUnauthorized {
		t.Fatal("previous identity's summary cookie survived session replacement")
	}

	// Logout never receives the summary copy and must still delete it.
	issue(ownerToken)
	resp = do(http.MethodPost, "/api/auth/logout", "", nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent || !cleared(resp) || holds("/api/decision-desk/owner-summary", deskPeekCookieName) {
		t.Fatalf("logout left the summary cookie: status %d cleared %v held %v", resp.StatusCode, cleared(resp), holds("/api/decision-desk/owner-summary", deskPeekCookieName))
	}
	if summary() != http.StatusUnauthorized {
		t.Fatal("summary cookie survived logout")
	}
}
