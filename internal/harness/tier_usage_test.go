// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"github.com/inspr-at/paimos/internal/servicetier"
	"testing"
)

func TestTierUsageDoesNotRepriceEarlierTokens(t *testing.T) {
	r := servicetier.Advertised("codex", "gpt-6.1-sol", "0.159.2")
	s := Session{Harness: "codex", ServiceTierReports: []servicetier.Report{r}}
	price := ModelPrice{InputUSDPerMillion: "1", OutputUSDPerMillion: "1", CachedInputUSDPerMillion: "1"}
	var old SessionModelUsage
	for i, tc := range []struct {
		tier   string
		tokens int64
		want   string
	}{{"default", 100, "0.000100000000"}, {"fast", 200, "0.000300000000"}, {"default", 300, "0.000400000000"}} {
		next := SessionModelUsage{Model: "gpt-6.1-sol", Sequence: int64(i + 1), InputTokens: usagePtr(tc.tokens), OutputTokens: usagePtr(int64(0)), CachedInputTokens: usagePtr(int64(0))}
		if err := usageTierSegments(s, old, &next, &tc.tier); err != nil {
			t.Fatal(err)
		}
		cost := estimateTierUsageCost(next, price)
		if cost == nil || *cost != tc.want {
			t.Fatalf("%s cost=%v, want %s", tc.tier, cost, tc.want)
		}
		old = next
	}
	if len(old.TierSegments) != 2 {
		t.Fatal("segments did not coalesce by frozen multiplier")
	}
	unknown := "fastest"
	next := old
	next.InputTokens = usagePtr(int64(400))
	if err := usageTierSegments(s, old, &next, &unknown); err != nil {
		t.Fatal(err)
	}
	if estimateTierUsageCost(next, price) != nil {
		t.Fatal("unpublished price estimated")
	}
}
