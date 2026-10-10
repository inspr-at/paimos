// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
)

type ReviewRoute struct {
	Profile       *Profile
	Account       *agentaccounts.Account
	Ladder        []Candidate
	Role          string
	Residency     string
	OwnerRequired bool
	Trace         PreferenceTrace
}

var reviewFamilyRank = map[string]int{"openai": 0, "xai": 1, "anthropic": 2, "cursor": 3, "google": 4, "local": 5}

// ReviewFamilyRank exposes a copy of the built-in dispatch order. The Policies
// read shares this source without allowing callers to change routing rules.
func ReviewFamilyRank() map[string]int { return maps.Clone(reviewFamilyRank) }

// ResolveReview uses registry pins and the flywheel's family fallback order.
// Profiles remain tenant policy; no model names or shell snippets are invented.
func ResolveReview(ctx context.Context, tx pgx.Tx, p tenant.Principal, author, projectID string, now time.Time) (ReviewRoute, error) {
	return ResolveReviewFor(ctx, tx, p, WorkQuery{AuthorFamily: author, ProjectID: projectID}, now)
}

// ResolveReviewFor forms the qualified set before applying preferences.
// Preferences never introduce a profile or an unqualified account.
func ResolveReviewFor(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time) (ReviewRoute, error) {
	return resolveReviewWithCatalog(ctx, tx, p, q, now, nil)
}

// ResolveReviewWithPolicyFor filters the qualified ladder before preferences
// choose a reviewer. A policy-rejected first choice can fall back safely.
func ResolveReviewWithPolicyFor(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time, policy reviewgate.FamilyPolicy) (ReviewRoute, error) {
	return resolveReviewWithCatalog(ctx, tx, p, q, now, nil, policy)
}

func resolveReviewWithCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time, catalog *preferencePreviewCatalog, policies ...reviewgate.FamilyPolicy) (ReviewRoute, error) {
	out := ReviewRoute{Ladder: []Candidate{}, Role: "review-gate"}
	if strings.TrimSpace(q.Area) == "security" || q.Role == "review-gate-security" {
		out.Role = "review-gate-security"
	}
	author, err := NormalizeAuthorFamily(q.AuthorFamily)
	if err != nil {
		return out, fail(400, err.Error())
	}
	if author == "" {
		return out, fail(400, "known author family required")
	}
	if err := requireCatalog(ctx, tx); err != nil {
		return out, err
	}
	q.ReviewPolicies = policies
	q.Role = out.Role
	q.AuthorFamily = author
	if board, err := resolveBoardWork(ctx, tx, p, q, now, nil); err != nil {
		return out, err
	} else if board != nil {
		out.Profile, out.Ladder, out.OwnerRequired, out.Trace, out.Residency = board.Profile, board.Ladder, board.OwnerRequired, board.Trace, board.Residency
		if out.Profile != nil {
			out.Account, err = agentaccounts.ReviewAccount(ctx, tx, out.Profile.ID, out.Profile.Harness, q.ProjectID, now, out.Residency)
		}
		return out, err
	}
	return out, nil
}

// nativeGrokEnrolled is the review boundary for native Grok: only an account
// enrolled from a macOS arm64 computer qualifies.
func nativeGrokEnrolled(ctx context.Context, tx pgx.Tx, accountID string) (bool, error) {
	var native bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id WHERE e.account_id=$1 AND q.details->>'platform'='darwin' AND q.details->>'arch'='arm64')`, accountID).Scan(&native)
	return native, err
}

// reviewQualifiedAccountIDs is the set the review route itself admits for a
// profile: the approved principal, project and enrollment checks, and the
// native Grok boundary. Later narrowing, such as the daily ceiling, starts here.
func reviewQualifiedAccountIDs(ctx context.Context, tx pgx.Tx, profile Profile, projectID, requirement string, now time.Time) ([]string, error) {
	accounts, err := agentaccounts.ReviewAccounts(ctx, tx, profile.ID, profile.Harness, projectID, now, requirement)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, a := range accounts {
		if profile.Harness == "grok" {
			native, err := nativeGrokEnrolled(ctx, tx, a.ID)
			if err != nil {
				return nil, err
			}
			if !native {
				continue
			}
		}
		ids = append(ids, a.ID)
	}
	return ids, nil
}
