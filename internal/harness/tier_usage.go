// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"math/big"
	"strconv"

	"github.com/inspr-at/paimos/internal/workorders"
)

// Segments retain the multiplier at the observation boundary. Counters remain
// vendor tokens; weighted estimates never replace billed vendor telemetry.
type TierUsageSegment struct {
	Tier              *string  `json:"tier"`
	PriceMultiplier   *float64 `json:"price_multiplier"`
	UsageMultiplier   *float64 `json:"usage_multiplier"`
	InputTokens       *int64   `json:"input_tokens"`
	OutputTokens      *int64   `json:"output_tokens"`
	CachedInputTokens *int64   `json:"cached_input_tokens"`
}

func usageTierSegments(s Session, old SessionModelUsage, next *SessionModelUsage, reported *string) error {
	tier := reported
	if tier == nil {
		tier = s.ServiceTier
	}
	next.ServiceTier = tier
	segment := TierUsageSegment{Tier: tier}
	if tier == nil {
		// Legacy reporters keep the established list-price estimate. They do
		// not acquire an asserted active tier merely by reporting tokens.
		one := 1.0
		segment.PriceMultiplier = &one
	} else if cap, ok := sessionTierReport(s, next.Model).Find(*tier); ok {
		segment.PriceMultiplier, segment.UsageMultiplier = cap.PriceMultiplier, cap.UsageMultiplier
	}
	segments := append([]TierUsageSegment{}, old.TierSegments...)
	if len(segments) == 0 && old.Model != "" {
		one := 1.0
		segments = append(segments, TierUsageSegment{Tier: old.ServiceTier, PriceMultiplier: &one, InputTokens: old.InputTokens, OutputTokens: old.OutputTokens, CachedInputTokens: old.CachedInputTokens})
	}
	index := -1
	for i, v := range segments {
		if sameUsageValue(v.Tier, segment.Tier) && sameUsageValue(v.PriceMultiplier, segment.PriceMultiplier) && sameUsageValue(v.UsageMultiplier, segment.UsageMultiplier) {
			index = i
			break
		}
	}
	// A single unchanged segment can resolve previously unknown counters.
	if len(segments) == 1 && index == 0 {
		segments[0].InputTokens, segments[0].OutputTokens, segments[0].CachedInputTokens = next.InputTokens, next.OutputTokens, next.CachedInputTokens
	} else {
		delta := func(before, after *int64) *int64 {
			if after == nil || (before == nil && old.Model != "") {
				return nil
			}
			n := *after
			if before != nil {
				n -= *before
			}
			return &n
		}
		segment.InputTokens, segment.OutputTokens, segment.CachedInputTokens = delta(old.InputTokens, next.InputTokens), delta(old.OutputTokens, next.OutputTokens), delta(old.CachedInputTokens, next.CachedInputTokens)
		if index < 0 {
			if len(segments) >= 128 {
				return workorders.Fail(429, "tier usage segment limit reached")
			}
			segments = append(segments, segment)
		} else {
			add := func(a, b *int64) *int64 {
				if a == nil || b == nil {
					return nil
				}
				n := *a + *b
				return &n
			}
			v := &segments[index]
			v.InputTokens, v.OutputTokens, v.CachedInputTokens = add(v.InputTokens, segment.InputTokens), add(v.OutputTokens, segment.OutputTokens), add(v.CachedInputTokens, segment.CachedInputTokens)
		}
	}
	next.TierSegments = segments
	return nil
}
func estimateTierUsageCost(u SessionModelUsage, price ModelPrice) *string {
	if len(u.TierSegments) == 0 {
		return estimateUsageCost(u, price)
	}
	total := new(big.Rat)
	for _, segment := range u.TierSegments {
		if segment.PriceMultiplier == nil {
			return nil
		}
		base := estimateUsageCost(SessionModelUsage{InputTokens: segment.InputTokens, OutputTokens: segment.OutputTokens, CachedInputTokens: segment.CachedInputTokens}, price)
		if base == nil {
			return nil
		}
		value, ok := new(big.Rat).SetString(*base)
		if !ok {
			return nil
		}
		multiplier, ok := new(big.Rat).SetString(strconv.FormatFloat(*segment.PriceMultiplier, 'f', -1, 64))
		if !ok {
			return nil
		}
		total.Add(total, value.Mul(value, multiplier))
	}
	result := total.FloatString(12)
	return &result
}
