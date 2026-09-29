// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// HarnessHealthAt reports dispatch capacity for every enrolled harness.
// A harness with no accounts is absent. Callers treat that as "no pool yet"
// and do not skip a model for account reasons.
func HarnessHealthAt(ctx context.Context, tx pgx.Tx, now time.Time) (map[string]HarnessHealth, error) {
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return nil, err
	}
	used, err := occupancy(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := map[string]HarnessHealth{}
	for _, account := range accounts {
		health := out[account.Harness]
		health.Accounts++
		if account.State == "available" && probeFresh(account, now) && used[account.ID] < account.MaxParallel {
			health.Available++
			if allowanceHeadroom(account.Windows, now) {
				health.Dispatchable++
			}
		}
		out[account.Harness] = health
	}
	return out, nil
}

func probeFresh(account Account, now time.Time) bool {
	if account.LastProbeOK == nil || !*account.LastProbeOK || account.LastProbeAt == nil {
		return false
	}
	return !account.LastProbeAt.Before(now.Add(-ProbeFreshness))
}

func allowanceHeadroom(windows []Window, now time.Time) bool {
	active := activeWindows(windows, now)
	if len(active) == 0 {
		return false
	}
	for _, window := range active {
		if _, ok := fits(window, now, 1); !ok {
			return false
		}
	}
	return true
}

func activeWindows(windows []Window, now time.Time) []Window {
	out := []Window{}
	manual := false
	latest := map[string]Window{}
	for _, w := range windows {
		if w.capacityReadAt == nil {
			if !w.pairingVerification && !now.Before(w.StartsAt) && now.Before(w.EndsAt) {
				manual = true
			}
			continue
		}
		key := w.capacityKind + "/" + w.capacityBucket
		old, exists := latest[key]
		if !exists || w.capacityReadAt.After(*old.capacityReadAt) {
			latest[key] = w
		}
	}
	// Each current vendor window constrains the account. Dropping just a stale
	// or denied weekly window would incorrectly route against its fresh 5h peer.
	usable := !manual && len(latest) > 0
	for _, w := range latest {
		if !w.capacityAllowed || now.Sub(*w.capacityReadAt) > 10*time.Minute || now.Before(w.StartsAt) || !now.Before(w.EndsAt) {
			usable = false
		}
	}
	for _, w := range windows {
		if w.capacityReadAt == nil && !now.Before(w.StartsAt) && now.Before(w.EndsAt) {
			out = append(out, w)
		}
	}
	if usable {
		for _, w := range latest {
			out = append(out, w)
		}
	}
	return out
}
