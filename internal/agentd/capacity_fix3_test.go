// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/openrouter"
)

type exhaustedCheckAdapter struct{ *checkFixtureAdapter }

func (a *exhaustedCheckAdapter) CaptureCapacityResult(ctx context.Context, key string) CapacityCapture {
	c := a.checkFixtureAdapter.CaptureCapacityResult(ctx, key)
	c.Readings[0].UsedPercent = 100
	return c
}

func TestFix3AutomaticHardStopSurvivesFailedDeliveryAndRestart(t *testing.T) {
	s, api, base, clock := checkFixture(t)
	a := &exhaustedCheckAdapter{base}
	s.adapters[Codex] = a
	api.lost = true
	tickChecks(t, s, clock)
	if len(api.reports) != 1 || len(api.reports[0].Facts) != 1 || api.reports[0].Facts[0].StopKind != "named_reset" {
		t.Fatal("fixture did not observe exhausted quota")
	}
	want := api.reports[0]
	root, workspace, accounts := s.state.Path(), s.workspace, s.accounts
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, Workspace: workspace, DaemonID: "daemon", Accounts: accounts, Adapters: []Adapter{a}, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(t.Context())
	if restarted.capacityChecks["account"].Pending == nil {
		t.Fatal("failed automatic hard-stop delivery was not durable")
	}
	// Pending evidence is delivered even if the new health probe failed.
	restarted.blockedAccounts["account"] = true
	api.lost = false
	tickChecks(t, restarted, clock)
	if base.calls != 1 || len(api.reports) != 2 || !reflect.DeepEqual(api.reports[1], want) || restarted.capacityChecks["account"].Pending != nil {
		t.Fatal("restart recaptured, changed, or failed to acknowledge hard-stop evidence")
	}
	if !restarted.blockedAccounts["account"] || restarted.probedAccounts["account"] {
		t.Fatal("measurement replay changed newer local health failure")
	}
}

func fix3Pi(t *testing.T, api *checkFixtureAPI, clock *checkClock, handler http.Handler) (*Supervisor, *PiAdapter) {
	t.Helper()
	home := privateCapacityHome(t)
	store, err := agentsetup.OpenStore(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write("auth.json", []byte(`{"openrouter":{"type":"api_key","key":"synthetic-fix3-key"}}`), true); err != nil {
		t.Fatal(err)
	}
	store.Close()
	path := filepath.Join(privateCapacityHome(t), "pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$1\" = --version ] || exit 88\necho fixture\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	a := NewPiAdapter(path, map[string]string{"local": home})
	a.probes = map[string]piProbeResult{}
	a.SetExpectedProviders(map[string]string{"local": "openrouter"})
	a.OpenRouter = openrouter.Client{Base: server.URL}
	api.account.Harness = Pi
	api.account.Resources[0].Kind = "key_cap"
	s, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: privateCapacityHome(t), Workspace: privateCapacityHome(t), DaemonID: "daemon", Accounts: []EnrolledAccount{{ID: "account", Key: "local", Harness: Pi}}, Adapters: []Adapter{a}, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	s.probedAccounts["account"] = true
	return s, a
}

func TestFix3PiHealthPollingCannotOverlapKeyCapture(t *testing.T) {
	_, api, _, clock := checkFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s, a := fix3Pi(t, api, clock, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key" {
			t.Error("unexpected provider endpoint")
		}
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"data":{"limit":0}}`)
	}))
	done := make(chan struct{})
	go func() { defer close(done); tickChecks(t, s, clock) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not enter key request")
	}
	// A health poll must finish from local qualification without another /key.
	pollCtx, cancel := context.WithCancel(t.Context())
	poll := make(chan error, 1)
	go func() { _, err := a.ProbeStatus(pollCtx, "local"); poll <- err }()
	select {
	case err := <-poll:
		if err != nil {
			t.Error("local health qualification failed", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("health polling joined a vendor request instead of local qualification")
	}
	cancel()
	before := calls.Load()
	close(release)
	<-done
	if before != 1 {
		t.Fatalf("health polling overlapped capture: %d key requests", before)
	}
}

func TestFix3PiHealthPollingRespectsDurableCaptureBackoff(t *testing.T) {
	_, api, _, clock := checkFixture(t)
	var calls atomic.Int32
	s, a := fix3Pi(t, api, clock, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	for i, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 30 * time.Minute} {
		tickChecks(t, s, clock)
		if calls.Load() != int32(i+1) || !s.capacityChecks["account"].NextAttempt.Equal(clock.Now().Add(delay)) {
			t.Fatal("wrong scheduler retry step")
		}
		clock.Add(delay - time.Second)
		// Expire the independent health cache; it must not bypass scheduler state.
		a.probeMu.Lock()
		cached := a.probes["local"]
		cached.expires = time.Time{}
		a.probes["local"] = cached
		a.probeMu.Unlock()
		if available, err := a.ProbeStatus(t.Context(), "local"); !available || err != nil {
			t.Fatal("unknown measurement blocked qualified health", err)
		}
		tickChecks(t, s, clock)
		if calls.Load() != int32(i+1) {
			t.Fatalf("health poll bypassed persisted retry: %d calls", calls.Load())
		}
		clock.Add(time.Second)
	}
	root, workspace, accounts := s.state.Path(), s.workspace, s.accounts
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Restart with a fresh adapter, so no in-memory cache can prove durability.
	fresh := NewPiAdapter(a.Path, a.Homes)
	fresh.SetExpectedProviders(a.Providers)
	fresh.OpenRouter = a.OpenRouter
	restarted, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, Workspace: workspace, DaemonID: "daemon", Accounts: accounts, Adapters: []Adapter{fresh}, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(t.Context())
	clock.Add(-time.Second)
	if available, err := fresh.ProbeStatus(t.Context(), "local"); !available || err != nil {
		t.Fatal("restart health rejected unknown measurement", err)
	}
	restarted.probedAccounts["account"] = true
	tickChecks(t, restarted, clock)
	if calls.Load() != 4 {
		t.Fatal("restart health polling bypassed durable backoff", calls.Load())
	}
}
