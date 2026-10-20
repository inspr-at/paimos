// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/hookcap"
)

// Risk: pair polling used to reprint the pairing code, the approve URL and the
// hook block on every poll (AEON-401). A slow approval must keep that block
// once, rewrite one TTY status line, and emit JSON exactly as before.

type scriptedSetupPoller struct {
	steps []agentsetup.Progress
}

func (p *scriptedSetupPoller) Step(context.Context) (agentsetup.Progress, error) {
	if len(p.steps) == 0 {
		return agentsetup.Progress{}, errors.New("poll continued after the script")
	}
	next := p.steps[0]
	p.steps = p.steps[1:]
	return next, nil
}

func fixedProgressClock(start time.Time) func() time.Time {
	tick := 0
	return func() time.Time {
		now := start.Add(time.Duration(tick) * time.Second)
		tick++
		return now
	}
}

func runCalmPoll(t *testing.T, tty, jsonOutput bool, command string, initial agentsetup.Progress, steps []agentsetup.Progress, now func() time.Time) string {
	t.Helper()
	var out bytes.Buffer
	view := newSetupProgressView(tty, now)
	if err := view.Print(&out, jsonOutput, initial); err != nil {
		t.Fatal(err)
	}
	poller := &scriptedSetupPoller{steps: append([]agentsetup.Progress(nil), steps...)}
	if _, err := pollSetupProgress(t.Context(), command, initial, false, jsonOutput, &out, poller, func() {}, func(context.Context, time.Duration) error { return nil }, view); err != nil {
		t.Fatal(err)
	}
	if err := view.Finish(&out); err != nil {
		t.Fatal(err)
	}
	if len(poller.steps) != 0 {
		t.Fatalf("%s stopped with %d polls left", command, len(poller.steps))
	}
	return out.String()
}

func TestCalmPairOutputKeepsOneCodeBlockAndOneStatusLine(t *testing.T) {
	if progressWriterTTY(&bytes.Buffer{}) {
		t.Fatal("TTY detection treated a buffer as a terminal")
	}
	const code = "123-456-789"
	const uri = "https://pairing.example.test/agents/register-agent"
	const instruction = "Enter this code at the same Aeon instance, review the selected accounts and choices, then Connect your machine."
	start := time.Date(2026, 10, 18, 12, 24, 58, 0, time.UTC)
	waiting := agentsetup.Progress{
		Stage: "awaiting_approval", Action: instruction, UserCode: code, VerificationURI: uri,
	}
	hooks := []hookcap.Capability{
		{Harness: "claude", Blocker: "feature_disabled"},
		{Harness: "codex", Blocker: "qualification_pending"},
		{Harness: "cursor", Blocker: "unsupported_harness"},
	}
	provisioning := agentsetup.Progress{
		Stage: "provisioning", Action: "Daemon connectivity is unconfirmed.",
		UserCode: code, VerificationURI: uri, HookCapabilities: hooks,
		TouchIDConfirmation:   "needs pairing upgrade",
		TouchIDUpgradeCommand: "aeon-agentd pair --url 'https://pairing.example.test' --state-root '/tmp/pair-touch-id' --workspace '/tmp/workspace'",
		Accounts:              []agentsetup.Enrollment{{AccountID: "acct", Harness: "claude", State: "connected", Label: "person@example.test"}},
		AccountStatuses:       map[string]agentsetup.HarnessDetail{"acct": {State: "ready"}},
	}
	connected := provisioning
	connected.Stage = "connected"
	connected.Action = "This computer is connected."
	steps := make([]agentsetup.Progress, 0, 25)
	for i := 0; i < 20; i++ {
		steps = append(steps, waiting)
	}
	steps = append(steps, provisioning, provisioning, provisioning, provisioning, connected)

	t.Run("tty", func(t *testing.T) {
		text := runCalmPoll(t, true, false, "setup", waiting, steps, fixedProgressClock(start))
		if strings.Count(text, "Pairing code: "+code) != 1 || strings.Count(text, "Approve at: "+uri) != 1 || strings.Count(text, instruction) != 1 {
			t.Fatalf("code block repeated:\n%s", text)
		}
		if strings.Count(text, "\r") < 20 {
			t.Fatalf("status line was not rewritten in place (%d carriage returns)", strings.Count(text, "\r"))
		}
		if strings.Count(text, "\nWaiting for approval") != 1 {
			t.Fatal("waiting line was duplicated instead of rewritten")
		}
		if !strings.Contains(text, "Waiting for approval ⠋ (last check 12:24:58)") || !strings.Contains(text, "Waiting for approval ⠙ (last check 12:24:59)") || !strings.Contains(text, "Waiting for approval ⠋ (last check 12:25:18)") {
			t.Fatal("spinner frame or last-check time did not come from the injected clock")
		}
		for _, line := range []string{
			"provisioning: Daemon connectivity is unconfirmed.",
			"claude user hook: watch-only: feature_disabled",
			"codex user hook: watch-only: qualification_pending",
			"cursor user hook: watch-only: unsupported_harness",
			"Touch ID confirmation: needs pairing upgrade: aeon-agentd pair --url 'https://pairing.example.test' --state-root '/tmp/pair-touch-id' --workspace '/tmp/workspace'",
			"Use a new, empty state folder and approve in Aeon. Keep the old pairing until the new one is verified. Point the service at the new state root through its existing owner.",
			"account acct harness claude ready login person@example.test",
			"connected: This computer is connected.",
		} {
			if strings.Count(text, line) != 1 {
				t.Errorf("line %q count = %d", line, strings.Count(text, line))
			}
		}
		if !strings.HasSuffix(text, "connected: This computer is connected.\n") {
			t.Fatal("approval did not end on one final line")
		}
		order := []string{"claude user hook:", "provisioning: Daemon connectivity", "Touch ID confirmation:", "account acct "}
		previous := -1
		for _, part := range order {
			at := strings.Index(text, part)
			if at <= previous {
				t.Fatalf("post-approval block out of order at %q", part)
			}
			previous = at
		}
	})

	t.Run("plain", func(t *testing.T) {
		text := runCalmPoll(t, false, false, "setup", waiting, steps, fixedProgressClock(start))
		if strings.Contains(text, "\r") || strings.Contains(text, "\x1b") || strings.Contains(text, "Waiting for") {
			t.Fatalf("non-TTY drew a spinner:\n%s", text)
		}
		if strings.Count(text, "Pairing code: "+code) != 1 || strings.Count(text, "Approve at: "+uri) != 1 {
			t.Fatal("code block was not printed once")
		}
		for _, line := range []string{
			"awaiting_approval: " + instruction,
			"provisioning: Daemon connectivity is unconfirmed.",
			"connected: This computer is connected.",
			"claude user hook: watch-only: feature_disabled",
			"account acct harness claude ready login person@example.test",
		} {
			if strings.Count(text, line) != 1 {
				t.Errorf("state line %q count = %d\n%s", line, strings.Count(text, line), text)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		var golden bytes.Buffer
		if err := printSetupProgress(&golden, true, waiting); err != nil {
			t.Fatal(err)
		}
		for _, step := range steps {
			if err := printSetupProgress(&golden, true, step); err != nil {
				t.Fatal(err)
			}
		}
		got := runCalmPoll(t, true, true, "setup", waiting, steps, fixedProgressClock(start))
		if got != golden.String() || strings.Contains(got, "\r") || !strings.Contains(got, `"schema":"aeon.agent-setup.v1"`) {
			t.Fatal("JSON progress bytes changed")
		}
	})

	t.Run("hooks", func(t *testing.T) {
		status := agentsetup.Progress{Stage: "connected", Action: "Ready.", HookCapabilities: append([]hookcap.Capability(nil), hooks...)}
		var once, many bytes.Buffer
		if err := printSetupProgress(&once, false, status); err != nil {
			t.Fatal(err)
		}
		view := newSetupProgressView(false, fixedProgressClock(start))
		for i := 0; i < 4; i++ {
			if err := view.Print(&many, false, status); err != nil {
				t.Fatal(err)
			}
		}
		if many.String() != once.String() {
			t.Fatalf("status reprinted hook notices:\n%s", many.String())
		}
		status.HookCapabilities[0].Verified = true
		if err := view.Print(&many, false, status); err != nil {
			t.Fatal(err)
		}
		if strings.Count(many.String(), "claude user hook: watch-only: feature_disabled") != 1 || strings.Count(many.String(), "claude user hook: verified") != 1 {
			t.Fatalf("changed hook notice was dropped or repeated:\n%s", many.String())
		}
		withHooks := waiting
		withHooks.HookCapabilities = hooks
		for _, tty := range []bool{false, true} {
			var out bytes.Buffer
			add := newSetupProgressView(tty, fixedProgressClock(start))
			for i := 0; i < 6; i++ {
				if err := add.Print(&out, false, withHooks); err != nil {
					t.Fatal(err)
				}
			}
			if err := add.Finish(&out); err != nil {
				t.Fatal(err)
			}
			for _, line := range []string{
				"claude user hook: watch-only: feature_disabled",
				"codex user hook: watch-only: qualification_pending",
				"cursor user hook: watch-only: unsupported_harness",
			} {
				if strings.Count(out.String(), line) != 1 {
					t.Errorf("add-harness tty=%v line %q count = %d", tty, line, strings.Count(out.String(), line))
				}
			}
		}
	})

	t.Run("terminal states", func(t *testing.T) {
		for _, stage := range []string{"expired", "denied", "approved"} {
			terminal := waiting
			terminal.Stage = stage
			terminal.Action = "The pairing " + stage + "."
			text := runCalmPoll(t, true, false, "setup", waiting, []agentsetup.Progress{waiting, terminal}, fixedProgressClock(start))
			if strings.Count(text, "Pairing code: "+code) != 1 || strings.Count(text, stage+": The pairing "+stage+".") != 1 || strings.Count(text, "\r") < 1 || strings.Count(text, "\nWaiting for approval") != 1 {
				t.Fatalf("%s final line:\n%s", stage, text)
			}
		}
	})

	t.Run("changed fact", func(t *testing.T) {
		var out bytes.Buffer
		view := newSetupProgressView(true, fixedProgressClock(start))
		if err := view.Print(&out, false, provisioning); err != nil {
			t.Fatal(err)
		}
		first := out.String()
		next := provisioning
		next.Accounts = append(append([]agentsetup.Enrollment(nil), provisioning.Accounts...), agentsetup.Enrollment{AccountID: "next", Harness: "codex", State: "connected", Label: "other@example.test"})
		next.AccountStatuses = map[string]agentsetup.HarnessDetail{"acct": {State: "ready"}, "next": {State: "ready"}}
		if err := view.Print(&out, false, next); err != nil {
			t.Fatal(err)
		}
		delta := strings.TrimPrefix(out.String(), first)
		if strings.Contains(delta, "claude user hook:") || strings.Contains(delta, "Touch ID confirmation:") || strings.Contains(delta, "account acct ") {
			t.Fatalf("unchanged facts reprinted:\n%s", delta)
		}
		if strings.Count(out.String(), "account next harness codex ready login other@example.test") != 1 {
			t.Fatalf("changed account missing:\n%s", out.String())
		}
	})
}
