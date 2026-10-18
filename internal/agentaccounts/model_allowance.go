// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// AccountAllowsProfile shares the successor policy with creation, reservation
// and claim SQL. The supplied account must be read under the caller's live
// ownership/authorization fence; allowance alone grants no launch authority.
func AccountAllowsProfile(ctx context.Context, tx pgx.Tx, a Account, profileID string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT aeon_account_allows_profile($1,$2::uuid[],$3::uuid)`, a.Harness, a.AllowedProfileIDs, profileID).Scan(&allowed)
	return allowed, err
}

// expandCatalogAllowances projects effective IDs on copies, preserving stored
// pins in account metadata responses and all mutation comparisons.
func expandCatalogAllowances(ctx context.Context, tx pgx.Tx, accounts []Account) ([]Account, error) {
	out := append([]Account(nil), accounts...)
	for i := range out {
		if out[i].AllowedProfileIDs == nil {
			continue
		}
		if err := tx.QueryRow(ctx, `SELECT aeon_account_allowed_profile_ids($1,$2::uuid[])::text[]`, out[i].Harness, out[i].AllowedProfileIDs).Scan(&out[i].AllowedProfileIDs); err != nil {
			return nil, err
		}
	}
	return out, nil
}
