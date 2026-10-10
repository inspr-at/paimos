// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// HarnessHealthAt reports dispatch capacity for every enrolled harness.
// A harness with no accounts is absent. Callers treat that as "no pool yet"
// and do not skip a model for account reasons.
// Explanations use the current reader's sharing policy before account identity
// is folded into harness totals. Without a reader, private details stay hidden.
func HarnessHealthAt(ctx context.Context, tx pgx.Tx, now time.Time, projectIDs ...string) (map[string]HarnessHealth, error) {
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(accounts))
	for i, a := range accounts {
		ids[i] = a.ID
	}
	visible := accountprivacy.Policy{}
	if reader, ok := tenant.PrincipalFrom(ctx); ok && reader.ID != "" {
		visible, err = accountprivacy.Load(ctx, tx, reader, ids)
		if err != nil {
			return nil, err
		}
	}
	var allowed map[string]bool
	if len(projectIDs) > 0 {
		allowed, err = accountuse.AllowedIDs(ctx, tx, ids, projectIDs[0])
		if err != nil {
			return nil, err
		}
	}
	used, err := occupancy(ctx, tx)
	if err != nil {
		return nil, err
	}
	eligible := accounts
	if allowed != nil {
		eligible = []Account{}
		for _, account := range accounts {
			if allowed[account.ID] {
				eligible = append(eligible, account)
			}
		}
	}
	advice, err := routingAdvice(ctx, tx, eligible, "", runRow{Purpose: "managed"}, now)
	if err != nil {
		return nil, err
	}
	out := map[string]HarnessHealth{}
	for _, account := range accounts {
		health := out[account.Harness]
		health.Accounts++
		if allowed != nil && !allowed[account.ID] {
			health.ContextDenied++
			out[account.Harness] = health
			continue
		}
		if account.State == "available" && probeFresh(account, now) && used[account.ID] < account.MaxParallel {
			health.Available++
		}
		// The same routing projection is exposed by Settings and the Agents
		// overview. Unknown quota is not converted into an exhausted allowance.
		route := advice[account.ID]
		if route.Wait == nil && route.AvailableSlots > 0 && route.Rank > 0 {
			health.Dispatchable++
		} else if route.Wait != nil {
			wait := route.Wait
			if !visible[account.ID] {
				// Availability can be public without releasing the owner's
				// quota, schedule, reserve or exact next attempt time.
				wait = &CapacityWait{}
			}
			reason := ModelWaitReason(account.Harness, wait)
			if !slices.Contains(health.Reasons, reason) {
				health.Reasons = append(health.Reasons, reason)
			}
		}
		out[account.Harness] = health
	}
	return out, nil
}

// ModelWaitReason describes the actual admission gate without account identity
// or raw codes. A named reset is retained rather than guessed from missing quota.
func ModelWaitReason(harness string, wait *CapacityWait) string {
	name := map[string]string{"codex": "Codex", "claude": "Claude", "cursor": "Cursor", "grok": "Grok", "gemini": "Gemini", "pi": "Pi", "opencode": "OpenCode"}[harness]
	if name == "" {
		name = "Model"
	}
	cause := map[string]string{
		"allowance":           "account is at its allowance or floor",
		"vendor":              "account reported a vendor limit",
		"reading":             "account needs a quota reading to check its saved floor",
		"schedule":            "account is outside its scheduled hours",
		"reserve":             "account capacity is kept for its owner",
		"hold":                "account is on hold",
		"offline":             "account has no recent successful sign-in check",
		"sign_in":             "account needs signing in",
		"state":               "account is paused",
		"capacity":            "account has no free agent slots",
		"approval":            "account needs approval for agent use",
		"models":              "account has no allowed model",
		"daily_limit":         "account is at its daily cap",
		"daily_limit_unknown": "account's daily cap could not be checked",
	}[wait.Code]
	if cause == "" {
		cause = "account cannot take new work right now"
	}
	until := ""
	if wait.Until != nil {
		until = " until " + wait.Until.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s %s%s", name, cause, until)
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
