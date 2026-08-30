//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
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
