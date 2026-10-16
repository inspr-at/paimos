// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import "sort"

const (
	reviewOrderLegacy = "legacy"
	reviewOrderSaved  = "saved"
)

// Stable ties retain saved priority within each family/tier, as before opt-in.
func sortLegacyReview[T any](steps []T, profile func(T) Profile) {
	sort.SliceStable(steps, func(i, j int) bool {
		a, b := profile(steps[i]), profile(steps[j])
		if reviewFamilyRank[a.Family] != reviewFamilyRank[b.Family] {
			return reviewFamilyRank[a.Family] < reviewFamilyRank[b.Family]
		}
		if a.Tier != b.Tier {
			return a.Tier == "frontier"
		}
		return false
	})
}
