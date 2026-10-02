// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type resolutionCacheKey struct{}
type roleCacheKey struct {
	query resolveQuery
	now   time.Time
}
type resolutionCache struct {
	roles   map[roleCacheKey]Resolution
	ladders map[string][]ladderStep
}

// WithResolutionCache shares role ladders and selections within one read-only
// transaction. Discard it before any write or another transaction. Placements
// still resolve their own preferences and residency; no placement is cached here.
func WithResolutionCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, resolutionCacheKey{}, &resolutionCache{
		roles: map[roleCacheKey]Resolution{}, ladders: map[string][]ladderStep{},
	})
}

func cloneResolution(in Resolution) Resolution {
	out := in
	if in.Profile != nil {
		profile := *in.Profile
		out.Profile = &profile
	}
	out.Ladder = slices.Clone(in.Ladder)
	for i := range out.Ladder {
		out.Ladder[i].SkipReasons = slices.Clone(in.Ladder[i].SkipReasons)
	}
	return out
}

func resolveRole(ctx context.Context, tx pgx.Tx, q resolveQuery, now time.Time) (Resolution, error) {
	cache, _ := ctx.Value(resolutionCacheKey{}).(*resolutionCache)
	key := roleCacheKey{q, now}
	if cache != nil {
		if found, ok := cache.roles[key]; ok {
			return cloneResolution(found), nil
		}
	}
	out, err := resolveRoleUncached(ctx, tx, q, now)
	if err == nil && cache != nil {
		cache.roles[key] = cloneResolution(out)
	}
	return out, err
}
func loadLadder(ctx context.Context, tx pgx.Tx, role string) ([]ladderStep, error) {
	cache, _ := ctx.Value(resolutionCacheKey{}).(*resolutionCache)
	if cache != nil {
		if found, ok := cache.ladders[role]; ok {
			return found, nil
		}
	}
	out, err := loadLadderUncached(ctx, tx, role)
	if err == nil && cache != nil {
		cache.ladders[role] = out
	}
	return out, err
}
