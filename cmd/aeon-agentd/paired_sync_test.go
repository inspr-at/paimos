// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/localjournal"
)

type syncProbeAPI struct {
	repinIdentityAPI
	probes map[string]int
	polls  int
}

func (a *syncProbeAPI) Queued(context.Context) ([]agentd.Run, error) {
	a.polls++
	return nil, nil
}
func (a *syncProbeAPI) Probe(_ context.Context, id, _, _ string, available bool) error {
	if !available {
		return errors.New("fixture probe unexpectedly unavailable")
	}
	a.probes[id]++
	return nil
}

type syncProbeAdapter struct {
	agentd.Adapter
	harness string
	probes  int
}

func (a *syncProbeAdapter) Name() string { return a.harness }
func (a *syncProbeAdapter) Probe(context.Context, string) bool {
	a.probes++
	return true
}

// Runs the production tick, real SyncFences and real Supervisor.PollOnce with
// synthetic pairing state, a TLS fixture server and three counting adapters.
// No vendor CLI, operator profile or production endpoint participates.
func TestPairedSyncTombstoneProbesAndFailureDiagnostics(t *testing.T) {
	e, pins := repinCommandFixture(t)
	c, err := agentsetup.ReadRuntimeConfig(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	c.NodePath, c.ClaudeSDKPath = pins.NodePath, pins.SDKPath
	for i, harness := range []string{agentd.Codex, agentd.Cursor} {
		c.Accounts = append(c.Accounts, agentsetup.RuntimeAccount{Harness: harness, AccountID: []string{"55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"}[i], Key: harness + "-approved", Path: pins.NodePath})
	}
	const retired = "77777777-7777-4777-8777-777777777777"
	view := agentsetup.View{TenantID: c.TenantID, ComputerID: c.ComputerID, PrincipalID: c.PrincipalID, DaemonID: c.DaemonID, ComputerState: "connected", Workspace: c.Workspace, Revision: 2}
	candidates := []agentsetup.LocalCandidate{}
	for _, a := range c.Accounts {
		view.Enrollments = append(view.Enrollments, agentsetup.Enrollment{AccountID: a.AccountID, AccountKey: a.Key, Harness: a.Harness, Label: a.Harness, State: "connected"})
		candidates = append(candidates, agentsetup.LocalCandidate{Candidate: agentsetup.Candidate{Key: a.Key, Harness: a.Harness, Label: a.Harness}, Path: a.Path})
	}
	view.Enrollments[0].Label = "renamed live account"
	view.Enrollments = append(view.Enrollments, agentsetup.Enrollment{AccountID: retired, AccountKey: "old-claude", Harness: agentd.Claude, Label: "retired", State: "revoked"})
	var mu sync.Mutex
	mode := "healthy"
	var reported *agentsetup.SetupProgress
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-pairing/reconcile" {
			t.Error("unexpected pairing route")
			w.WriteHeader(404)
			return
		}
		var proof agentsetup.ProofRequest
		if err := json.NewDecoder(r.Body).Decode(&proof); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		reported = proof.Progress
		v := view
		v.Enrollments = append([]agentsetup.Enrollment(nil), view.Enrollments...)
		switch mode {
		case "unapproved":
			v.Enrollments[1].AccountKey = "unapproved-connected-account"
		case "offline":
			w.WriteHeader(503)
			_, _ = w.Write([]byte("fixture private response must not escape"))
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}))
	defer server.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = transport }()
	c.Origin = server.URL
	snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": c.Origin, "phase": "connected", "request": agentsetup.DeviceRequest{RequestID: c.TenantID, Details: agentsetup.Details{Workspace: c.Workspace}}, "response": agentsetup.DeviceResponse{TenantID: c.TenantID}, "view": view, "candidates": candidates, "device_secret": strings.Repeat("a", 64), "runtime_secret": strings.Repeat("b", 64), "lifecycle_secret": strings.Repeat("c", 64)}
	writeRuntime := func(config agentsetup.RuntimeConfig) {
		t.Helper()
		raw, _ := json.Marshal(config)
		if err := e.Store.Write(agentsetup.RuntimeName, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(snapshot)
	if err := e.Store.Write("pairing.json", raw, false); err != nil {
		t.Fatal(err)
	}
	writeRuntime(c)
	if err := e.Store.Write("runtime.key", []byte("aeon_fixture_"+strings.Repeat("b", 64)), true); err != nil {
		t.Fatal(err)
	}
	accounts, _, err := pairedAdapters(c)
	if err != nil {
		t.Fatal(err)
	}
	adapters := []agentd.Adapter{}
	for _, account := range accounts {
		if account.DependencyBlocked {
			t.Fatal("fixture must allow an actual account probe")
		}
		adapters = append(adapters, &syncProbeAdapter{harness: account.Harness})
	}
	api := &syncProbeAPI{repinIdentityAPI: repinIdentityAPI{c: c}, probes: map[string]int{}}
	root := e.Store.Path()
	now := time.Now()
	s, err := agentd.NewSupervisor(t.Context(), agentd.Config{Now: func() time.Time { return now }, API: api, StateRoot: filepath.Join(root, "daemon"), DaemonID: c.DaemonID, Workspace: c.Workspace, Accounts: accounts, Adapters: adapters})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(t.Context())
	if err := agentd.PersistFence(filepath.Join(root, "daemon"), c.DaemonID, retired); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(logger)
	diagnostics := pairedDiagnostics{}
	tick := func() error {
		var err error
		now = now.Add(5 * time.Second)
		c, err = pairedPollIteration(t.Context(), s, root, c, &diagnostics, s.PollScheduledOnce)
		return err
	}
	if err := tick(); err != nil {
		t.Fatal("stale revoked enrollment or changed label suppressed probes", err)
	}
	if len(api.probes) != 3 || api.polls != 1 {
		t.Fatal("not all live enrollments reached probe reports", api.probes)
	}
	for _, adapter := range adapters {
		if adapter.(*syncProbeAdapter).probes != 1 {
			t.Fatal("a live harness was never probed")
		}
	}
	if !s.Lifecycle("").Ready || logs.Len() != 0 {
		t.Fatal("healthy sync reported failure")
	}
	setMode := func(next string) { mu.Lock(); mode = next; mu.Unlock() }
	assertFailure := func(cause string) {
		t.Helper()
		local, err := (localPairing{root: root, supervisor: s}).Status(t.Context(), "")
		if err != nil || local.Ready {
			t.Fatal("sync failure claimed readiness", err)
		}
		for _, account := range accounts {
			for _, detail := range []agentsetup.HarnessDetail{local.AccountStatuses[account.ID], local.HarnessDetails[account.Harness]} {
				if detail.State != "blocked" || detail.Reason != agentsetup.PairingSyncFailed || detail.ReasonDetail != cause {
					t.Fatal("sync failure lost from local lifecycle", detail)
				}
			}
		}
		if api.polls != 1 {
			t.Fatal("failed pairing allowed polling")
		}
	}
	setMode("unapproved")
	for range 2 {
		if err := tick(); !errors.Is(err, agentsetup.ErrUnapprovedAccount) {
			t.Fatal("unapproved connected account not refused", err)
		}
		assertFailure(agentsetup.PairingAccountUnapproved)
	}
	mu.Lock()
	projected := reported.HarnessDetails[agentd.Codex]
	mu.Unlock()
	if projected.Reason != agentsetup.PairingSyncFailed || projected.ReasonDetail != agentsetup.PairingAccountUnapproved {
		t.Fatal("next reconciliation did not publish the actual blocker", projected)
	}
	setMode("offline")
	for i := range 2 {
		if err := tick(); err == nil {
			t.Fatal("offline sync unexpectedly succeeded")
		}
		assertFailure(agentsetup.PairingServerUnavailable)
		for _, adapter := range adapters {
			if adapter.(*syncProbeAdapter).probes != i+2 {
				t.Fatal("retryable server failure suppressed account health probe")
			}
		}
		if err := s.StartRun(t.Context(), agentd.Run{}); !errors.Is(err, agentd.ErrDraining) {
			t.Fatal("pairing outage did not fence direct dispatch", err)
		}
	}
	setMode("healthy")
	changed := c
	changed.PrincipalID = retired
	writeRuntime(changed)
	for range 2 {
		if err := tick(); err == nil || err.Error() != "pairing configuration identity changed" {
			t.Fatal("runtime identity check not reached", err)
		}
		assertFailure(agentsetup.PairingRuntimeUnavailable)
	}
	writeRuntime(c)
	if err := tick(); err != nil || !s.Lifecycle("").Ready || api.polls != 2 {
		t.Fatal("pairing recovery did not resume probes", err)
	}
	for _, detail := range s.Lifecycle("").AccountStatuses {
		if detail.State != "ready" || detail.Reason != "" || detail.ReasonDetail != "" {
			t.Fatal("recovery retained stale diagnostic", detail)
		}
	}
	setMode("unapproved")
	if err := tick(); !errors.Is(err, agentsetup.ErrUnapprovedAccount) {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "level=WARN") != 3 || strings.Count(logs.String(), "reason=pairing_sync_failed") != 3 || strings.Contains(logs.String(), "fixture private") || strings.Contains(logs.String(), root) {
		t.Fatal("warning was not bounded, deduplicated and value-free")
	}
	if !strings.Contains(logs.String(), "first_cause=http_503 retryable=true") || !strings.Contains(logs.String(), "first_cause=account_unapproved retryable=false") {
		t.Fatal("first safe cause or retry policy missing")
	}
}

func TestHistoricalFencePreservesUnconfirmedProcessEvidence(t *testing.T) {
	e, _ := repinCommandFixture(t)
	c, err := agentsetup.ReadRuntimeConfig(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(e.Store.Path(), "daemon")
	store, err := agentsetup.OpenStore(state, true)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	j, err := localjournal.Open(localjournal.Config[agentd.Record]{Directory: state, Prefix: "aeon-agentd-" + c.DaemonID, Version: agentd.RecordSchemaVersion, MaxBytes: 4 << 20, MaxRecords: 4096, Key: func(r agentd.Record) (string, error) { return r.RunID, nil }, Validate: func(agentd.Record) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Put(agentd.Record{TenantID: c.TenantID, PrincipalID: c.PrincipalID, AccountID: "historical", RunID: "prior", Generation: "old", State: "running"}); err != nil {
		t.Fatal(err)
	}
	s, err := agentd.NewSupervisor(t.Context(), agentd.Config{API: repinIdentityAPI{c: c}, StateRoot: state, DaemonID: c.DaemonID, Workspace: c.Workspace, Accounts: []agentd.EnrolledAccount{{ID: c.Accounts[0].AccountID, Key: c.Accounts[0].Key, Harness: agentd.Claude, DependencyBlocked: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(t.Context())
	local := localPairing{root: e.Store.Path(), supervisor: s}
	status, err := local.Fence(t.Context(), c.DaemonID, "historical")
	if err != nil || status.State != "unconfirmed" || len(status.Unconfirmed) != 1 || status.Unconfirmed[0] != "prior" {
		t.Fatal("historical fence invented process exit", err)
	}
	if _, err := local.Fence(t.Context(), "other-daemon", "another-historical"); !errors.Is(err, agentd.ErrScope) {
		t.Fatal("historical fence bypassed daemon identity", err)
	}
}
