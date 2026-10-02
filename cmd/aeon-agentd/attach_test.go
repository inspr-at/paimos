// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
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
		"Waiting for your approval in Aeon · expires in 10 min",
		"Where  aeon.example/agents \u2192 Decision Desk",
		"Code   123 456 789",
		"\x1b]8;;https://aeon.example/agents#attach=123456789\x1b\\https://aeon.example/agents#attach=123456789\x1b]8;;\x1b\\\n",
		"you still review and allow",
		"Keep this terminal open. Ctrl-C cancels the request.",
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
		{"unreachable", "pending", "expired before it was allowed"},
		{"unreachable", "approved", "expired before it was allowed"},
		{"detached", "pending", "declined in Aeon or cancelled"},
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
		{"pending", at(-time.Second), "expired before it was allowed"},
		{"pending", at(3 * time.Second), "expired before it was allowed"},
		{"pending", at(5 * time.Minute), "declined, cancelled or expired in Aeon"},
		{"pending", nil, "declined, cancelled or expired in Aeon"},
		{"approved", at(5 * time.Minute), "approval ended before the attach started"},
		{"active", nil, "lost contact"},
	} {
		err := attachPollFailure(tc.previous, tc.expires, now)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "run attach again") && !strings.Contains(err.Error(), "run aeon-agentd attach again") {
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
		{"approved", "aeon", false, "Allowed in Aeon. Connecting"},
		{"approved", attachwatch.ConsentLocalAuth, false, "Confirm with Touch ID on this Mac; nothing is shared yet"},
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

func TestAttachLocalPresencePrecedesConfirmAndBrowser(t *testing.T) {
	for _, input := range []string{"\n", "y\n", "Y\n", "n\n", "ATTACH\n", "", "yes\n"} {
		t.Run(fmtInput(input), func(t *testing.T) {
			var out bytes.Buffer
			var steps []string
			preview := agentd.AttachLocalView{ID: "local-request", Digest: "snapshot-digest"}
			_, err := confirmAttachApproval(t.Context(), bufio.NewReader(strings.NewReader(input)), &out, &out, preview, "en", false, func(_ context.Context, in agentd.AttachLocalRequest) (agentd.AttachLocalView, error) {
				if in.Operation != "confirm" || in.ID != preview.ID || in.Digest != preview.Digest {
					t.Fatal("confirmation lost its local snapshot binding")
				}
				steps = append(steps, "confirm")
				return agentd.AttachLocalView{Origin: "https://paired.example", Code: "123456789", State: "pending"}, nil
			}, func(_ context.Context, link string) error {
				steps = append(steps, "open")
				if link != "https://paired.example/agents#attach=123456789" || !strings.Contains(out.String(), "https://paired.example/agents#attach=123456789") {
					t.Fatal("opener destination changed or fallback was not printed first")
				}
				return nil
			})
			accepted := input == "\n" || input == "y\n" || input == "Y\n"
			if (err == nil) != accepted {
				t.Fatalf("wrong presence decision: %v", err)
			}
			if accepted && strings.Join(steps, ",") != "confirm,open" || !accepted && len(steps) != 0 {
				t.Fatalf("presence/confirm/browser order: %v", steps)
			}
			if !strings.Contains(out.String(), "Next: approve in the browser window") || !strings.Contains(out.String(), "press Enter or y") {
				t.Fatal("next step or single-key local check hidden")
			}
		})
	}
}

func TestAttachConfirmFailureNeverOpensBrowser(t *testing.T) {
	var out bytes.Buffer
	_, err := confirmAttachApproval(t.Context(), bufio.NewReader(strings.NewReader("\n")), &out, &out, agentd.AttachLocalView{}, "en", false, func(context.Context, agentd.AttachLocalRequest) (agentd.AttachLocalView, error) {
		return agentd.AttachLocalView{}, errors.New("paired instance refused attach")
	}, func(context.Context, string) error { t.Fatal("opened after a refused local confirmation"); return nil })
	if err == nil || strings.Contains(out.String(), "#attach=") {
		t.Fatal("refusal printed an approval link")
	}
}

func TestAttachOwnerFailuresKeepTheirCauseInGerman(t *testing.T) {
	for code, word := range map[string]string{
		"attach_version_mismatch":       "Aktualisiere Aeon",
		"attach_pairing_revoked":        "gekoppelten Computer",
		"attach_ticket_not_visible":     "Projektzugriff",
		"attach_code_expired":           "abgelaufen",
		"attach_live_limit":             "Freigabe",
		"attach_draining":               "getrennt",
		"attach_enrollment_unavailable": "add-harness",
		"attach_registration_lost":      "Serverneustart",
		"attach_pairing_unavailable":    "Kontoberechtigungen",
		"attach_scope_changed":          "Projektzuordnung",
		"attach_computer_limit":         "8",
		"attach_attempt_limit":          "10 Minuten",
		"attach_registration_limit":     "Kapazität",
		"attach_poll_limit":             "Reihenfolge",
		"attach_rate_limit":             "Warte",
		"attach_snapshot_changed":       "Prozess",
		"attach_consent_required":       "Touch ID",
		"attach_ended":                  "genehmige",
		"attach_invalid_request":        "Anfrageformat",
		"attach_offline":                "Verbindung",
		"attach_server_unavailable":     "Serverprotokolle",
		"attach_unknown":                "Serverprotokollen",
	} {
		original := &agentd.AttachLocalError{Code: code, Hint: "English repair"}
		if attachLocalizedFailure(original, "en") != original {
			t.Fatal("English failure changed")
		}
		var detail *agentd.AttachLocalError
		if !errors.As(attachLocalizedFailure(original, "de"), &detail) || detail.Code != code || !strings.Contains(detail.Hint, word) {
			t.Fatalf("German repair missing for %s", code)
		}
	}
}

func fmtInput(input string) string {
	if input == "" {
		return "EOF"
	}
	if input == "\n" {
		return "Enter"
	}
	return strings.TrimSpace(input)
}

func TestAttachBrowserFailureDisableAndInvalidURLs(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name, origin, code, state string
		disabled                  bool
		expires                   *time.Time
		fail                      bool
		opens                     int
	}{
		{name: "opens", origin: "https://paired.example", code: "123456789", state: "pending", opens: 1},
		{name: "failure", origin: "https://paired.example", code: "123456789", state: "pending", fail: true, opens: 1},
		{name: "disabled", origin: "https://paired.example", code: "123456789", state: "pending", disabled: true},
		{name: "expired", origin: "https://paired.example", code: "123456789", state: "pending", expires: &expired},
		{name: "local review", origin: "https://paired.example", code: "123456789", state: "local_review"},
		{name: "code injection", origin: "https://paired.example", code: "123456789&x=1", state: "pending"},
		{name: "credentials", origin: "https://person:password@paired.example", code: "123456789", state: "pending"},
		{name: "query", origin: "https://paired.example?code=123456789", code: "123456789", state: "pending"},
		{name: "fragment", origin: "https://paired.example#other", code: "123456789", state: "pending"},
		{name: "path", origin: "https://paired.example/other", code: "123456789", state: "pending"},
		{name: "unsafe protocol", origin: "file:///tmp/approval", code: "123456789", state: "pending"},
		{name: "remote cleartext", origin: "http://paired.example", code: "123456789", state: "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			openAttachApproval(t.Context(), &out, agentd.AttachLocalView{Origin: tc.origin, Code: tc.code, State: tc.state, ExpiresAt: tc.expires}, "en", tc.disabled, func(ctx context.Context, _ string) error {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("opener has no timeout")
				}
				if tc.fail {
					return errors.New("opener output must not be printed")
				}
				return nil
			})
			if calls != tc.opens {
				t.Fatalf("opened %d times, want %d", calls, tc.opens)
			}
			if tc.fail && !strings.Contains(out.String(), "Open the printed link") {
				t.Fatal("fallback hidden")
			}
			if strings.Contains(out.String(), "opener output") {
				t.Fatal("raw opener error printed")
			}
		})
	}
}

func TestAttachCancellationNamesWhetherAnythingWasShared(t *testing.T) {
	for _, state := range []string{"pending", "approved"} {
		if got := attachCancelledLine(state); got != "Cancelled. Nothing was shared; Aeon shows the request as cancelled." {
			t.Fatal(got)
		}
	}
	if got := attachCancelledLine("active"); got != "Detached. Nothing more is shared." {
		t.Fatal(got)
	}
}
