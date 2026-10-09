// SPDX-License-Identifier: AGPL-3.0-only
package agentplan

import (
	"encoding/json"
	"testing"
	"time"
)

// Risk: a malformed control or shifted midnight silently raises allowance.
func TestDailySettingsValidationLocalDayAndLimitMath(t *testing.T) {
	valid := `{"total":5,"daily":{"codex":{"pace":{"mode":"pace","points_per_day":null},"boost_today":null,"at_limit":"ladder"}}}`
	p, _, err := Decode([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if !SameLimits(p, Plan{Total: 5}) {
		t.Fatal("missing settings differ from default")
	}
	for _, daily := range []string{`null`, `{"codex":null}`, `{"unknown":{"pace":{"mode":"pace","points_per_day":null},"boost_today":null,"at_limit":"ladder"}}`, `{"codex":{"pace":{"mode":"pace","points_per_day":0},"boost_today":null,"at_limit":"ladder"}}`, `{"codex":{"pace":{"mode":"pace","points_per_day":51},"boost_today":null,"at_limit":"ladder"}}`, `{"codex":{"pace":{"mode":"pace"},"boost_today":null,"at_limit":"ladder"}}`, `{"codex":{"pace":{"mode":"pace","points_per_day":null},"boost_today":{"entered_as":"used","until":"2026-10-10T00:00:00Z"},"at_limit":"ladder"}}`, `{"codex":{"pace":{"mode":"pace","points_per_day":null},"boost_today":{"limit_used_pct":null,"entered_as":"used","until":"2026-10-10T00:00:00Z"},"at_limit":"ladder"}}`, `{"codex":{"pace":{"mode":"pace","points_per_day":null},"boost_today":null,"at_limit":"ladder","extra":true}}`} {
		if _, _, err := Decode([]byte(`{"total":5,"daily":` + daily + `}`)); err == nil {
			t.Errorf("accepted %s", daily)
		}
	}
	for _, tc := range []struct {
		at    string
		hours time.Duration
	}{{"2026-03-29T12:00:00Z", 23}, {"2026-10-25T12:00:00Z", 25}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		start, end, err := LocalDay(at, "Europe/Vienna")
		if err != nil || end.Sub(start) != tc.hours*time.Hour {
			t.Fatalf("local day %s %s %v", start, end, err)
		}
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, mode           string
		boost                *DailyBoost
		limit, allowed, over float64
	}{
		{"pace", "pace", nil, 50, 10, 2},
		{"everything floor", "everything", nil, 80, 40, 0},
		{"raise", "pace", &DailyBoost{LimitUsedPct: 70, EnteredAs: "left", Until: now.Add(time.Hour)}, 70, 30, 2},
		{"lower", "pace", &DailyBoost{LimitUsedPct: 45, EnteredAs: "used", Until: now.Add(time.Hour)}, 45, 5, 2},
		{"expired", "pace", &DailyBoost{LimitUsedPct: 70, EnteredAs: "used", Until: now}, 50, 10, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := DefaultDaily()
			d.Pace.Mode = tc.mode
			d.BoostToday = tc.boost
			a := DailyAccount{UsedPct: Number(52), StartOfDayUsedPct: Number(40), FloorPct: Number(20)}
			if err := ApplyDaily(&a, d, 10, now); err != nil {
				t.Fatal(err)
			}
			if *a.LimitUsedPct != tc.limit || *a.LeftPct != 48 || *a.TodayPointsAllowed != tc.allowed || *a.OverPacePoints != tc.over {
				t.Fatalf("wrong maths: %+v", a)
			}
		})
	}
	raw, err := json.Marshal(Snapshot{Plan: Default(), DailyState: map[string]DailyState{}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["daily"]) != "{}" {
		t.Fatalf("daily omitted: %s", raw)
	}
}

// Risk: one exhausted account hides another door, or unknown use appears free.
func TestDailyHarnessSummaryAndUnknownUsage(t *testing.T) {
	makeAccount := func(id string, used float64) DailyAccount {
		a := DailyAccount{AccountID: id, UsedPct: Number(used), StartOfDayUsedPct: Number(40), FloorPct: Number(0), Freshness: "fresh", Routable: true}
		if err := ApplyDaily(&a, DefaultDaily(), 10, time.Now()); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b := makeAccount("a", 52), makeAccount("b", 48)
	out := SummarizeDaily([]DailyAccount{a, b})
	if out.State != "on_pace" || out.ActiveAccountID == nil || *out.ActiveAccountID != "b" || *out.TodayPointsUsed != 8 {
		t.Fatalf("lost second account: %+v", out)
	}
	b.UsedPct = Number(50)
	out = SummarizeDaily([]DailyAccount{a, b})
	if out.State != "at_limit" {
		t.Fatal("all-account limit lost")
	}
	a.LimitUsedPct = Number(60)
	out = SummarizeDaily([]DailyAccount{a})
	if out.State != "over_pace" || *out.OverPacePoints != 2 {
		t.Fatal("over pace means above daily share")
	}
	out = SummarizeDaily([]DailyAccount{{AccountID: "private", Freshness: "unknown", DetailsRedacted: true}})
	if out.State != "at_limit" || out.LimitUsedPct != nil || out.TodayPointsUsed != nil {
		t.Fatal("unknown usage invented headroom")
	}
	out = SummarizeDaily([]DailyAccount{{AccountID: "payg", NoDailyLimit: true, Routable: true}})
	if out.State != "no_limit" {
		t.Fatal("payg daily cap invented")
	}
}
