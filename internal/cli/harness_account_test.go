// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestClaudeLoginLabelNamesOnlyASubscriptionLogin(t *testing.T) {
	for _, tc := range []struct {
		name, raw, label string
		answered         bool
	}{
		{"subscription", `{"loggedIn":true,"authMethod":"claude.ai","email":"agent.one@example.com"}`, "agent.one@example.com", true},
		{"api key", `{"loggedIn":true,"authMethod":"api_key","email":"agent.one@example.com"}`, "", true},
		{"signed out", `{"loggedIn":false}`, "", true},
		{"no email", `{"loggedIn":true,"authMethod":"claude.ai"}`, "", true},
		{"control characters", `{"loggedIn":true,"authMethod":"claude.ai","email":"a‮@example.com"}`, "", true},
		{"not json", `Logged in as agent.one@example.com`, "", false},
		{"no login state", `{"email":"agent.one@example.com"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label, answered := claudeLoginLabel([]byte(tc.raw))
			if label != tc.label || answered != tc.answered {
				t.Fatalf("got %q/%v, want %q/%v", label, answered, tc.label, tc.answered)
			}
		})
	}
}

// AEON-1285: a session registered as admin@ that now runs as agent.one@ reports
// agent.one@ on its heartbeat; the launch label stays only for registration.
func TestHeartbeatReportsTheLiveAccount(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	var asked []string
	rt.harnessAccount = func(_ context.Context, harness string) (string, bool) {
		asked = append(asked, harness)
		return "agent.one@example.com", true
	}
	opts := heartbeatTestOptions(t.TempDir())
	opts.AccountLabel = "admin@example.com"
	if err := rt.runHeartbeat(context.Background(), opts, heartbeatDeps{
		alive: func(int) bool { return true },
		wait:  func(context.Context, int, time.Duration) error { return errOwnerExited },
	}); err != nil {
		t.Fatalf("heartbeat: %v stderr %s", err, stderr.String())
	}
	reg, beats := hbWhere(calls, http.MethodPost, "/harness-sessions"), hbWhere(calls, http.MethodPost, "/heartbeat")
	if len(reg) != 1 || len(beats) != 1 {
		t.Fatalf("registrations %d beats %d", len(reg), len(beats))
	}
	if reg[0].body["account_label"] != "admin@example.com" {
		t.Fatalf("registration label %v", reg[0].body["account_label"])
	}
	if beats[0].body["account_label"] != "agent.one@example.com" {
		t.Fatalf("heartbeat label %v, want the live account", beats[0].body["account_label"])
	}
	if len(asked) != 1 || asked[0] != "claude" {
		t.Fatalf("probe calls %v", asked)
	}
}

// The probe runs at most once per interval; an answer without a label falls back to
// the launch label, and a failed probe keeps the last live one.
func TestHeartbeatAccountProbeIsPacedAndFallsBack(t *testing.T) {
	type answer struct {
		label    string
		answered bool
	}
	answers := []answer{{"agent.one@example.com", true}, {"", false}, {"", true}}
	calls := 0
	rt := &runtime{harnessAccount: func(context.Context, string) (string, bool) {
		a := answers[calls]
		calls++
		return a.label, a.answered
	}}
	o := heartbeatOptions{Harness: "claude", AccountLabel: "admin@example.com"}
	var disk heartbeatDisk
	start := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	steps := []struct {
		at    time.Duration
		want  string
		calls int
	}{
		{0, "agent.one@example.com", 1},
		{accountProbeEvery - time.Second, "agent.one@example.com", 1},
		{accountProbeEvery, "agent.one@example.com", 2},
		{2 * accountProbeEvery, "admin@example.com", 3},
	}
	for _, step := range steps {
		if got := rt.heartbeatAccountLabel(context.Background(), o, &disk, start.Add(step.at)); got != step.want || calls != step.calls {
			t.Fatalf("at %s: label %q after %d probes, want %q after %d", step.at, got, calls, step.want, step.calls)
		}
	}
	// Without a probe (the tests' default runtime) the launch label is all there is.
	if got := (&runtime{}).heartbeatAccountLabel(context.Background(), o, &heartbeatDisk{}, start); got != "admin@example.com" {
		t.Fatalf("no probe: %q", got)
	}
}
