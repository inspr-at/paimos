// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

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

func storedReviewOrder(ctx context.Context, tx pgx.Tx) (string, error) {
	var mode string
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT ordinary_review_order_mode FROM model_review_order), 'legacy')`).Scan(&mode)
	return mode, err
}

// Called only inside the final authorized tenant/registry fence, before events.
func saveReviewOrder(ctx context.Context, tx pgx.Tx, tenantID, mode string) error {
	_, err := tx.Exec(ctx, `INSERT INTO model_review_order(tenant_id,ordinary_review_order_mode)
        VALUES($1,$2) ON CONFLICT(tenant_id) DO UPDATE SET ordinary_review_order_mode=EXCLUDED.ordinary_review_order_mode`, tenantID, mode)
	return err
}

func writeRoutesEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, before, after []Route, beforeMode, afterMode string) error {
	change := events.Change{Type: evRoutes, Before: before, After: after}
	if beforeMode != afterMode {
		change.Metadata, _ = json.Marshal(struct {
			Before string `json:"before_order_mode"`
			After  string `json:"after_order_mode"`
		}{beforeMode, afterMode})
	}
	_, err := events.Append(ctx, tx, p, change)
	return err
}
