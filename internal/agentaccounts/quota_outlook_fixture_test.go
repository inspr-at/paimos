// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// The web dial's browser fixtures are this package's own projection of
// simulated readings, so the UI is tested against the server's exact shape and
// numbers. AEON_UPDATE_QUOTA_FIXTURE=1 rewrites the file; otherwise it must match.
const quotaFixturePath = "../../web/tests/fixtures/quota-outlook.json"

// A day's relative burn by hour: quiet nights, busy afternoons.
var quotaProfile = []float64{.5, .45, .42, .4, .42, .5, .62, .8, 1.02, 1.18, 1.25, 1.28, 1.2, 1.18, 1.3, 1.34, 1.32, 1.28, 1.2, 1.12, 1.08, 1.02, .88, .7}

type simAccount struct {
	id, label string
	used      float64
	reset     time.Time // the next refill after the simulation's start
	held      bool
}

// simulateReadings replays a week of hourly use through the policy, one
// reading per account and hour, so history, pace and refills are consistent.
func simulateReadings(now time.Time, accounts []simAccount, rate func(h int) float64) map[string][]weeklySample {
	out := map[string][]weeklySample{}
	for h := -168; h <= 0; h++ {
		at := now.Add(time.Duration(h) * time.Hour)
		for i := range accounts {
			for !accounts[i].reset.After(at) {
				accounts[i].reset = accounts[i].reset.Add(outlookWindow)
				accounts[i].used = 0
			}
		}
		need := rate(h)
		for need > 1e-9 {
			k := -1
			for i, a := range accounts {
				if !a.held && a.used < 100 && (k < 0 || a.reset.Before(accounts[k].reset)) {
					k = i
				}
			}
			if k < 0 {
				break
			}
			take := math.Min(need, 100-accounts[k].used)
			accounts[k].used += take
			need -= take
		}
		for _, a := range accounts {
			out[a.id] = append(out[a.id], weeklySample{ReadAt: at, Used: math.Round(a.used*10) / 10, ResetAt: a.reset})
		}
	}
	return out
}

func quotaFixture() map[string]quotaOutlook {
	now := time.Date(2026, 10, 18, 11, 10, 0, 0, time.UTC)
	day := func(h int) float64 { return quotaProfile[(now.Add(time.Duration(h)*time.Hour).Hour()+24)%24] }
	weekBefore := func(t time.Time) time.Time { return t.Add(-outlookWindow) }
	group := func(harness string, accounts []simAccount, rate func(int) float64) []outlookInput {
		readings := simulateReadings(now, accounts, rate)
		inputs := []outlookInput{}
		for _, a := range accounts {
			in := outlookInput{ID: a.id, Label: a.label, Harness: harness, Limit: 100, Held: a.held, Samples: readings[a.id], Freshness: "fresh"}
			inputs = append(inputs, in)
		}
		return inputs
	}
	claudeResets := []time.Time{time.Date(2026, 10, 21, 18, 59, 0, 0, time.UTC), time.Date(2026, 10, 19, 20, 59, 0, 0, time.UTC), time.Date(2026, 10, 23, 0, 59, 0, 0, time.UTC)}
	claude := func(used [3]float64, held bool, level func(int) float64) []outlookInput {
		return group("claude", []simAccount{
			{"0e6f0000-0000-4000-8000-00000000000a", "account-a@", used[0], weekBefore(claudeResets[0]), false},
			{"0e6f0000-0000-4000-8000-00000000000b", "account-b@", used[1], weekBefore(claudeResets[1]), false},
			{"0e6f0000-0000-4000-8000-00000000000c", "account-c@", used[2], weekBefore(claudeResets[2]), held},
		}, func(h int) float64 { return level(h) * day(h) })
	}
	codex := group("codex", []simAccount{
		{"0e6f0000-0000-4000-8000-00000000000d", "account-d@", 30, weekBefore(time.Date(2026, 10, 20, 9, 12, 0, 0, time.UTC)), false},
		{"0e6f0000-0000-4000-8000-00000000000e", "account-e@", 20, weekBefore(time.Date(2026, 10, 22, 16, 40, 0, 0, time.UTC)), false},
	}, func(h int) float64 { return 1.45 * day(h) })
	cursor := []outlookInput{
		{ID: "0e6f0000-0000-4000-8000-00000000000f", Label: "account-f@", Harness: "cursor", Limit: 100, Freshness: "unknown"},
		{ID: "0e6f0000-0000-4000-8000-000000000010", Label: "account-g@", Harness: "cursor", Limit: 100, Freshness: "unknown"},
	}
	fast := claude([3]float64{40, 60, 70}, false, func(h int) float64 {
		if h >= -56 && h < -53 {
			return 0
		}
		return 2.3
	})
	slow := claude([3]float64{10, 30, 20}, true, func(h int) float64 {
		if h < -30 {
			return 2.1
		}
		return 0.6
	})
	build := func(groups ...[]outlookInput) quotaOutlook {
		all := []outlookInput{}
		for _, g := range groups {
			all = append(all, g...)
		}
		return buildOutlook(all, now)
	}
	// An elapsed reset has no measured refill until a later reading confirms it.
	// The browser uses this real response to check both the line and its markers.
	expired := []outlookInput{{ID: "expired", Label: "expired@", Harness: "claude", Limit: 100, Samples: []weeklySample{
		{ReadAt: now.Add(-29 * time.Hour), Used: 20, ResetAt: now.Add(-2 * time.Hour)},
		{ReadAt: now.Add(-5 * time.Hour), Used: 80, ResetAt: now.Add(-2 * time.Hour)},
	}}}
	return map[string]quotaOutlook{"fast": build(fast, codex, cursor), "slow": build(slow, codex, cursor), "expired": build(expired)}
}

// Risk: the browser spec draws a shape the server never sends. The fixture is
// regenerated from the projection and pinned; it also keeps the scenarios the
// spec relies on (too fast with an empty stretch, too slow with a held account).
func TestQuotaOutlookWebFixtureIsCurrent(t *testing.T) {
	fixture := quotaFixture()
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if os.Getenv("AEON_UPDATE_QUOTA_FIXTURE") == "1" {
		if err := os.WriteFile(quotaFixturePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if g := fixture["fast"].Groups[0]; g.Harness != "claude" || g.Verdict != "too_fast" || len(g.Current.Empty) == 0 {
		t.Fatalf("fast scenario: %s %+v", g.Verdict, g.Headline)
	}
	if g := fixture["slow"].Groups[0]; g.Verdict != "too_slow" || g.Accounts[2].State != "held" {
		t.Fatalf("slow scenario: %s %+v", g.Verdict, g.Headline)
	}
	if g := fixture["fast"].Groups[1]; g.Harness != "codex" || g.Verdict != "on_pace" {
		t.Fatalf("codex scenario: %s", g.Verdict)
	}
	if g := fixture["fast"].Groups[2]; g.Harness != "cursor" || g.State != "not_reported" {
		t.Fatalf("cursor scenario: %s", g.State)
	}
	if g := fixture["expired"].Groups[0]; g.Pool != nil || len(g.PastRefills) != 0 {
		t.Fatalf("expired evidence invented measured headroom or refills: %+v %+v", g.Pool, g.PastRefills)
	}
	current, err := os.ReadFile(quotaFixturePath)
	if err != nil || !bytes.Equal(current, raw) {
		t.Fatalf("web/tests/fixtures/quota-outlook.json is out of date; rerun with AEON_UPDATE_QUOTA_FIXTURE=1 (%v)", err)
	}
}
