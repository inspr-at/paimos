// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalSocketAuthAndBound(t *testing.T) {
	s, _, _ := testSupervisor(t)
	short, err := os.MkdirTemp("/tmp", "aeon-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	short, err = filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(short, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(short, "agentd.sock")
	local, err := ServeLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	defer s.Close(context.Background())
	info, err := os.Lstat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode: %v %v", info, err)
	}
	token, err := os.ReadFile(socket + ".token")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()
	request, _ := http.NewRequest("GET", "http://agentd/v1/status", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("unauthenticated status %d", response.StatusCode)
	}
	request, _ = http.NewRequest("GET", "http://agentd/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+string(token))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("authenticated status %d", response.StatusCode)
	}
	// Capacity is opt-in, preserving strict lifecycle decoders during upgrades.
	for _, query := range []string{"", "?include_capacity=1", "?include_capacity=1&account_id=missing", "?include_readiness=1"} {
		request, _ = http.NewRequest("GET", "http://agentd/v1/lifecycle"+query, nil)
		request.Header.Set("Authorization", "Bearer "+string(token))
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		err = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		_, readiness := body["account_statuses"]
		if readiness != strings.Contains(query, "include_readiness=1") {
			t.Fatal("readiness changed legacy lifecycle contract")
		}
		_, present := body["capacity_accounts"]
		if present != strings.Contains(query, "include_capacity=1") {
			t.Fatal("capacity changed the ordinary lifecycle contract")
		}
		if strings.Contains(query, "missing") && string(body["capacity_accounts"]) != "[]" {
			t.Fatal("empty selection is not an empty inventory")
		}
	}
	request, _ = http.NewRequest("POST", "http://agentd/v1/runs/run/control", strings.NewReader(strings.Repeat("x", 71<<10)))
	request.Header.Set("Authorization", "Bearer "+string(token))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("oversized control status %d", response.StatusCode)
	}
}

func TestAttachedHookSocketAndReconnectPreserveExternalOwnership(t *testing.T) {
	s, api, p := testSupervisor(t)
	a := &recoveryAPI{fakeAPI: api}
	s.api = a
	m, _, target, _, _ := attachIdentityFixture(t, Claude)
	observe := m.observe
	m.observe = func(pid int) (attachObservation, error) {
		if pid == os.Getpid() {
			return observeAttachProcess(pid)
		}
		return observe(pid)
	}
	dir, err := os.MkdirTemp("/tmp", "aeon-hook-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := ServeLocal(s, filepath.Join(dir, "daemon.sock"), m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close(); _ = s.Close(context.Background()) })
	token, err := os.ReadFile(local.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", local.Socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: time.Second * 5}
	input := map[string]any{"origin": m.cfg.Origin, "project_id": "11111111-1111-4111-8111-111111111111", "session_id": "22222222-2222-4222-8222-222222222222", "harness": "claude", "worker_lease": strings.Repeat("fixture-", 8), "owner_pid": target.PID, "activity_sequence": 2, "activity": "busy"}
	bind := func(auth bool) int {
		raw, _ := json.Marshal(input)
		r, _ := http.NewRequest("POST", "http://agentd/v1/attached-hook", bytes.NewReader(raw))
		if auth {
			r.Header.Set("Authorization", "Bearer "+string(token))
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}

	a.hookReplyLost = true
	if got := bind(true); got != 409 {
		t.Fatalf("uncertain heartbeat binding=%d", got)
	}
	s.recoveryMu.Lock()
	uncertainIdentity := *s.attachedHooks[input["session_id"].(string)].session.Ownership
	s.recoveryMu.Unlock()
	if got := bind(true); got != 200 {
		t.Fatalf("positive native hook binding=%d", got)
	}
	if got := bind(false); got != 403 {
		t.Fatalf("unauthenticated binding=%d", got)
	}
	input["origin"] = "https://foreign.test"
	if got := bind(true); got != 409 {
		t.Fatalf("foreign instance binding=%d", got)
	}
	input["origin"] = m.cfg.Origin
	sessionID := input["session_id"].(string)
	s.recoveryMu.Lock()
	b := s.attachedHooks[sessionID]
	s.recoveryMu.Unlock()
	if *b.session.Ownership != uncertainIdentity {
		t.Fatal("retry replaced an already attested hook identity")
	}
	q := RecoveryRequest{ID: "attached-reconnect", SessionID: sessionID, ProjectID: b.session.ProjectID, Action: "reconnect", Ownership: *b.session.Ownership, deadline: time.Now().Add(time.Minute)}
	a.requests = []RecoveryRequest{q}
	if err := s.recoverAgents(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(a.completions) != 1 || a.completions[0].Outcome != "reconnected" || a.probes != 1 {
		t.Fatalf("attached reconnect=%v probes=%d", a.completions, a.probes)
	}
	for _, scenario := range []string{"restart", "generation", "process", "helper", "expired", "lease-expired", "probe-error", "closed"} {
		t.Run(scenario, func(t *testing.T) {
			changed := q
			oldProcess, oldObserve, oldTouched, oldClosed := *target, m.observe, b.touched, m.closed
			defer func() {
				*target = oldProcess
				m.observe = oldObserve
				b.touched = oldTouched
				m.closed = oldClosed
				a.probeError = nil
			}()
			switch scenario {
			case "restart":
				changed.Action = "restart"
			case "generation":
				changed.Ownership.Generation = "old"
			case "process":
				target.Started = "reused-pid"
			case "helper":
				m.observe = func(pid int) (attachObservation, error) {
					if pid == os.Getpid() {
						return attachObservation{}, errAttachExited
					}
					return oldObserve(pid)
				}
			case "expired":
				changed.deadline = time.Now().Add(-time.Second)
			case "lease-expired":
				b.touched = time.Now().Add(-11 * time.Minute)
			case "probe-error":
				a.probeError = errors.New("inbox unavailable")
			case "closed":
				m.closed = true
			}
			a.requests = []RecoveryRequest{changed}
			if err := s.recoverAgents(t.Context()); err != nil {
				t.Fatal(err)
			}
			if a.completions[len(a.completions)-1].Outcome != "rejected" {
				t.Fatalf("unsafe attached recovery: %s", scenario)
			}
		})
	}

	hook := attachObservation{Process: b.helper, Parent: target.PID}
	hook.PID = 50
	hook.Started = "fixture-hook"
	beforeObserve := m.observe
	m.observe = func(pid int) (attachObservation, error) {
		if pid == hook.PID {
			return hook, nil
		}
		return beforeObserve(pid)
	}
	m.ancestry = m.observe
	delivery := HarnessDelivery{ID: "33333333-3333-4333-8333-333333333333", MessageID: "44444444-4444-4444-8444-444444444444", Cursor: 4, Body: "Untrusted fixture input"}
	a.harnessDeliveries = []HarnessDelivery{delivery}
	for i := 0; i < 2; i++ {
		out, err := s.serviceAttachedHook(t.Context(), hook, AttachedHookRequest{Operation: "pull", SessionID: sessionID})
		if err != nil || len(out) != 1 || out[0] != delivery {
			t.Fatal("bound hook could not replay its exact inbox delivery")
		}
		if a.harnessDeliveryCompletions != 0 {
			t.Fatal("pull acknowledged before stdout handoff")
		}
	}
	foreign := hook
	foreign.PID = os.Getpid()
	if _, err := s.serviceAttachedHook(t.Context(), foreign, AttachedHookRequest{Operation: "complete", SessionID: sessionID, DeliveryID: delivery.ID, Cursor: delivery.Cursor}); err == nil {
		t.Fatal("foreign helper acknowledged a bound message")
	}
	if _, err := s.serviceAttachedHook(t.Context(), hook, AttachedHookRequest{Operation: "complete", SessionID: sessionID, DeliveryID: delivery.ID, Cursor: delivery.Cursor}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.stopped:
		t.Fatal("attached reconnect signalled an external process")
	default:
	}
	if len(m.sessions) != 0 {
		t.Fatal("reconnect created a watch or bypassed its consent flow")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.claims != 0 || a.harnessDeliveryCompletions != 1 {
		t.Fatal("reconnect launched work or hook handoff was not acknowledged exactly once")
	}
}
