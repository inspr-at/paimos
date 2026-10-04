// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"sort"
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
			windows, wait, err := admission(ctx, tx, account, account.Windows, now, used[account.ID], runRow{Purpose: "managed"}, false)
			if err != nil {
				return nil, err
			}
			if wait == nil && windowWait(windows, now) == nil {
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

// activeWindows is every window that binds now. A manual window set by hand
// caps on top of the vendor's readings (AEON-384): both apply, so readings,
// pacing and Keep for you stay in force next to a person's limit.
func activeWindows(windows []Window, now time.Time) []Window {
	out := []Window{}
	latest := map[string]Window{}
	for _, w := range windows {
		if w.capacityReadAt == nil {
			continue
		}
		key := w.capacityKind + "/" + w.capacityBucket
		old, exists := latest[key]
		if !exists || w.capacityReadAt.After(*old.capacityReadAt) {
			latest[key] = w
		}
	}
	// A current vendor denial fences even explicit manual budgets. Expired or
	// replaced buckets are history, not permanent account constraints.
	// Keys are sorted so the surviving windows do not depend on map iteration.
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	usable := true
	kept := make([]string, 0, len(keys))
	for _, key := range keys {
		w := latest[key]
		if w.capacityRetired {
			continue
		}
		if !now.Before(w.EndsAt) {
			continue
		}
		if !w.capacityAllowed && now.Sub(*w.capacityReadAt) <= 10*time.Minute {
			return out
		}
		if !w.capacityAllowed || now.Before(w.StartsAt) {
			usable = false
		}
		kept = append(kept, key)
	}
	for _, w := range windows {
		if w.capacityReadAt == nil && !now.Before(w.StartsAt) && now.Before(w.EndsAt) {
			out = append(out, w)
		}
	}
	if usable {
		for _, key := range kept {
			out = append(out, latest[key])
		}
	}
	return out
}
