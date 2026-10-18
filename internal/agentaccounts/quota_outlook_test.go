// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/capacity"
)

// series builds hourly weekly readings from `from` to `to` hours relative to now.
func series(now time.Time, from, to int, used func(k int) float64, reset func(k int) time.Time) []weeklySample {
	out := []weeklySample{}
	for k := from; k <= to; k++ {
		at := now.Add(time.Duration(k) * time.Hour)
		out = append(out, weeklySample{ReadAt: at, Used: used(k), ResetAt: reset(k)})
	}
	return out
}

func outlookAccountByID(g outlookGroup, id string) outlookAccount {
	for _, a := range g.Accounts {
		if a.AccountID == id {
			return a
		}
	}
	return outlookAccount{}
}

func near(t *testing.T, what string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || math.Abs(got.Sub(want).Minutes()) > 1 {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// Risk: the fuel gauge tells a person the pool lasts (or wastes quota) when the
// routing policy says otherwise. The fixtures replay measured hourly readings
// through the policy (soonest-resetting usable account runs, full at the limit,
// held accounts excluded) and pin the verdict, the moment the headline names,
// the safe pace, refill order and the honest states for missing readings.
func TestQuotaOutlookReplayVerdicts(t *testing.T) {
	now := time.Date(2026, 10, 18, 11, 10, 0, 0, time.UTC)
	anchor := now.Truncate(time.Hour)
	at := func(h float64) time.Time { return now.Add(time.Duration(h * float64(time.Hour))) }
	constant := func(v float64) func(int) float64 { return func(int) float64 { return v } }
	resetAt := func(h float64) func(int) time.Time { return func(int) time.Time { return at(h) } }

	t.Run("too fast: runs dry before a refill, names the stretch and the refilling account", func(t *testing.T) {
		// b burned 2.5 %/h until full 16 h ago; c refilled then and has burned 2.5 %/h since.
		b := series(now, -30, 0, func(k int) float64 { return math.Min(100, 65+2.5*float64(k+30)) }, resetAt(30))
		c := append(series(now, -30, -17, constant(90), resetAt(-16)),
			series(now, -16, 0, func(k int) float64 { return 2.5 * float64(k+16) }, resetAt(152))...)
		g := buildGroup("claude", []outlookInput{
			{ID: "a", Label: "account-a@", Harness: "claude", Limit: 100, Samples: series(now, -30, 0, constant(100), resetAt(60))},
			{ID: "b", Label: "account-b@", Harness: "claude", Limit: 100, Samples: b},
			{ID: "c", Label: "account-c@", Harness: "claude", Limit: 100, Samples: c},
		}, now, anchor)
		if g.State != "measured" || g.Verdict != "too_fast" || g.Headline.Kind != "runs_dry" {
			t.Fatalf("verdict: %s %s %+v", g.State, g.Verdict, g.Headline)
		}
		if *g.Pace.PerHour24h != 2.5 || *g.Pace.PerHour6h != 2.5 {
			t.Fatalf("measured pace: %v %v", *g.Pace.PerHour24h, *g.Pace.PerHour6h)
		}
		near(t, "runs dry", g.Headline.At, at(24))
		near(t, "back when b refills", g.Headline.Until, at(30))
		near(t, "focus", g.Headline.FocusAt, at(30))
		if g.Headline.AccountID != "b" {
			t.Fatalf("refilling account: %q", g.Headline.AccountID)
		}
		// Until c refills at +152 h the pool holds c's 60 points, then b's 100 (from
		// +30 h) and a's 100 (from +60 h): 260 / 152 h ≈ 1.71 %/h never runs dry.
		if s := *g.Pace.SafePerHour; s < 1.70 || s > 1.72 {
			t.Fatalf("safe pace: %v", s)
		}
		if len(g.Safe.Empty) != 0 || len(g.Current.Empty) != 2 {
			t.Fatalf("empty stretches: safe %d, current %d", len(g.Safe.Empty), len(g.Current.Empty))
		}
		if a := outlookAccountByID(g, "c"); a.State != "active" || a.Forecast.Kind != "empties" {
			t.Fatalf("c forecast: %+v %+v", a, a.Forecast)
		}
		if a := outlookAccountByID(g, "a"); a.State != "full" || a.Forecast.Kind != "waits" {
			t.Fatalf("a forecast: %+v", a.Forecast)
		}
		if g.Pool.Accounts != 3 || g.Pool.UsedPercent != 80 {
			t.Fatalf("pool: %+v", g.Pool)
		}
		// The measured line ends at the reading now and the projection starts there.
		if last := g.PastLine[len(g.PastLine)-1]; !last.At.Equal(now) || last.LeftPercent != 20 || g.Current.Points[0].LeftPercent != 20 {
			t.Fatalf("line seam: %+v %+v", last, g.Current.Points[0])
		}
		if len(g.PastRefills) != 1 || g.PastRefills[0].AccountID != "c" {
			t.Fatalf("measured refill: %+v", g.PastRefills)
		}
	})

	t.Run("too slow: quota refills unused; held accounts never run and stay out of the pool", func(t *testing.T) {
		g := buildGroup("claude", []outlookInput{
			{ID: "a", Label: "account-a@", Harness: "claude", Limit: 100, Samples: series(now, -30, 0, constant(28), resetAt(80))},
			{ID: "b", Label: "account-b@", Harness: "claude", Limit: 100, Samples: series(now, -30, 0, func(k int) float64 { return 70 + 0.5*float64(k) }, resetAt(30))},
			{ID: "c", Label: "account-c@", Harness: "claude", Limit: 100, Held: true, Samples: series(now, -30, 0, constant(2), resetAt(40))},
		}, now, anchor)
		if g.Verdict != "too_slow" || g.Headline.Kind != "unused" || g.Headline.AccountID != "a" || *g.Headline.UnusedPercent != 47 {
			t.Fatalf("verdict: %s %+v", g.Verdict, g.Headline)
		}
		near(t, "the refill that wastes most", g.Headline.At, at(80))
		if c := outlookAccountByID(g, "c"); c.State != "held" || c.Pooled || c.Forecast != nil {
			t.Fatalf("held account: %+v", c)
		}
		if g.Pool.Accounts != 2 {
			t.Fatalf("held account pooled: %+v", g.Pool)
		}
		for _, h := range g.Projected {
			if h.Burn[2] != 0 {
				t.Fatalf("held account burned at %v", h.HourAt)
			}
		}
		if b := outlookAccountByID(g, "b"); b.Forecast.Kind != "unused" || *b.Forecast.UnusedPercent != 15 {
			t.Fatalf("b forecast: %+v", b.Forecast)
		}
	})

	t.Run("on pace: the soonest-resetting usable account runs first, even with less room", func(t *testing.T) {
		g := buildGroup("codex", []outlookInput{
			{ID: "a", Label: "a@", Harness: "codex", Limit: 100, Samples: series(now, -30, 0, func(k int) float64 { return 60 + 1.4*float64(k) }, resetAt(20))},
			{ID: "b", Label: "b@", Harness: "codex", Limit: 100, Samples: series(now, -30, 0, constant(10), resetAt(100))},
		}, now, anchor)
		if g.Verdict != "on_pace" || g.Headline.Kind != "enough" || g.Headline.AccountID != "a" {
			t.Fatalf("verdict: %s %+v", g.Verdict, g.Headline)
		}
		near(t, "next refill", g.Headline.At, at(20))
		if outlookAccountByID(g, "a").State != "active" || outlookAccountByID(g, "b").State != "ready" {
			t.Fatal("the later-resetting account was chosen to run")
		}
		if g.Projected[1].Burn[0] == 0 || g.Projected[1].Burn[1] != 0 {
			t.Fatalf("refill order in the projection: %+v", g.Projected[1].Burn)
		}
		// After a refills (h 20) b resets sooner and takes over.
		if burn := g.Projected[22].Burn; burn[1] == 0 || burn[0] != 0 {
			t.Fatalf("hand-over after the refill: %+v", burn)
		}
		if *g.Pace.SafePerHour <= *g.Pace.PerHour24h {
			t.Fatalf("on pace but safe %v ≤ pace %v", *g.Pace.SafePerHour, *g.Pace.PerHour24h)
		}
	})

	t.Run("missing readings stay missing: no pool, no pace, no invented curve", func(t *testing.T) {
		g := buildGroup("cursor", []outlookInput{{ID: "f", Label: "f@", Harness: "cursor", Limit: 100}, {ID: "g", Label: "g@", Harness: "cursor", Limit: 100}}, now, anchor)
		if g.State != "not_reported" || g.Verdict != "unknown" || g.Pool != nil || g.Current != nil || len(g.History) != 0 {
			t.Fatalf("not reported: %+v", g)
		}
		for _, a := range g.Accounts {
			if a.State != "not_reported" || a.UsedPercent != nil {
				t.Fatalf("invented reading: %+v", a)
			}
		}
		one := buildGroup("codex", []outlookInput{
			{ID: "x", Label: "x@", Harness: "codex", Limit: 100, Samples: series(now, 0, 0, constant(40), resetAt(50))},
			{ID: "y", Label: "y@", Harness: "codex", Limit: 100},
		}, now, anchor)
		if one.State != "measured" || one.Pace.PerHour24h != nil || one.Current != nil || one.Verdict != "unknown" || one.Headline.Kind != "unknown" {
			t.Fatalf("a single reading claimed a pace: %+v", one.Pace)
		}
		if one.Pool.Accounts != 1 || outlookAccountByID(one, "y").State != "not_reported" {
			t.Fatalf("unreported account pooled: %+v", one.Pool)
		}
		for _, h := range one.History {
			if h.Known {
				t.Fatalf("history before the first reading claimed known: %v", h.HourAt)
			}
		}
		held := buildGroup("claude", []outlookInput{{ID: "h", Label: "h@", Harness: "claude", Limit: 100, Held: true, Samples: series(now, -2, 0, constant(10), resetAt(9))}}, now, anchor)
		if held.State != "held" || held.Headline.Kind != "held" || held.Pool != nil {
			t.Fatalf("all held: %+v", held)
		}
		private := buildGroup("claude", []outlookInput{{ID: "p", Label: "p@", Harness: "claude", Private: true, Limit: 100}}, now, anchor)
		if private.Accounts[0].State != "private" || private.Accounts[0].UsedPercent != nil {
			t.Fatalf("private account disclosed: %+v", private.Accounts[0])
		}
	})

	t.Run("a floor makes an account full before 100 %", func(t *testing.T) {
		g := buildGroup("codex", []outlookInput{
			{ID: "a", Label: "a@", Harness: "codex", Limit: 80, Samples: series(now, -10, 0, func(k int) float64 { return 70 + float64(k) }, resetAt(30))},
		}, now, anchor)
		// 10 points of room at 1 %/h: full at +10 h, empty until the refill at +30 h.
		if g.Verdict != "too_fast" {
			t.Fatalf("verdict with floor: %s", g.Verdict)
		}
		near(t, "full at the floor", g.Headline.At, at(10))
	})
}

// Risk: the outlook discloses another person's or tenant's quota, or answers
// agents that hold no person's view. Readings travel the real ingest path.
func TestQuotaOutlookScopeAndTenant(t *testing.T) {
	now := time.Date(2026, 10, 18, 11, 10, 0, 0, time.UTC)
	f := readinessWorld(t, "quota-outlook", now)
	f.mod = accountsMod()
	path := "/api/agent-accounts/" + f.account.ID + "/readings"
	for k := -6; k <= 0; k++ {
		at := now.Add(time.Duration(k) * time.Hour)
		v := capacity.Reading{WindowKind: "weekly", WindowMinutes: 10080, Bucket: "codex", UsedPercent: 40 + 2*float64(k+6), ReadAt: at, ResetsAt: now.Add(50 * time.Hour), Source: "harness"}
		status, raw := callAt(t, f.mod, &f.runner, f.token, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{v}}), at)
		if status != 204 {
			t.Fatalf("reading: %d %s", status, raw)
		}
	}
	read := func(want int) quotaOutlook {
		t.Helper()
		var out quotaOutlook
		status, raw := callAt(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/quota-outlook", "", now)
		if status != want || want == http.StatusOK && json.Unmarshal(raw, &out) != nil {
			t.Fatalf("outlook: %d %s", status, raw)
		}
		return out
	}
	out := read(http.StatusOK)
	if len(out.Groups) != 1 || out.Groups[0].Harness != "codex" || out.Groups[0].State != "measured" {
		t.Fatalf("own group: %+v", out.Groups)
	}
	g := out.Groups[0]
	// 48 points left at the measured 2 %/h last 24 h; the refill is 50 h away.
	if g.Pool.UsedPercent != 52 || g.Pace.PerHour24h == nil || *g.Pace.PerHour24h != 2 || g.Verdict != "too_fast" || len(g.History) != 169 || len(g.Projected) != 168 {
		t.Fatalf("outlook numbers: pool %+v pace %+v verdict %s history %d", g.Pool, g.Pace, g.Verdict, len(g.History))
	}
	callStatusAt(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/quota-outlook?days=9", "", now, 400, nil)
	callStatusAt(t, f.mod, nil, "", "GET", "/api/agent-accounts/quota-outlook", "", now, 401, nil)
	callStatusAt(t, f.mod, &f.runner, f.token, "GET", "/api/agent-accounts/quota-outlook", "", now, 403, nil)
	// Another person of the workspace, even an administrator, sees no one else's quota.
	peer := addPrincipal(t, f.admin.TenantID, "person", "Peer admin", []string{"admin"})
	var theirs quotaOutlook
	callStatusAt(t, f.mod, &peer, "", "GET", "/api/agent-accounts/quota-outlook", "", now, 200, &theirs)
	if len(theirs.Groups) != 0 {
		t.Fatalf("peer saw another person's quota: %+v", theirs.Groups)
	}
	// A different tenant reads nothing through RLS.
	other := makePrincipal(t, "quota-outlook-other", "person", "Other tenant", []string{"admin"})
	var foreign quotaOutlook
	callStatusAt(t, f.mod, &other, "", "GET", "/api/agent-accounts/quota-outlook", "", now, 200, &foreign)
	if len(foreign.Groups) != 0 {
		t.Fatalf("tenant isolation: %+v", foreign.Groups)
	}
	// A relinked binding hides the previous binding's readings.
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET link_revision=link_revision+1 WHERE id=$1`, f.account.ID); err != nil {
		t.Fatal(err)
	}
	if out := read(http.StatusOK); out.Groups[0].State != "not_reported" || out.Groups[0].Accounts[0].UsedPercent != nil {
		t.Fatalf("previous binding's quota survived relink: %+v", out.Groups[0])
	}
}

// Risk: two computers on one confirmed subscription are counted as two tanks,
// so the pool lasts longer than the subscription does and the pace doubles.
func TestQuotaOutlookSharedSubscriptionCountsOnce(t *testing.T) {
	now := time.Date(2026, 10, 18, 11, 10, 0, 0, time.UTC)
	anchor := now.Truncate(time.Hour)
	fp := strings.Repeat("ab", 32)
	soon := func(int) time.Time { return now.Add(8 * time.Hour) }
	far := func(int) time.Time { return now.Add(100 * time.Hour) }
	shared := append(
		series(now, -30, -10, func(k int) float64 { return 4 * float64(k+30) }, func(int) time.Time { return now.Add(-9 * time.Hour) }),
		series(now, -9, 0, func(k int) float64 { return 4 * float64(k+24) }, soon)...,
	)
	independent := series(now, -30, 0, func(int) float64 { return 10 }, far)
	g := buildGroup("claude", []outlookInput{
		{ID: "a", Label: "a@", Harness: "claude", Limit: 100, PoolFingerprint: fp, Samples: shared},
		{ID: "b", Label: "b@", Harness: "claude", Limit: 100, PoolFingerprint: fp, Samples: shared},
		{ID: "c", Label: "c@", Harness: "claude", Limit: 100, Samples: independent},
	}, now, anchor)
	one := buildGroup("claude", []outlookInput{
		{ID: "a", Label: "a@", Harness: "claude", Limit: 100, Samples: shared},
		{ID: "c", Label: "c@", Harness: "claude", Limit: 100, Samples: independent},
	}, now, anchor)
	if g.Pool == nil || g.Pool.Accounts != 2 || one.Pool == nil || g.Pool.UsedPercent != one.Pool.UsedPercent {
		t.Fatalf("shared subscription counted apart: %+v one %+v", g.Pool, one.Pool)
	}
	if g.Pace.PerHour24h == nil || one.Pace.PerHour24h == nil || *g.Pace.PerHour24h != *one.Pace.PerHour24h {
		t.Fatalf("pace doubled: %+v vs %+v", g.Pace.PerHour24h, one.Pace.PerHour24h)
	}
	if len(g.Accounts) != 3 || !outlookAccountByID(g, "a").Pooled || outlookAccountByID(g, "b").Pooled || outlookAccountByID(g, "b").Forecast != nil {
		t.Fatalf("enrollment rows: %+v %+v", outlookAccountByID(g, "a"), outlookAccountByID(g, "b"))
	}
	canonBurn, aliasBurn := false, false
	for _, h := range append(append([]outlookHour{}, g.History...), g.Projected...) {
		if len(h.Burn) > 0 && h.Burn[0] != 0 {
			canonBurn = true
		}
		if len(h.Burn) > 1 && h.Burn[1] != 0 {
			aliasBurn = true
		}
	}
	if !canonBurn || aliasBurn {
		t.Fatalf("burn stacked onto the alias: canon %v alias %v", canonBurn, aliasBurn)
	}
	if g.Verdict != one.Verdict || g.Headline.Kind != one.Headline.Kind {
		t.Fatalf("verdict %s/%s vs %s/%s", g.Verdict, g.Headline.Kind, one.Verdict, one.Headline.Kind)
	}
	if len(g.PastRefills) != 1 || g.PastRefills[0].AccountID != "a" || len(one.PastRefills) != 1 {
		t.Fatalf("refill doubled: %+v one %+v", g.PastRefills, one.PastRefills)
	}
}

// Risk: a weekly window that ended without a new reading is reported as
// refilled and full, so the pool looks empty of use.
func TestQuotaOutlookExpiredReadingDoesNotInventHeadroom(t *testing.T) {
	now := time.Date(2026, 10, 18, 11, 10, 0, 0, time.UTC)
	anchor := now.Truncate(time.Hour)
	reset := now.Add(-2 * time.Hour)
	samples := []weeklySample{
		{ReadAt: now.Add(-29 * time.Hour), Used: 20, ResetAt: reset},
		{ReadAt: now.Add(-5 * time.Hour), Used: 80, ResetAt: reset},
	}
	followed := append(append([]weeklySample{}, samples...), weeklySample{ReadAt: now.Add(-time.Hour), Used: 4, ResetAt: now.Add(20 * time.Hour)})
	if used, ok := usedAt(followed, reset); used != 0 || !ok {
		t.Fatalf("a later reading still refills the measured line: %v %v", used, ok)
	}
	g := buildGroup("claude", []outlookInput{{ID: "a", Label: "a@", Harness: "claude", Limit: 100, Samples: samples}}, now, anchor)
	a := g.Accounts[0]
	if a.UsedPercent == nil || *a.UsedPercent != 80 || a.Freshness != "expired" || a.Pooled || a.State == "full" || g.Pool != nil || g.State != "measured" {
		t.Fatalf("expired reading invented headroom: %+v pool %+v state %s", a, g.Pool, g.State)
	}
	if g.Current == nil || len(g.Current.Points) == 0 || g.Current.Points[0].LeftPercent != 100 {
		t.Fatalf("projection did not assume the weekly refill: %+v", g.Current)
	}
	for _, p := range g.PastLine {
		if !p.At.Before(reset) && p.LeftPercent > 50 {
			t.Fatalf("measured line jumped after the reset: %+v", p)
		}
	}
	fresh := series(now, -29, 0, func(int) float64 { return 10 }, func(int) time.Time { return now.Add(40 * time.Hour) })
	mixed := buildGroup("claude", []outlookInput{
		{ID: "a", Label: "a@", Harness: "claude", Limit: 100, Samples: samples},
		{ID: "b", Label: "b@", Harness: "claude", Limit: 100, Samples: fresh},
	}, now, anchor)
	if mixed.Pool == nil || mixed.Pool.Accounts != 1 || mixed.Pool.UsedPercent != 10 {
		t.Fatalf("expired account joined the average: %+v", mixed.Pool)
	}
}

func outlookBody(t *testing.T, now time.Time, g outlookGroup) []byte {
	t.Helper()
	raw, err := json.Marshal(quotaOutlook{
		GeneratedAt: now, AnchorHour: now.Truncate(time.Hour),
		HistoryHours: int(outlookWindow / time.Hour), HorizonHours: int(outlookHorizon / time.Hour),
		Groups: []outlookGroup{g},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func risingPair(now time.Time) outlookGroup {
	anchor := now.Truncate(time.Hour)
	reset := func(int) time.Time { return now.Add(80 * time.Hour) }
	return buildGroup("claude", []outlookInput{
		{ID: "11111111-1111-4111-8111-111111111111", Label: "a@", Harness: "claude", Limit: 100, Samples: series(now, -24, 0, func(k int) float64 { return 10 + float64(k+24) }, reset)},
		{ID: "22222222-2222-4222-8222-222222222222", Label: "b@", Harness: "claude", Limit: 100, Samples: series(now, -24, 0, func(k int) float64 { return 2 * float64(k+24) }, reset)},
	}, now, anchor)
}

// Risk: sharing revoked after the outlook was built still leaves the private
// account's quota in the pool, the burn and the projection.
func TestQuotaOutlookRedactionWithholdsRevokedAggregates(t *testing.T) {
	now := time.Date(2026, 10, 18, 11, 10, 0, 0, time.UTC)
	const idA = "11111111-1111-4111-8111-111111111111"
	const idB = "22222222-2222-4222-8222-222222222222"
	g := risingPair(now)
	if g.Pool == nil || g.Pool.UsedPercent != 41 || g.Accounts[0].UsedPercent == nil || *g.Accounts[0].UsedPercent != 34 || *g.Accounts[1].UsedPercent != 48 {
		t.Fatalf("fixture pool: %+v %+v %+v", g.Pool, g.Accounts[0].UsedPercent, g.Accounts[1].UsedPercent)
	}
	body := outlookBody(t, now, g)
	allow := accountprivacy.Policy{idA: true, idB: true}
	if got, err := redactOutlook(body, allow); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("allow-all changed the body: %v equal %v", err, err == nil && bytes.Equal(got, body))
	}
	raw, err := redactOutlook(body, accountprivacy.Policy{idA: true})
	if err != nil {
		t.Fatal(err)
	}
	var out quotaOutlook
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	got := out.Groups[0]
	a, b := outlookAccountByID(got, idA), outlookAccountByID(got, idB)
	if b.State != "private" || b.UsedPercent != nil || b.Forecast != nil || b.Pooled {
		t.Fatalf("revoked row: %+v", b)
	}
	if a.UsedPercent == nil || *a.UsedPercent != 34 || a.Forecast != nil {
		t.Fatalf("remaining row: %+v forecast %v", a, a.Forecast)
	}
	if got.Pool == nil || got.Pool.Accounts != 1 || got.Pool.UsedPercent != 34 {
		t.Fatalf("pool kept the revoked account: %+v", got.Pool)
	}
	if got.Current != nil || got.Safe != nil || len(got.Projected) != 0 || got.Verdict != "unknown" || got.Headline.Kind != "unknown" {
		t.Fatalf("joint projection survived: verdict %s headline %s current %v projected %d", got.Verdict, got.Headline.Kind, got.Current != nil, len(got.Projected))
	}
	if got.Pace.PerHour24h != nil || got.Pace.PerHour6h != nil || got.Pace.SafePerHour != nil || got.Pace.MeasuredHours != 0 {
		t.Fatalf("pace survived: %+v", got.Pace)
	}
	aBurn, bBurn := false, false
	for _, h := range got.History {
		if len(h.Burn) > 0 && h.Burn[0] != 0 {
			aBurn = true
		}
		if len(h.Burn) > 1 && h.Burn[1] != 0 {
			bBurn = true
		}
	}
	if !aBurn || bBurn {
		t.Fatalf("history burn: A %v B %v", aBurn, bBurn)
	}
	if bytes.Contains(raw, []byte(`"projected":null`)) || bytes.Contains(raw, []byte(`"past_line":null`)) {
		t.Fatal("withheld curves marshaled as null")
	}
	raw, err = redactOutlook(body, accountprivacy.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	gone := out.Groups[0]
	if gone.Accounts[0].UsedPercent != nil || gone.Accounts[1].UsedPercent != nil || gone.Pool != nil || gone.Current != nil {
		t.Fatalf("fully revoked outlook still disclosed quota: pool %+v", gone.Pool)
	}
}
