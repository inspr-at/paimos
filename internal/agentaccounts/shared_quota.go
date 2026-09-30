// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// quotaAccounts is tenant-scoped by RLS. Empty fingerprints never join two
// enrollments, and a fingerprint never joins different harnesses.
const quotaAccounts = `SELECT sibling.id FROM agent_accounts own
 JOIN agent_accounts sibling ON sibling.tenant_id=own.tenant_id AND
 (sibling.id=own.id OR (own.quota_fingerprint<>'' AND sibling.quota_fingerprint=own.quota_fingerprint AND sibling.harness=own.harness))
 WHERE own.id=$1`

// sharedQuotaWindows projects one reservation ledger across every door. Mutating
// callers hold agentpairing.Lock before reading this view through reservation or
// settlement, so a sibling cannot concurrently spend the same remaining quota.
// Reservations keep their original window IDs for replay, release and settlement.
func sharedQuotaWindows(ctx context.Context, tx pgx.Tx, a Account, own []Window, now time.Time) ([]Window, error) {
	if a.QuotaFingerprint == "" {
		return own, nil
	}
	rows, err := tx.Query(ctx, quotaAccounts, a.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	all, err := readAccountWindows(ctx, tx, ids, false)
	if err != nil {
		return nil, err
	}
	var history, out []Window
	latest := map[string]Window{}
	for _, id := range ids {
		for _, w := range all[id] {
			if w.pairingVerification {
				continue
			}
			history = append(history, w)
			// Manual limits remain independent bounds, shared by all doors.
			// Synthetic grants stay in history to prevent repeated refreshes.
			if w.capacityReadAt == nil || synthetic(w) {
				out = append(out, w)
				continue
			}
			key := w.capacityKind + "/" + w.capacityBucket
			prev, exists := latest[key]
			freshMeasured := func(v Window) bool {
				return v.capacitySource != "estimate" && now.Sub(*v.capacityReadAt) <= 10*time.Minute && v.EndsAt.After(now)
			}
			prefer := !exists
			if exists {
				prefer = freshMeasured(w) && !freshMeasured(prev) || freshMeasured(w) == freshMeasured(prev) &&
					(w.capacityReadAt.After(*prev.capacityReadAt) || w.capacityReadAt.Equal(*prev.capacityReadAt) &&
						(w.capacitySource == "harness" && prev.capacitySource != "harness" || w.capacitySource == prev.capacitySource && w.ID < prev.ID))
			}
			if prefer {
				latest[key] = w
			}
		}
	}
	for _, w := range latest {
		// A new observation/door must not strand outstanding holds on an old
		// ledger row, including one retired when the vendor replaced a window.
		w.Reserved = 0
		for _, held := range history {
			if held.capacityKind == w.capacityKind && held.capacityBucket == w.capacityBucket &&
				held.StartsAt.Before(w.EndsAt) && w.StartsAt.Before(held.EndsAt) {
				w.Reserved += held.Reserved
			}
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
