// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
)

func sessionHomeProductionRouter() http.Handler {
	r := chi.NewRouter()
	// Keep the production outer ordering that matters for privacy and early
	// failures, then mount the exact production API route tree.
	r.Use(handlers.ClassifiedControlCachePolicyMiddleware)
	r.Use(handlers.ControlAwareRecoverer)
	r.Use(handlers.RequestIDMiddleware)
	r.Route("/api", mountAPI)
	return r
}

func seedSessionHomeRouterUser(t *testing.T, username, role, status string, mustChange bool) int64 {
	t.Helper()
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status,must_change_password)
		VALUES(?,'x',?,?,?)`, username, role, status, mustChange)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func seedSessionHomeRouterCookie(t *testing.T, userID int64, suffix string) string {
	t.Helper()
	sessionID := "session-home-router-cookie-" + suffix
	credentialID := fmt.Sprintf("10000000-0000-4000-8000-%012s", suffix)
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,csrf_token,credential_id)
		VALUES(?,?,datetime('now','+1 hour'),datetime('now'),'csrf',?)`, sessionID, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	return "session=" + sessionID
}

func TestSessionHomeProductionRouterIsPrivateBeforeEveryGate(t *testing.T) {
	openSeedTestDB(t)
	router := sessionHomeProductionRouter()

	adminID := seedSessionHomeRouterUser(t, "session-home-admin", "admin", "active", false)
	mustChangeID := seedSessionHomeRouterUser(t, "session-home-must-change", "member", "active", true)
	externalID := seedSessionHomeRouterUser(t, "session-home-external", "external", "active", false)
	revokedID := seedSessionHomeRouterUser(t, "session-home-revoked", "member", "active", false)
	inactiveID := seedSessionHomeRouterUser(t, "session-home-inactive", "member", "inactive", false)

	adminCookie := seedSessionHomeRouterCookie(t, adminID, "000000000001")
	mustChangeCookie := seedSessionHomeRouterCookie(t, mustChangeID, "000000000002")
	externalCookie := seedSessionHomeRouterCookie(t, externalID, "000000000003")
	revokedCookie := seedSessionHomeRouterCookie(t, revokedID, "000000000004")
	inactiveCookie := seedSessionHomeRouterCookie(t, inactiveID, "000000000005")

	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Session home router','SHR','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')`, revokedID, projectID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/projects/%d/session-home/v1", projectID)
	zoomPath := fmt.Sprintf("/api/projects/%d/session-home/zoom/v1", projectID)

	request := func(path, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	for _, tc := range []struct {
		name   string
		path   string
		cookie string
		status int
	}{
		{name: "unauthenticated", path: path, status: http.StatusUnauthorized},
		{name: "invalid credential", path: path, cookie: "session=not-a-session", status: http.StatusUnauthorized},
		{name: "inactive principal", path: path, cookie: inactiveCookie, status: http.StatusUnauthorized},
		{name: "must change password", path: path, cookie: mustChangeCookie, status: http.StatusForbidden},
		{name: "external role", path: path, cookie: externalCookie, status: http.StatusForbidden},
		{name: "revoked project view", path: path, cookie: revokedCookie, status: http.StatusNotFound},
		{name: "handler validation", path: "/api/projects/0/session-home/v1", cookie: adminCookie, status: http.StatusBadRequest},
		{name: "success", path: path, cookie: adminCookie, status: http.StatusOK},
		{name: "zoom unauthenticated", path: zoomPath, status: http.StatusUnauthorized},
		{name: "zoom invalid credential", path: zoomPath, cookie: "session=not-a-session", status: http.StatusUnauthorized},
		{name: "zoom inactive principal", path: zoomPath, cookie: inactiveCookie, status: http.StatusUnauthorized},
		{name: "zoom must change password", path: zoomPath, cookie: mustChangeCookie, status: http.StatusForbidden},
		{name: "zoom external role", path: zoomPath, cookie: externalCookie, status: http.StatusForbidden},
		{name: "zoom revoked project view", path: zoomPath, cookie: revokedCookie, status: http.StatusNotFound},
		{name: "zoom handler validation", path: "/api/projects/0/session-home/zoom/v1", cookie: adminCookie, status: http.StatusBadRequest},
		{name: "zoom malformed query", path: zoomPath + "?zoom=0", cookie: adminCookie, status: http.StatusBadRequest},
		{name: "zoom success", path: zoomPath, cookie: adminCookie, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(tc.path, tc.cookie)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), tc.status)
			}
			if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control=%q, want private, no-store", got)
			}
		})
	}

	// The fix is route-local: an unrelated internal auth failure keeps its
	// pre-existing cache behavior.
	unrelated := request("/api/projects", "")
	if unrelated.Code != http.StatusUnauthorized || unrelated.Header().Get("Cache-Control") != "" {
		t.Fatalf("unrelated route changed: status=%d cache=%q", unrelated.Code, unrelated.Header().Get("Cache-Control"))
	}
}
