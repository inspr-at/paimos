// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"
	"slices"

	"github.com/inspr-at/paimos/internal/modelprefs"

	"github.com/jackc/pgx/v5"
)

const preferencePreviewLimit = 256

var errPreferencePreviewIncomplete = errors.New("Model preview incomplete: catalog or Models order exceeds 256 entries; live routing remains authoritative")

// A nil catalog preserves live resolution. Editor GET uses one bounded catalog
// across all levels and selectors, and refuses to select from an incomplete set.
type preferencePreviewCatalog struct {
	profileRows   []Profile
	profilesRead  bool
	profilesError error
	ladders       map[string][]ladderStep
	ladderErrors  map[string]error
	orderModes    map[string]string
	board         *boardCatalog
	boardError    error
}

// previewBoardKey carries a preview's catalog into the Models walk.
type previewBoardKey struct{}

// routingBoard groups the preview's one bounded profile read into the
// routing catalog every cell's Models walk reuses. An over-limit catalog
// stays incomplete rather than being re-read unbounded per cell.
func (c *preferencePreviewCatalog) routingBoard(ctx context.Context, tx pgx.Tx) (boardCatalog, error) {
	if c.board == nil && c.boardError == nil {
		profiles, err := c.profiles(ctx, tx)
		if err != nil {
			c.boardError = err
		} else {
			enabled := make([]Profile, 0, len(profiles))
			for _, p := range profiles {
				if p.Enabled {
					enabled = append(enabled, p)
				}
			}
			board := boardCatalogFrom(enabled, nil)
			c.board = &board
		}
	}
	if c.boardError != nil {
		return boardCatalog{}, c.boardError
	}
	return *c.board, nil
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
	steps, _, err := c.ladderSnapshot(ctx, tx, role)
	return steps, err
}

func (c *preferencePreviewCatalog) ladderSnapshot(ctx context.Context, tx pgx.Tx, role string) ([]ladderStep, string, error) {
	if c == nil {
		return loadLadderSnapshot(ctx, tx, role, 0)
	}
	if c.ladders == nil {
		c.ladders = map[string][]ladderStep{}
		c.ladderErrors = map[string]error{}
		c.orderModes = map[string]string{}
	}
	if _, read := c.ladders[role]; !read {
		steps, mode, err := loadLadderSnapshot(ctx, tx, role, preferencePreviewLimit+1)
		if err == nil && len(steps) > preferencePreviewLimit {
			steps = nil
			err = errPreferencePreviewIncomplete
		}
		c.ladders[role], c.orderModes[role], c.ladderErrors[role] = steps, mode, err
	}
	return append([]ladderStep(nil), c.ladders[role]...), c.orderModes[role], c.ladderErrors[role]
}

// Picker membership does not decode the tenant's complete route inventory.
func pickerReviewProfiles(ctx context.Context, tx pgx.Tx, profiles []Profile) (map[string]bool, error) {
	ids := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		ids = append(ids, profile.ID)
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
		WHERE p.id=ANY($1::uuid[]) AND p.tier IN ('strong','frontier') AND coalesce(p.registered_effort_level,d.effort_level)>=4
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

// derivedRoleSteps retains the deprecated route response vocabulary while its
// choices come from the current Models row, situations, Quick jobs and rules.
func derivedRoleSteps(ctx context.Context, tx pgx.Tx, person *string, role string, limit int) ([]ladderStep, string, error) {
	c, err := loadBoardCatalog(ctx, tx)
	if err != nil {
		return nil, "", err
	}
	return derivedRoleStepsIn(ctx, tx, c, person, role, limit)
}

// routingRoleSteps derives the role's routes from the enabled catalog live
// routing reads, so disabled history never counts toward its bound.
func routingRoleSteps(ctx context.Context, tx pgx.Tx, person *string, role string) ([]ladderStep, error) {
	c, err := loadBoardCatalog(ctx, tx, true)
	if err != nil {
		return nil, err
	}
	steps, _, err := derivedRoleStepsIn(ctx, tx, c, person, role, 0)
	return steps, err
}

func derivedRoleStepsIn(ctx context.Context, tx pgx.Tx, c boardCatalog, person *string, role string, limit int) ([]ladderStep, string, error) {
	s, err := modelprefs.LoadBoardRouting(ctx, tx, person, "")
	if err != nil {
		return nil, "", err
	}
	s = modelsBoardIdentities(s, c)
	c = modelsCatalog(c)
	s.Lines = c.lines
	q := modelprefs.BoardQuery{Column: "other", Situation: "first"}
	if role == "build-hard" {
		q.Situation = "stuck"
	}
	if role == "review-gate" || role == "review-gate-security" {
		q.Review = true
	}
	d := modelprefs.ResolveModelsBoard(s, q)
	mode := reviewOrderSaved
	if d.Automatic {
		mode = reviewOrderLegacy
		d.Steps = append(d.Steps, automaticReviewSteps(c, "")...)
	}
	if role == "scout" || role == "mechanical" {
		definition, _ := roleByName(role)
		quick, err := quickJobSteps(ctx, tx, c, definition.effort)
		if err != nil {
			return nil, "", err
		}
		d.Steps = append(quick, d.Steps...)
	}
	out := []ladderStep{}
	seen := map[string]bool{}
	for _, order := range d.Steps {
		if slices.Contains(d.Not, order.Line) && order.Stage != "quick" {
			continue
		}
		for _, profile := range modelsWalkCandidates(c.profiles[order.Line], order.Effort, order.Level, q.Review) {
			if seen[profile.ID] || q.Review && (!qualifiedReviewProfile(profile) || role == "review-gate-security" && profile.Family == "anthropic") {
				continue
			}
			if order.Stage == "quick" && profile.Tier != "fast" {
				continue
			}
			if role == "build-hard" && profile.Tier != "strong" && profile.Tier != "frontier" {
				continue
			}
			seen[profile.ID] = true
			step, err := preferenceStep(ctx, tx, profile, role)
			if err != nil {
				return nil, "", err
			}
			step.Priority = len(out) + 1
			out = append(out, step)
			if limit > 0 && len(out) >= limit {
				return out, mode, nil
			}
		}
	}
	return out, mode, nil
}
