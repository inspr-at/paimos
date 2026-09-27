// SPDX-License-Identifier: AGPL-3.0-only

package harness

import "testing"

func usagePtr[T any](v T) *T { return &v }

func TestUsageExactCost(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		in, out, cached                        int64
		inputRate, outputRate, cacheRate, want string
	}{
		{"cached subset", 100, 20, 40, "2.500000", "10.000000", "0.250000", "0.000360000000"},
		{"sub micro precision", 1, 0, 0, "0.000001", "0", "0", "0.000000000001"},
		{"maximum counts and prices", maxUsageCount, maxUsageCount, 0, "1000000", "1000000", "1000000", "2000000000000.000000000000"},
		{"all cached", 100, 0, 100, "100", "100", "0.125", "0.000012500000"},
		{"measured zero", 0, 0, 0, "1000000", "1000000", "1000000", "0.000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := SessionModelUsage{InputTokens: &tc.in, OutputTokens: &tc.out, CachedInputTokens: &tc.cached}
			p := ModelPrice{InputUSDPerMillion: tc.inputRate, OutputUSDPerMillion: tc.outputRate, CachedInputUSDPerMillion: tc.cacheRate}
			got := estimateUsageCost(u, p)
			if got == nil || *got != tc.want {
				t.Fatalf("cost %v, want %s", got, tc.want)
			}
			u.CachedInputTokens = nil
			if estimateUsageCost(u, p) != nil {
				t.Fatal("unknown cache produced a cost")
			}
		})
	}
}

func TestUsageRateValidation(t *testing.T) {
	for _, raw := range []string{"-1", "NaN", "Infinity", "1e3", "1/2", "01", "+1", ".5", "1.", "1.0000001", "1000000.000001", "1000001", " 1", ""} {
		t.Run(raw, func(t *testing.T) {
			if _, err := normalizeUsageRate(raw); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
	for raw, want := range map[string]string{"0": "0.000000", "1.25": "1.250000", "1000000": "1000000.000000"} {
		got, err := normalizeUsageRate(raw)
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", raw, got, err)
		}
	}
}

func TestUsageTransition(t *testing.T) {
	old := SessionModelUsage{Sequence: 3, InputTokens: usagePtr(int64(100)), OutputTokens: usagePtr(int64(20)), CachedInputTokens: usagePtr(int64(40)), Provisional: true, BillingMode: "unknown"}
	for _, tc := range []struct {
		name   string
		change func(*SessionModelUsage)
		valid  bool
	}{
		{"advance", func(n *SessionModelUsage) { n.InputTokens = usagePtr(int64(110)) }, true},
		{"same counts", func(n *SessionModelUsage) {}, true},
		{"finalize", func(n *SessionModelUsage) { n.Provisional = false }, true},
		{"stale sequence", func(n *SessionModelUsage) { n.Sequence = 3 }, false},
		{"input decreases", func(n *SessionModelUsage) { n.InputTokens = usagePtr(int64(99)) }, false},
		{"output decreases", func(n *SessionModelUsage) { n.OutputTokens = usagePtr(int64(19)) }, false},
		{"cache decreases", func(n *SessionModelUsage) { n.CachedInputTokens = usagePtr(int64(39)) }, false},
		{"uncached decreases", func(n *SessionModelUsage) { n.CachedInputTokens = usagePtr(int64(41)) }, false},
		{"known becomes unknown", func(n *SessionModelUsage) { n.OutputTokens = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := old
			next.Sequence++
			tc.change(&next)
			if err := usageTransition(old, next); (err == nil) != tc.valid {
				t.Fatalf("transition: %v", err)
			}
		})
	}
	old.Provisional = false
	next := old
	next.Sequence++
	if err := usageTransition(old, next); err != nil {
		t.Fatal(err)
	}
	next.AccountLabel = usagePtr("different")
	if usageTransition(old, next) == nil {
		t.Fatal("final metadata changed")
	}
	next = old
	next.Sequence++
	next.Provisional = true
	if usageTransition(old, next) == nil {
		t.Fatal("final became provisional")
	}
}
