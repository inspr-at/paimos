// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHarnessSessionRoutesAreProjectScopedAndDistinct(t *testing.T) {
	router := chi.NewRouter()
	RegisterHarnessSessionRoutes(router)
	want := map[string]bool{
		"GET /projects/{id}/harness-sessions":                                            false,
		"POST /projects/{id}/harness-sessions":                                           false,
		"GET /projects/{id}/harness-sessions/{sessionID}":                                false,
		"POST /projects/{id}/harness-sessions/{sessionID}/heartbeat":                     false,
		"POST /projects/{id}/harness-sessions/{sessionID}/yield":                         false,
		"POST /projects/{id}/harness-sessions/{sessionID}/drain-steer":                   false,
		"POST /projects/{id}/harness-sessions/{sessionID}/complete-steer":                false,
		"POST /projects/{id}/harness-sessions/{sessionID}/controls/{kind}":               false,
		"POST /projects/{id}/harness-sessions/{sessionID}/controls/{controlID}/complete": false,
		"POST /projects/{id}/harness-sessions/{sessionID}/stop":                          false,
	}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if _, ok := want[key]; ok {
			want[key] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route, found := range want {
		if !found {
			t.Errorf("missing %s", route)
		}
	}
}
