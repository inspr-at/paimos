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

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/version"
)

type disconnectPoller struct {
	statusCalls, stepCalls int
	err                    error
}

func (p *disconnectPoller) Status(context.Context) (agentsetup.Progress, error) {
	p.statusCalls++
	if p.statusCalls == 1 {
		return agentsetup.Progress{Stage: "draining", RetryAfterSeconds: 9}, p.err
	}
	return agentsetup.Progress{Stage: "disconnected", LocalProcesses: "drained", ServerRevocation: "confirmed"}, p.err
}

func (p *disconnectPoller) Step(context.Context) (agentsetup.Progress, error) {
	p.stepCalls++
	return agentsetup.Progress{}, errors.New("disconnect must reconcile status, not resume pairing")
}

func TestDisconnectPollsStatusUntilDrained(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		poller := &disconnectPoller{}
		var out bytes.Buffer
		var waits []time.Duration
		unlocks := 0
		wait := func(_ context.Context, delay time.Duration) error {
			if unlocks != poller.statusCalls {
				t.Fatal("store stayed locked between polls")
			}
			waits = append(waits, delay)
			return nil
		}
		p, err := pollSetupProgress(t.Context(), "disconnect", agentsetup.Progress{Stage: "draining"}, false, jsonOutput, &out, poller, func() { unlocks++ }, wait)
		if err != nil || p.Stage != "disconnected" || p.ServerRevocation != "confirmed" || p.LocalProcesses != "drained" || poller.statusCalls != 2 || poller.stepCalls != 0 || unlocks != 2 {
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
	if !errors.Is(err, poller.err) || unlocks != 1 || poller.statusCalls != 1 || poller.stepCalls != 0 {
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

func TestStatusVersionMismatchIsAdvisoryAndBoundToInstance(t *testing.T) {
	old := version.Version
	version.Version = "260929113854.0.0"
	t.Cleanup(func() { version.Version = old })
	const origin = "https://pairing.example.test"
	for _, tc := range []struct {
		name, serverVersion, instance, want string
		err                                 error
	}{
		{"same", version.Version, origin, "matching", nil},
		{"different", "260928113854.0.0", origin, "mismatch", nil},
		{"unpublished", "dev", origin, "unavailable", nil},
		{"legacy guide", "", origin, "unavailable", nil},
		{"other instance", "260928113854.0.0", "https://other.example.test", "unavailable", nil},
		{"offline", version.Version, origin, "unavailable", errors.New("fixture offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := versionGuideAPI{guide: agentsetup.Guide{InstanceURL: tc.instance, Protocol: "pairing-v1", Version: tc.serverVersion}, err: tc.err}
			p := setupVersionStatus(t.Context(), api, origin, agentsetup.Progress{Stage: "connected", Action: "Original status."})
			if p.VersionStatus != tc.want || p.Stage != "connected" || !strings.HasPrefix(p.Action, "Original status.") {
				t.Fatal("version advisory changed lifecycle status")
			}
			var out bytes.Buffer
			if err := printSetupProgress(&out, false, p); err != nil || strings.Contains(out.String(), "versions differ") != (tc.want == "mismatch") {
				t.Fatal("version warning missing or invented")
			}
		})
	}
}
