// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/db"
)

func TestStructuredKnowledgeProductionRoutesAreSingleAndPrivateBeforeEveryGate(t *testing.T) {
	openSeedTestDB(t)
	router := sessionHomeProductionRouter()
	adminID := seedSessionHomeRouterUser(t, "structured-router-admin", "admin", "active", false)
	mustChangeID := seedSessionHomeRouterUser(t, "structured-router-must-change", "member", "active", true)
	externalID := seedSessionHomeRouterUser(t, "structured-router-external", "external", "active", false)
	adminCookie := seedSessionHomeRouterCookie(t, adminID, "000000000021")
	mustChangeCookie := seedSessionHomeRouterCookie(t, mustChangeID, "000000000022")
	externalCookie := seedSessionHomeRouterCookie(t, externalID, "000000000023")
	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Structured router','SKR','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	path := fmt.Sprintf("/api/projects/%d/structured-knowledge/v1", projectID)

	for _, test := range []struct {
		name, cookie string
		status       int
	}{
		{"unauthenticated", "", http.StatusUnauthorized},
		{"invalid credential", "session=invalid", http.StatusUnauthorized},
		{"password gate", mustChangeCookie, http.StatusForbidden},
		{"external gate", externalCookie, http.StatusForbidden},
		{"success", adminCookie, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			if test.cookie != "" {
				request.Header.Set("Cookie", test.cookie)
			}
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s want=%d", recorder.Code, recorder.Body.String(), test.status)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control=%q", got)
			}
		})
	}

	registered := chi.NewRouter()
	registered.Route("/api", mountAPI)
	want := map[string]struct{}{
		"GET /api/projects/{id}/structured-knowledge/v1":                                         {},
		"POST /api/projects/{id}/structured-knowledge/v1/validate":                               {},
		"POST /api/projects/{id}/structured-knowledge/v1/remember":                               {},
		"PUT /api/projects/{id}/structured-knowledge/v1/compact":                                 {},
		"POST /api/projects/{id}/structured-knowledge/v1/entries":                                {},
		"POST /api/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/adopt":            {},
		"PUT /api/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}":                   {},
		"POST /api/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/links":            {},
		"DELETE /api/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/links/{linkID}": {},
		"POST /api/structured-knowledge/v1/entries/{knowledgeID}/promote":                        {},
	}
	counts := map[string]int{}
	if err := chi.Walk(registered, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := strings.ToUpper(method) + " " + route
		if _, ok := want[key]; ok {
			counts[key]++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for operation := range want {
		if counts[operation] != 1 {
			t.Fatalf("production operation %s registered %d times", operation, counts[operation])
		}
	}
}
