// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

func TestAttachModeChoice(t *testing.T) {
	for _, tc := range []struct {
		name, input                string
		flag, statusOnly, rejected bool
	}{
		{name: "watch default", input: "\n"},
		{name: "watch chosen", input: "1\n"},
		{name: "status chosen", input: "2\n", statusOnly: true},
		{name: "status flag", flag: true, statusOnly: true},
		{name: "invalid", input: "WATCH\n", rejected: true},
		{name: "closed terminal", rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := promptAttachMode(t.Context(), bufio.NewReader(strings.NewReader(tc.input)), &out, tc.flag)
			if (err != nil) != tc.rejected || got != tc.statusOnly {
				t.Fatal("wrong mode decision")
			}
			if !strings.Contains(out.String(), "Status only (no conversation text)") || !tc.flag && !strings.Contains(out.String(), "Watch the conversation (default)") {
				t.Fatal("mode consequences hidden")
			}
		})
	}
}

func TestAttachApprovalNoticeNamesWhereToApproveAndPrefillsOnlyDigits(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	expires := now.Add(10 * time.Minute)
	got := attachApprovalNotice("https://aeon.example", "123456789", &expires, now)
	for _, want := range []string{
		"Waiting for your approval in Aeon (expires in 10 min).",
		"Open https://aeon.example/agents \u2192 Attach session and enter code 123-456-789",
		"\n  https://aeon.example/agents#attach=123456789\n",
		"you still approve",
		"Keep this terminal open; Ctrl-C detaches.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice lacks %q:\n%s", want, got)
		}
	}
	// The code rides only in the fragment, never a query string or path.
	for _, bad := range []string{"?attach", "?code", "/attach/"} {
		if strings.Contains(got, bad) {
			t.Fatalf("code outside the fragment (%q):\n%s", bad, got)
		}
	}
	// Anything but nine digits gets instructions without a link.
	for _, code := range []string{"", "12345678", "1234567890", "12345678a", "123456789\x1b[31m", "123 456 789", "١٢٣٤٥٦٧٨٩"} {
		notice := attachApprovalNotice("https://aeon.example", code, nil, now)
		if strings.Contains(notice, "#attach=") || strings.Contains(notice, "enter code") || !strings.Contains(notice, "Attach session and enter the attach code") || strings.Contains(notice, "expires in") {
			t.Fatalf("code %q must not reach a link:\n%s", code, notice)
		}
	}
}

func TestAttachTimeLeft(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	for _, tc := range []struct {
		name    string
		expires *time.Time
		want    string
	}{
		{"unknown", nil, ""},
		{"ten minutes", at(10 * time.Minute), "10 min"},
		{"nearly ten", at(9*time.Minute + 40*time.Second), "10 min"},
		{"two minutes", at(2 * time.Minute), "2 min"},
		{"under ninety seconds", at(45 * time.Second), "45 s"},
		{"expired", at(-time.Second), ""},
		{"skewed clock", at(3 * time.Hour), ""},
	} {
		if got := attachTimeLeft(tc.expires, now); got != tc.want {
			t.Fatalf("%s: %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestAttachEndStatesAreNamed(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	for _, tc := range []struct{ state, previous, want string }{
		{"unreachable", "pending", "expired before it was approved"},
		{"unreachable", "approved", "expired before it was approved"},
		{"detached", "pending", "declined or cancelled in Aeon"},
		{"detached", "approved", "approval was withdrawn"},
		{"confirmed_exited", "active", "session exited"},
		{"detached", "active", "Attach: detached"},
	} {
		if got := attachEndedLine(tc.state, tc.previous); !strings.Contains(got, tc.want) {
			t.Fatalf("%s after %s: %q lacks %q", tc.state, tc.previous, got, tc.want)
		}
	}
	for _, tc := range []struct {
		previous string
		expires  *time.Time
		want     string
	}{
		{"pending", at(-time.Second), "expired before it was approved"},
		{"pending", at(3 * time.Second), "expired before it was approved"},
		{"pending", at(5 * time.Minute), "declined, cancelled or expired in Aeon"},
		{"pending", nil, "declined, cancelled or expired in Aeon"},
		{"approved", at(5 * time.Minute), "approval ended before the attach started"},
		{"active", nil, "lost contact"},
	} {
		err := attachPollFailure(tc.previous, tc.expires, now)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "run attach again") {
			t.Fatalf("%s: %v lacks %q", tc.previous, err, tc.want)
		}
	}
}

func TestAttachStateLines(t *testing.T) {
	for _, tc := range []struct {
		state, mode string
		statusOnly  bool
		want        string
	}{
		{"approved", "aeon", false, "Approved in Aeon. Connecting"},
		{"approved", attachwatch.ConsentLocalAuth, false, "Waiting for local confirmation on the paired Mac; nothing is shared yet"},
		{"active", "aeon", false, "New turns are shared"},
		{"active", "aeon", true, "Session status is reported"},
	} {
		if got := attachStateLine(tc.state, tc.mode, tc.statusOnly); !strings.Contains(got, tc.want) {
			t.Fatalf("%s/%s: %q lacks %q", tc.state, tc.mode, got, tc.want)
		}
	}
}

func TestAttachWordingSeparatesComputerAndSession(t *testing.T) {
	for _, tc := range []struct{ language, paired, unlinked, linked, next string }{
		{"en", "Computer paired", "This session not yet linked", "This session linked", "approve in the browser window"},
		{"de", "Computer gekoppelt", "Diese Sitzung ist noch nicht verknüpft", "Diese Sitzung ist verknüpft", "Freigabe im Browserfenster"},
	} {
		words := attachWording(tc.language)
		for _, want := range []string{tc.paired, tc.unlinked} {
			if !strings.Contains(words.Unlinked, want) {
				t.Fatalf("%s preview hides pairing versus session: %q", tc.language, words.Unlinked)
			}
		}
		if !strings.Contains(words.Linked, tc.linked) || !strings.Contains(words.Next, tc.next) || !strings.Contains(words.Next, "Touch ID") {
			t.Fatalf("%s hides activation or the next approval step", tc.language)
		}
	}
}
