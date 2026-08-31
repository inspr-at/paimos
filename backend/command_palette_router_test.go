// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

func TestCommandPaletteProductionRouterIsPrivateBeforeEveryGate(t *testing.T) {
	openSeedTestDB(t)
	router := sessionHomeProductionRouter()
	adminID := seedSessionHomeRouterUser(t, "palette-admin", "admin", "active", false)
	mustChangeID := seedSessionHomeRouterUser(t, "palette-must-change", "member", "active", true)
	externalID := seedSessionHomeRouterUser(t, "palette-external", "external", "active", false)
	adminCookie := seedSessionHomeRouterCookie(t, adminID, "000000000011")
	mustChangeCookie := seedSessionHomeRouterCookie(t, mustChangeID, "000000000012")
	externalCookie := seedSessionHomeRouterCookie(t, externalID, "000000000013")
	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Palette router','CPR','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	searchPath := fmt.Sprintf("/api/projects/%d/command-palette/v1?q=palette", projectID)

	for _, test := range []struct {
		method string
		path   string
		cookie string
		body   string
		status int
	}{
		{http.MethodGet, "/api/command-palette/v1/settings", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/command-palette/v1/settings", "session=invalid", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/command-palette/v1/settings", mustChangeCookie, "", http.StatusForbidden},
		{http.MethodGet, "/api/command-palette/v1/settings", externalCookie, "", http.StatusForbidden},
		{http.MethodGet, "/api/command-palette/v1/not-a-route", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/command-palette/v1/not-a-route", adminCookie, "", http.StatusNotFound},
		{http.MethodPut, "/api/command-palette/v1/settings", "", `{}`, http.StatusUnauthorized},
		{http.MethodGet, searchPath, adminCookie, "", http.StatusOK},
		{http.MethodGet, fmt.Sprintf("/api/projects/%d/command-palette/v1/not-a-route", projectID), adminCookie, "", http.StatusNotFound},
		{http.MethodGet, fmt.Sprintf("/api/projects/%d/command-palette/v1", projectID), adminCookie, "", http.StatusBadRequest},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		if test.cookie != "" {
			request.Header.Set("Cookie", test.cookie)
		}
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s %s status=%d body=%s want=%d", test.method, test.path, recorder.Code, recorder.Body.String(), test.status)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("%s %s Cache-Control=%q", test.method, test.path, got)
		}
	}
}
