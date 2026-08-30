// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHealthHandlerReportsRemoteProductionIdentity(t *testing.T) {
	t.Setenv("PAIMOS_ENV", "production")
	t.Setenv("PAIMOS_DEPLOYMENT_INSTANCE", "ppm")
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")

	recorder := httptest.NewRecorder()
	healthHandler(recorder, httptest.NewRequest("GET", "/api/health", nil))

	var response healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.AgentBusInstance != "ppm" || response.DeploymentInstance != "ppm" || !response.AgentBusIdentityEnforced {
		t.Fatalf("unexpected remote identity evidence: %+v", response)
	}
}

func TestProductionEnvironmentAliases(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "production", want: true},
		{value: " PROD ", want: true},
		{value: "development", want: false},
		{value: "", want: false},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("PAIMOS_ENV", test.value)
			if got := productionEnvironment(); got != test.want {
				t.Fatalf("productionEnvironment()=%v, want %v", got, test.want)
			}
		})
	}
}
