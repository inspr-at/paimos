// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const preferencePreviewLimit = 256

var errPreferencePreviewIncomplete = errors.New("Model preview incomplete: catalog or role ladder exceeds 256 entries; live routing remains authoritative")

// A nil catalog preserves live resolution. Editor GET uses one bounded catalog
// across all levels and selectors, and refuses to select from an incomplete set.
type preferencePreviewCatalog struct {
	profileRows   []Profile
	profilesRead  bool
	profilesError error
	ladders       map[string][]ladderStep
	ladderErrors  map[string]error
}

func (c *preferencePreviewCatalog) profiles(ctx context.Context, tx pgx.Tx) ([]Profile, error) {
	if c == nil {
		return listProfiles(ctx, tx)
	}
	if !c.profilesRead {
		c.profilesRead = true
		c.profileRows, c.profilesError = listProfilesLimit(ctx, tx, preferencePreviewLimit+1)
		if c.profilesError == nil && len(c.profileRows) > preferencePreviewLimit {
			c.profileRows = nil
			c.profilesError = errPreferencePreviewIncomplete
		}
	}
	return c.profileRows, c.profilesError
}

func (c *preferencePreviewCatalog) ladder(ctx context.Context, tx pgx.Tx, role string) ([]ladderStep, error) {
	if c == nil {
		return loadLadder(ctx, tx, role)
	}
	if c.ladders == nil {
		c.ladders = map[string][]ladderStep{}
		c.ladderErrors = map[string]error{}
	}
	if _, read := c.ladders[role]; !read {
		steps, err := loadLadderLimit(ctx, tx, role, preferencePreviewLimit+1)
		if err == nil && len(steps) > preferencePreviewLimit {
			steps = nil
			err = errPreferencePreviewIncomplete
		}
		c.ladders[role], c.ladderErrors[role] = steps, err
	}
	// Managed review sorts its ladder; never let a caller change the cached order.
	return append([]ladderStep(nil), c.ladders[role]...), c.ladderErrors[role]
}

// Picker membership does not decode the tenant's complete route inventory.
func pickerReviewProfiles(ctx context.Context, tx pgx.Tx, profiles []Profile) (map[string]bool, error) {
	ids := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		ids = append(ids, profile.ID)
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text FROM model_profiles p
		WHERE p.id=ANY($1::uuid[]) AND EXISTS (
			SELECT 1 FROM model_role_routes r WHERE r.tenant_id=p.tenant_id
			AND r.profile_id=p.id AND r.role='review-gate')
		ORDER BY p.id LIMIT $2`, ids, preferencePreviewLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
