// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/servicetier"
)

func TestTierUsageDoesNotRepriceEarlierTokens(t *testing.T) {
	r := servicetier.Advertised("codex", "gpt-6.1-sol", "0.159.2")
	// Synthetic stored snapshot for accounting math, not a vendor catalog pin.
	priceMultiplier := 2.0
	r.Tiers[1].Offered, r.Tiers[1].PriceMultiplier = true, &priceMultiplier
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

func TestUnpublishedTierUsageHasNoEstimatedCost(t *testing.T) {
	r := servicetier.Advertised("codex", "gpt-6.1-sol", "0.159.2")
	s := Session{Harness: "codex", ServiceTierReports: []servicetier.Report{r}}
	for _, tier := range []string{"fast", "fastest"} {
		next := SessionModelUsage{Model: r.Model, InputTokens: usagePtr(int64(100)), OutputTokens: usagePtr(int64(0)), CachedInputTokens: usagePtr(int64(0))}
		if err := usageTierSegments(s, SessionModelUsage{}, &next, &tier); err != nil {
			t.Fatal(err)
		}
		segment := next.TierSegments[0]
		if segment.PriceMultiplier != nil || segment.UsageMultiplier != nil || estimateTierUsageCost(next, ModelPrice{InputUSDPerMillion: "1", OutputUSDPerMillion: "1", CachedInputUSDPerMillion: "1"}) != nil {
			t.Fatalf("unpublished %s acquired invented multipliers or cost", tier)
		}
	}
}

func TestTierUsageTestFormatting(t *testing.T) {
	source, err := os.ReadFile("tier_usage_test.go")
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, formatted) {
		t.Fatal("tier_usage_test.go needs gofmt")
	}
	// gofmt alone does not separate standard-library and external imports.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "tier_usage_test.go", source, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	previousLine := 0
	externalSeen := false
	for _, imp := range f.Imports {
		external := strings.HasPrefix(imp.Path.Value, `"github.com/`)
		line := fset.Position(imp.Pos()).Line
		if external && !externalSeen && previousLine != 0 && line-previousLine < 2 {
			t.Fatal("external imports must be grouped apart from standard-library imports")
		}
		if !external && externalSeen {
			t.Fatal("standard-library imports must precede external imports")
		}
		externalSeen = externalSeen || external
		previousLine = line
	}
}

func TestTierModelTimeKeepsToolAndWaitTime(t *testing.T) {
	u := SessionModelUsage{ModelTimeMS: usagePtr(int64(600000)), TierSegments: []TierUsageSegment{
		{ModelTimeMS: usagePtr(int64(200000)), SpeedFactor: usagePtr(1.0)},
		{ModelTimeMS: usagePtr(int64(400000)), SpeedFactor: usagePtr(2.0)},
	}}
	got := tierModelTime(u, 900000, usagePtr(2.0))
	if got == nil || *got != 800000 {
		t.Fatalf("model scaled incorrectly: %v", got)
	}
	u.TierSegments[1].ModelTimeMS = nil
	if tierModelTime(u, 900000, usagePtr(2.0)) != nil {
		t.Fatal("missing model time became an estimate")
	}
	if tierModelTime(u, 100, usagePtr(2.0)) != nil {
		t.Fatal("invalid measured duration became an estimate")
	}
	if projectTierCost("2.400000000000", usagePtr(2.0)) == nil || *projectTierCost("2.400000000000", usagePtr(2.0)) != "4.800000000000" {
		t.Fatal("price projection rounded incorrectly")
	}
}
func TestTierRunCostRequiresFrozenPricesAndTier(t *testing.T) {
	tier := "fast"
	u := SessionModelUsage{BillingMode: "api", EstimatedCostUSD: usagePtr("4.800000000000"), PriceVersion: usagePtr(int64(1)), InputTokens: usagePtr(int64(2400000)), OutputTokens: usagePtr(int64(0)), CachedInputTokens: usagePtr(int64(0)), TierSegments: []TierUsageSegment{{Tier: &tier, PriceMultiplier: usagePtr(2.0)}}}
	price := ModelPrice{InputUSDPerMillion: "1", OutputUSDPerMillion: "1", CachedInputUSDPerMillion: "1"}
	cost := tierRunCost(u, price, "run")
	if cost == nil || cost.DefaultCost != "2.400000000000" || cost.Segments[0].Multiplier != 2 {
		t.Fatalf("cost lost frozen multiplier: %+v", cost)
	}
	u.TierSegments[0].PriceMultiplier = nil
	if tierRunCost(u, price, "run") != nil {
		t.Fatal("unknown multiplier projected")
	}
	u.BillingMode = "subscription"
	if tierRunCost(u, price, "run") != nil {
		t.Fatal("subscription became USD bill")
	}
}
