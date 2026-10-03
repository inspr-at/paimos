// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentcompat"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/version"
)

type disconnectPoller struct {
	statusCalls, stepCalls int
	err                    error
}

func (p *disconnectPoller) Status(context.Context) (agentsetup.Progress, error) {
	p.statusCalls++
	return agentsetup.Progress{}, errors.New("read-only status must not reconcile cleanup")
}
func (p *disconnectPoller) Step(context.Context) (agentsetup.Progress, error) {
	p.stepCalls++
	if p.stepCalls == 1 {
		return agentsetup.Progress{Stage: "draining", RetryAfterSeconds: 9}, p.err
	}
	return agentsetup.Progress{Stage: "disconnected", LocalProcesses: "drained", ServerRevocation: "confirmed"}, p.err
}

func TestDisconnectPollsReconciliationUntilDrained(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		poller := &disconnectPoller{}
		var out bytes.Buffer
		var waits []time.Duration
		unlocks := 0
		wait := func(_ context.Context, delay time.Duration) error {
			if unlocks != poller.stepCalls {
				t.Fatal("store stayed locked between polls")
			}
			waits = append(waits, delay)
			return nil
		}
		p, err := pollSetupProgress(t.Context(), "disconnect", agentsetup.Progress{Stage: "draining"}, false, jsonOutput, &out, poller, func() { unlocks++ }, wait)
		if err != nil || p.Stage != "disconnected" || p.ServerRevocation != "confirmed" || p.LocalProcesses != "drained" || poller.statusCalls != 0 || poller.stepCalls != 2 || unlocks != 2 {
			t.Fatalf("disconnect polling failed: %v", err)
		}
		if !reflect.DeepEqual(waits, []time.Duration{5 * time.Second, 9 * time.Second}) {
			t.Fatal("retry delay was lost")
		}
		if jsonOutput {
			decoder := json.NewDecoder(&out)
			for _, stage := range []string{"draining", "disconnected"} {
				var emitted agentsetup.Progress
				if decoder.Decode(&emitted) != nil || emitted.Stage != stage || emitted.Schema != "aeon.agent-setup.v1" {
					t.Fatal("safe JSON progress missing")
				}
			}
			if decoder.Decode(&p) != io.EOF {
				t.Fatal("polling continued after disconnect")
			}
		} else if out.String() != "draining: \ndisconnected: \n" {
			t.Fatal("text progress missing")
		}
	}
}

func TestDisconnectPollStopsForOnceTerminalErrorAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		once        bool
	}{
		{"once", "draining", true},
		{"finished", "disconnected", false},
		{"revocation unconfirmed", "server_unconfirmed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			poller := &disconnectPoller{}
			wait := func(context.Context, time.Duration) error { t.Fatal("unexpected wait"); return nil }
			p, err := pollSetupProgress(t.Context(), "disconnect", agentsetup.Progress{Stage: tc.stage}, tc.once, true, io.Discard, poller, func() { t.Fatal("unexpected unlock") }, wait)
			if err != nil || p.Stage != tc.stage || poller.statusCalls != 0 || poller.stepCalls != 0 {
				t.Fatal("terminal or --once command polled")
			}
		})
	}
	poller := &disconnectPoller{err: errors.New("fixture offline")}
	unlocks := 0
	_, err := pollSetupProgress(t.Context(), "disconnect", agentsetup.Progress{Stage: "draining"}, false, false, io.Discard, poller, func() { unlocks++ }, func(context.Context, time.Duration) error { return nil })
	if !errors.Is(err, poller.err) || unlocks != 1 || poller.statusCalls != 0 || poller.stepCalls != 1 {
		t.Fatal("poll error lost or lock retained")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	poller = &disconnectPoller{}
	_, err = pollSetupProgress(ctx, "disconnect", agentsetup.Progress{Stage: "draining"}, false, false, io.Discard, poller, func() { t.Fatal("unexpected unlock") }, waitSetupPoll)
	if err == nil || !strings.Contains(err.Error(), "disconnect paused; rerun the same command") || poller.statusCalls != 0 {
		t.Fatal("cancelled disconnect did not remain resumable")
	}
}

func TestMaintenanceDoesNotRequireInstallerLink(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{
		filepath.Join(home, "brew", "Cellar", "aeon-agentd", "v1", "bin", "aeon-agentd"),
		filepath.Join(home, ".local", "lib", "aeon", "v1", "paimos-agentd"),
	} {
		if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("fixture executable"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"status", "disconnect", "repin"} {
			if got, err := setupExecutable(command, binary, home); err != nil || got != binary {
				t.Fatalf("%s blocked by missing installer link: %v", command, err)
			}
		}
		for _, command := range []string{"pair", "setup", "add-harness"} {
			if _, err := setupExecutable(command, binary, home); err == nil || strings.Contains(err.Error(), "before pairing") {
				t.Fatalf("%s lost the service installation check", command)
			}
		}
	}
}

type versionGuideAPI struct {
	agentsetup.PairingAPI
	guide agentsetup.Guide
	err   error
}

func (a versionGuideAPI) Guide(ctx context.Context) (agentsetup.Guide, error) {
	if _, ok := ctx.Deadline(); !ok {
		return agentsetup.Guide{}, errors.New("version lookup must be bounded")
	}
	return a.guide, a.err
}

func TestStatusVersionWindowIsAdvisoryAndBoundToInstance(t *testing.T) {
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	const origin = "https://pairing.example.test"
	policy := agentcompat.Supported()
	otherProtocol := policy
	otherProtocol.Protocol = "pairing-v2"
	for _, tc := range []struct {
		name, helper, protocol, instance, want string
		policy                                 *agentcompat.Policy
		err                                    error
	}{
		{"floor", policy.MinVersion, policy.Protocol, origin, "compatible", &policy, nil},
		{"newer than server", "261002072608.0.0", policy.Protocol, origin, "compatible", &policy, nil},
		{"below minimum", "260930072608.0.0", policy.Protocol, origin, "update_required", &policy, nil},
		{"protocol mismatch", policy.MinVersion, otherProtocol.Protocol, origin, "protocol_mismatch", &otherProtocol, nil},
		{"development", "dev", policy.Protocol, origin, "unknown", &policy, nil},
		{"legacy guide", policy.MinVersion, policy.Protocol, origin, "unavailable", nil, nil},
		{"other instance", policy.MinVersion, policy.Protocol, "https://other.example.test", "unavailable", &policy, nil},
		{"offline", policy.MinVersion, policy.Protocol, origin, "unavailable", &policy, errors.New("fixture offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version.Version = tc.helper
			api := versionGuideAPI{guide: agentsetup.Guide{InstanceURL: tc.instance, Protocol: tc.protocol, Version: "261001072608.0.0", AgentCompatibility: tc.policy}, err: tc.err}
			p := setupVersionStatus(t.Context(), api, origin, agentsetup.Progress{Stage: "connected", Action: "Original status."})
			if p.VersionStatus != tc.want || p.Stage != "connected" || !strings.HasPrefix(p.Action, "Original status.") {
				t.Fatalf("version advisory changed lifecycle status: %+v", p)
			}
			var out bytes.Buffer
			needsUpdate := tc.want == "update_required" || tc.want == "protocol_mismatch"
			if err := printSetupProgress(&out, false, p); err != nil || strings.Contains(out.String(), "Update aeon-agentd to at least "+policy.MinVersion) != needsUpdate {
				t.Fatal("version advice missing or invented")
			}
		})
	}
}

func TestStatusCommandDuringPairLockIsReadOnly(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	writer, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err = writer.Lock(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = setupCommandInput("status", []string{"--state-root", root, "--json"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Setup in progress") {
		t.Fatal("missing in-progress report")
	}
	if _, err = os.Stat(filepath.Join(root, "daemon")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status created daemon state")
	}
	reader, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err = reader.Lock(); !errors.Is(err, agentsetup.ErrBusy) {
		t.Fatal("status released or aborted pair lock", err)
	}
}

func TestVerificationRefusalCompletesPairPolling(t *testing.T) {
	p := agentsetup.Progress{Stage: "verification_unavailable"}
	if setupNeedsPoll("setup", p) || setupNeedsPoll("add-harness", p) {
		t.Fatal("paired computer waits forever on unavailable verification")
	}
}
