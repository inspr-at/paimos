// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusDoesNotContendWithPairOrMutateSnapshot(t *testing.T) {
	e, api, local, opts, executor := engineFixture(t)
	approveFixture(t, e, api, opts)
	// The pair writer keeps its lock; status gets a separate descriptor.
	reader, err := OpenStore(e.Store.Path(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	before, _ := e.Store.Read(snapshotName, 1<<20)
	calls := len(executor.calls)
	status := &Engine{Store: reader, Local: local}
	if p, err := status.Status(t.Context()); err != nil || p.Stage != "connected" {
		t.Fatalf("read-only status: %s %v", p.Stage, err)
	}
	if err := reader.Lock(); !errors.Is(err, ErrBusy) {
		t.Fatalf("writer lock was lost: %v", err)
	}
	after, _ := e.Store.Read(snapshotName, 1<<20)
	if !bytes.Equal(before, after) || calls != len(executor.calls) {
		t.Fatal("status mutated setup")
	}
	if _, err := e.Step(t.Context()); err != nil {
		t.Fatal("pair was interrupted", err)
	}
}

func TestStatusBeforeFirstSnapshotReportsSetupInProgress(t *testing.T) {
	s := testStore(t)
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenStore(s.Path(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	p, err := (&Engine{Store: reader}).Status(t.Context())
	if err != nil || !strings.Contains(p.Action, "Setup in progress") {
		t.Fatal(p.Stage, err)
	}
}

func TestReadinessNamesEveryPendingOrBlockedEnrollment(t *testing.T) {
	v := View{Enrollments: []Enrollment{{AccountID: "claude", Harness: "claude", Label: "Work account", State: "connected"}, {AccountID: "codex", Harness: "codex", Label: "Personal account", State: "connected"}}}
	local := LocalStatus{Ready: true, AccountStatuses: map[string]HarnessDetail{"claude": {State: "ready"}}, HarnessStatuses: map[string]string{"claude": "ready"}}
	reported := enrollmentReadiness(v, local)
	stage, action := readinessAction(v, reported)
	if stage != "blocked" || !strings.Contains(action, "Codex was approved but isn't set up") || !strings.Contains(action, "add-harness --harness codex") {
		t.Fatal(stage, action)
	}
	if reported.HarnessDetails["codex"].Reason != "binding_missing" {
		t.Fatal("missing server projection")
	}
	for _, tc := range []struct{ reason, state, want, stage string }{
		{"probe_pending", "checking", "60 seconds", "provisioning"},
		{"probe_timeout", "blocked", "did not finish within 60 seconds", "blocked"},
		{"probe_failed", "blocked", "availability check failed", "blocked"},
		{"capacity_capture", "checking", "within 10 seconds", "provisioning"},
		{"capacity_timeout", "blocked", "10-second limit", "blocked"},
		{"pin_missing", "blocked", "pin missing", "blocked"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			v.Enrollments = v.Enrollments[:1]
			local = LocalStatus{AccountStatuses: map[string]HarnessDetail{"claude": {State: tc.state, Reason: tc.reason}}}
			stage, action := readinessAction(v, local)
			if stage != tc.stage || !strings.Contains(action, "Work account") || !strings.Contains(action, tc.want) {
				t.Fatal(stage, action)
			}
		})
	}
}

func TestLaunchdPrivateDiagnosticPaths(t *testing.T) {
	e, api, _, opts, _ := engineFixture(t)
	opts.StartService = true
	approveFixture(t, e, api, opts)
	dir := filepath.Join(e.Services.Home, "Library", "Logs", "aeon-agentd")
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("private log directory missing", err)
	}
	raw, err := e.Services.Definition(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"<key>StandardOutPath</key>", "<key>StandardErrorPath</key>", "/Library/Logs/aeon-agentd/stdout.log", "/Library/Logs/aeon-agentd/stderr.log"} {
		if !strings.Contains(string(raw), part) {
			t.Fatal("missing log path", part)
		}
	}
}

func TestStatusReadsAtomicSnapshotsDuringPairWrites(t *testing.T) {
	e, api, local, opts, _ := engineFixture(t)
	approveFixture(t, e, api, opts)
	reader, err := OpenStore(e.Store.Path(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	status := &Engine{Store: reader, Local: local}
	saved, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			if err := e.save(saved, false); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 100; i++ {
		if p, err := status.Status(t.Context()); err != nil || p.Stage != "connected" {
			t.Fatalf("torn snapshot: %s %v", p.Stage, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPairCompletesWithNamedVerificationRefusal(t *testing.T) {
	e, api, local, opts, _ := engineFixture(t)
	approveFixture(t, e, api, opts)
	api.view.Enrollments[0].VerificationRunID = otherAccount
	api.view.Enrollments[0].VerificationState = "failed"
	api.view.Enrollments[0].VerificationError = "verification_unavailable"
	api.view.Enrollments[0].VerificationReason = "adapter_unsupported"
	local.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", Ready: true, VerificationReasons: map[string]string{otherAccount: "adapter_unsupported"}, AccountStatuses: map[string]HarnessDetail{testAccount: {State: "ready"}}}
	p, err := e.Step(t.Context())
	if err != nil || p.Stage != "verification_unavailable" || !strings.Contains(p.Action, "Codex verification couldn't run") || !strings.Contains(p.Action, "computer is paired") {
		t.Fatal("pair did not complete with named cause", p.Stage, err)
	}
	local.offline = true
	p, err = e.Status(t.Context())
	if err != nil || !strings.Contains(p.Action, "no heartbeat") {
		t.Fatal("connectivity gap lost heartbeat reason", p.Stage, err)
	}
}
