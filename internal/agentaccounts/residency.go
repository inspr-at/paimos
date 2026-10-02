// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
	"slices"
	"time"
)

// ResidencyClassifier is the evidence boundary supplied by AEON-473. No
// classifier means any, regardless of vendor, harness or workstation label.
type ResidencyClassifier interface {
	ResidencyClass(context.Context, pgx.Tx, Account, string) (class, evidenceRef string, expiresAt *time.Time, err error)
}
type residencyKey struct{}

func WithResidencyClassifier(ctx context.Context, c ResidencyClassifier) context.Context {
	return context.WithValue(ctx, residencyKey{}, c)
}
func ResidencyClass(ctx context.Context, tx pgx.Tx, a Account, profileID string, now time.Time) (string, error) {
	classifier, _ := ctx.Value(residencyKey{}).(ResidencyClassifier)
	if classifier == nil {
		return "any", nil
	}
	class, ref, expires, err := classifier.ResidencyClass(ctx, tx, a, profileID)
	if err != nil {
		return "", err
	}
	class = modelprefs.NormalizeResidency(class)
	if class != "any" && (ref == "" || expires == nil || !expires.After(now)) {
		return "any", nil
	}
	return class, nil
}
func applyResidency(ctx context.Context, tx pgx.Tx, accounts []Account, profileID, requirement string, now time.Time) ([]Account, bool, error) {
	if modelprefs.Strictness(requirement) == 0 {
		return accounts, false, nil
	}
	kept := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		class, err := ResidencyClass(ctx, tx, a, profileID, now)
		if err != nil {
			return nil, false, err
		}
		if modelprefs.Strictness(class) >= modelprefs.Strictness(requirement) {
			kept = append(kept, a)
		}
	}
	return kept, len(accounts) > 0 && len(kept) == 0, nil
}

// AccountMeetsResidency checks an explicit account before insertion/retarget.
// A false result is distinct from ownership and allowance errors.
func AccountMeetsResidency(ctx context.Context, tx pgx.Tx, accountID, profileID, requirement string, now time.Time) (bool, error) {
	if modelprefs.Strictness(requirement) == 0 {
		return true, nil
	}
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, a := range accounts {
		if a.ID == accountID {
			kept, _, err := applyResidency(ctx, tx, []Account{a}, profileID, requirement, now)
			return len(kept) > 0, err
		}
	}
	return false, nil
}

// QualifyingAccountIDs checks availability, model allowance and evidence for
// advisory model selection. Dispatch still enforces the same live fence.
func QualifyingAccountIDs(ctx context.Context, tx pgx.Tx, profileID, harness, projectID, requirement string, now time.Time) ([]string, error) {
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return nil, err
	}
	fences, err := loadFences(ctx, tx, harness)
	if err != nil {
		return nil, err
	}
	accounts = applyFence(accounts, fences, projectID)
	kept := []Account{}
	for _, a := range accounts {
		if a.Harness == harness && (a.AllowedProfileIDs == nil || slices.Contains(a.AllowedProfileIDs, profileID)) {
			kept = append(kept, a)
		}
	}
	kept, _, err = applyResidency(ctx, tx, kept, profileID, requirement, now)
	if err != nil {
		return nil, err
	}
	advice, err := routingAdvice(ctx, tx, kept, profileID, runRow{Purpose: "managed"}, now)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, a := range kept {
		r := advice[a.ID]
		if r.Wait == nil && r.AvailableSlots > 0 && r.Rank > 0 {
			ids = append(ids, a.ID)
		}
	}
	return ids, nil
}

// ResidencyRouteCount counts account/profile routes with valid residency and
// model allowance. Capacity is transient and does not change this evidence
// count. Load account metadata once for the whole editor view.
func ResidencyRouteCount(ctx context.Context, tx pgx.Tx, profiles map[string]string, projectID, requirement string, now time.Time) (int, error) {
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return 0, err
	}
	count := 0
	for profileID, harness := range profiles {
		fs, err := loadFences(ctx, tx, harness)
		if err != nil {
			return 0, err
		}
		for _, a := range applyFence(accounts, fs, projectID) {
			if a.Harness != harness || a.AllowedProfileIDs != nil && !slices.Contains(a.AllowedProfileIDs, profileID) {
				continue
			}
			class, err := ResidencyClass(ctx, tx, a, profileID, now)
			if err != nil {
				return 0, err
			}
			if modelprefs.Strictness(class) >= modelprefs.Strictness(requirement) {
				count++
			}
		}
	}
	return count, nil
}
