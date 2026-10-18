// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Risk: Ping is healthy during a shared authentication outage, or readiness
// flaps and never recovers after the load balancer removes client traffic.
func TestAuthenticationReadinessHysteresis(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	failed := true
	s := &Server{readyProbe: func(context.Context) error { return nil }, readyNow: func() time.Time { return now }}
	s.AuthProbe = func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("authentication probe is unbounded")
		}
		if failed {
			return errors.New("shared authentication unavailable")
		}
		return nil
	}
	s.SetServing(true)
	check := func(status int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/ready", nil))
		if w.Code != status {
			t.Fatalf("ready=%d want=%d at %s: %s", w.Code, status, now, w.Body.String())
		}
		if status == 503 && !strings.Contains(w.Body.String(), "authentication_unavailable") {
			t.Fatalf("wrong failure reason: %s", w.Body.String())
		}
	}
	check(200)
	now = now.Add(14 * time.Second)
	check(200)
	now = now.Add(time.Second)
	check(503)
	// Liveness cannot start an authentication probe or alter the circuit.
	live := httptest.NewRecorder()
	s.Handler().ServeHTTP(live, httptest.NewRequest("GET", "/api/health", nil))
	if live.Code != http.StatusOK {
		t.Fatal("authentication outage changed liveness")
	}
	failed = false
	now = now.Add(time.Second)
	check(503)
	now = now.Add(29 * time.Second)
	check(503)
	now = now.Add(time.Second)
	check(200)
	// A brief failure never reopens the circuit.
	failed = true
	now = now.Add(time.Second)
	check(200)
	failed = false
	now = now.Add(time.Second)
	check(200)
}

func TestAuthenticationReadinessIgnoresCancelledAndSparseProbes(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	s := &Server{readyProbe: func(context.Context) error { return nil }, readyNow: func() time.Time { return now }, AuthProbe: func(context.Context) error { return errors.New("auth") }}
	s.SetServing(true)
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.handleReady(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/ready", nil).WithContext(ctx))
		now = now.Add(time.Minute)
	}
	if !s.authReady.last.IsZero() {
		t.Fatal("caller cancellation changed process health")
	}
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		s.handleReady(w, httptest.NewRequest("GET", "/api/ready", nil))
		if w.Code != 200 {
			t.Fatal("sparse failure was reported as sustained")
		}
		now = now.Add(time.Minute)
	}
	// A burst at one instant cannot satisfy minimum observations.
	var h authReadiness
	for i := 0; i < 100; i++ {
		if !h.observe(now, true) {
			t.Fatal("burst tripped readiness")
		}
	}
	if h.samples != 1 {
		t.Fatalf("burst counted %d samples", h.samples)
	}
}
