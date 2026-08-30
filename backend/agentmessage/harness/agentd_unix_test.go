//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/backend/agentdwire"
)

func TestAgentdExitedSessionRequestsDurableIdleReroute(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pai849-agentd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agentd.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"session_not_running"}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	target := `{"socket":"` + socket + `","session_id":"33333333-3333-4333-8333-333333333333"}`
	_, err = (AgentdCodexPlugin{}).Deliver(context.Background(), DeliverRequest{
		Level: LevelSteer, TargetRef: target, Body: "managed steer", CorrelationID: "delivery-exited",
	})
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error=%v want typed unavailability", err)
	}
	if !unavailable.Reroute || unavailable.FallbackReason != "idle" {
		t.Fatalf("unavailable=%+v", unavailable)
	}
}

func TestAgentdClaudeDeliveryRequiresCorrelatedQueryInputEvidence(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pai850-agentd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agentd.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "85000000-0000-4000-8000-000000000850"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/"+sessionID+"/steer" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"operation": "steer", "session_id": sessionID, "identity": "claude:worker",
			"requested_level": "steer", "effective_level": "steer", "fallback_reason": "",
			"primitive": agentdwire.ClaudeSteerPrimitive, "correlation_id": "delivery-850",
			"vendor_message_id": "85000000-0000-4000-8000-000000000851", "applied_at": "2026-08-30T04:00:00Z",
		})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	result, err := (AgentdClaudePlugin{}).Deliver(context.Background(), DeliverRequest{
		Level: LevelSteer, TargetRef: `{"socket":"` + socket + `","session_id":"` + sessionID + `"}`,
		Body: "same Query steer", CorrelationID: "delivery-850",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveLevel != LevelSteer || result.Primitive != agentdwire.ClaudeSteerPrimitive {
		t.Fatalf("result=%+v", result)
	}
}
