// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteBusIdentityCheck(t *testing.T) {
	tests := []struct {
		name   string
		health doctorHealthResponse
		status string
		detail string
	}{
		{
			name:   "matching production identity",
			health: doctorHealthResponse{AgentBusInstance: "ppm", DeploymentInstance: "ppm", AgentBusIdentityEnforced: true},
			status: "ok",
			detail: "remote instance=ppm",
		},
		{
			name:   "mismatch fails",
			health: doctorHealthResponse{AgentBusInstance: "pma", DeploymentInstance: "ppm", AgentBusIdentityEnforced: true},
			status: "fail",
			detail: "does not match",
		},
		{
			name:   "default fails",
			health: doctorHealthResponse{AgentBusInstance: "default", DeploymentInstance: "default", AgentBusIdentityEnforced: true},
			status: "fail",
			detail: "empty or default",
		},
		{
			name:   "older server warns",
			health: doctorHealthResponse{},
			status: "warn",
			detail: "does not expose",
		},
		{
			name:   "unenforced server warns",
			health: doctorHealthResponse{AgentBusInstance: "ppm", DeploymentInstance: "ppm"},
			status: "warn",
			detail: "not enforced",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := remoteBusIdentityCheck(test.health)
			if got.Name != "bus_identity" || got.Status != test.status || !strings.Contains(got.Detail, test.detail) {
				t.Fatalf("remoteBusIdentityCheck()=%+v, want status=%s detail containing %q", got, test.status, test.detail)
			}
		})
	}
}

func TestDoctorUsesRemoteIdentityAndContinuesAfterMismatch(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			fmt.Fprint(w, `{"status":"ok","service":"ppm","version":"test","agent_bus_instance":"pma","deployment_instance":"ppm","agent_bus_identity_enforced":true}`)
		case "/api/auth/me":
			fmt.Fprint(w, `{"user":{"username":"operator"}}`)
		case "/api/schema":
			fmt.Fprint(w, `{"version":"test","enums":{},"transitions":{},"entities":{},"enum_fields":{},"conventions":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	t.Setenv("HOME", t.TempDir())
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, "test-key")
	t.Setenv(envPPMURL, "")
	t.Setenv(envPPMAPIKey, "")
	// These deliberately disagree with the server. Doctor must ignore local
	// process identity and report only the remote server's startup proof.
	t.Setenv("PAIMOS_ENV", "development")
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "local-alias")

	out, _, err := executeCLIForTest(t, "doctor")
	exit, ok := err.(*doctorExitCode)
	if !ok || exit.code != 2 {
		t.Fatalf("doctor error=%T %v, want hard failure", err, err)
	}
	for _, want := range []string{"bus_identity", "does not match", "auth", "schema"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output %q missing %q", out, want)
		}
	}
	joined := strings.Join(paths, ",")
	for _, want := range []string{"/api/health", "/api/auth/me", "/api/schema"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("doctor requests %q missing %q", joined, want)
		}
	}
}
