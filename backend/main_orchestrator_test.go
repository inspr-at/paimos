// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestProductionOrchestratorRoutesApplyNoStoreBeforeAuthentication(t *testing.T) {
	router := chi.NewRouter()
	router.Route("/api", mountAPI)
	for _, test := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodGet, "/api/orchestrator/v1", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/orchestrator/v1/config", "", http.StatusUnauthorized},
		{http.MethodPut, "/api/orchestrator/v1/config", `{}`, http.StatusUnauthorized},
		{http.MethodGet, "/api/orchestrator/v1/events", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/orchestrator/v1/not-a-route", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/orchestrator/v1/config", `{}`, http.StatusUnauthorized},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s %s status=%d, want %d", test.method, test.path, recorder.Code, test.status)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("%s %s Cache-Control=%q", test.method, test.path, got)
		}
	}
}
