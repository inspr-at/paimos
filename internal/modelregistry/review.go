// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentverification"
	"github.com/inspr-at/paimos/internal/modelprefs"
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
	chain, trace, requirement, err := placementTrace(ctx, tx, q)
	if err != nil {
		return out, err
	}
	out.Trace = trace
	out.Residency = requirement
	if _, known := roleByName(out.Role); !known {
		out.OwnerRequired = true
		out.Trace.Blocked = "security review ladder not available (AEON-485)"
		out.Trace.Hard = []string{"security_review"}
		return out, nil
	}
	if err := ensureCatalog(ctx, tx, p); err != nil {
		return out, err
	}
	steps, err := loadLadder(ctx, tx, out.Role)
	if err != nil {
		return out, err
	}
	if out.Role == "review-gate" {
		sort.SliceStable(steps, func(i, j int) bool {
			a, b := steps[i].Profile, steps[j].Profile
			if reviewFamilyRank[a.Family] != reviewFamilyRank[b.Family] {
				return reviewFamilyRank[a.Family] < reviewFamilyRank[b.Family]
			}
			if a.Tier != b.Tier {
				return a.Tier == "frontier"
			}
			return false
		})
	}
	role, _ := roleByName(out.Role)
	qualified := map[string]*agentaccounts.Account{}
	for _, step := range steps {
		reasons := skipReasons(step, role, resolveQuery{Role: out.Role, AuthorFamily: author, Harness: q.Harness}, now, nil)
		if step.Profile.Effort != "xhigh" || step.Profile.Tier != "frontier" && step.Profile.Tier != "strong" {
			reasons = append(reasons, "review requires frontier or strong at xhigh")
		}
		// Claude has a platform-independent qualified no-tools bridge. Native
		// Grok is qualified only for an arm64 Mac enrollment; Cursor/pi/Codex
		// are deliberately skipped until their release-owned boundary qualifies.
		var account *agentaccounts.Account
		if len(reasons) == 0 {
			capability := agentverification.For(step.Profile.Harness, "darwin", "arm64")
			if !capability.Supported {
				reasons = append(reasons, capability.Reason)
			} else {
				account, err = agentaccounts.ReviewAccount(ctx, tx, step.ProfileID, step.Profile.Harness, q.ProjectID, now, requirement)
				if err != nil {
					return out, err
				}
				if account == nil {
					reasons = append(reasons, "no approved account with available capacity")
					if requirement != "any" {
						reasons = append(reasons, "not allowed: "+requirement)
					}
				}
				if account != nil && step.Profile.Harness == "grok" {
					var native bool
					if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id WHERE e.account_id=$1 AND q.details->>'platform'='darwin' AND q.details->>'arch'='arm64')`, account.ID).Scan(&native); err != nil {
						return out, err
					}
					if !native {
						reasons = append(reasons, "native Grok needs a qualified macOS arm64 enrollment")
						account = nil
					}
				}
			}
		}
		candidate := Candidate{ProfileID: step.ProfileID, SkipReasons: reasons}
		if len(reasons) == 0 {
			qualified[step.ProfileID] = account
			if out.Profile == nil {
				profile := step.Profile
				out.Profile = &profile
				out.Account = account
				candidate.Selected = true
			}
		}
		out.Ladder = append(out.Ladder, candidate)
	}
	if out.Role == "review-gate-security" {
		out.Trace.Hard = []string{"security_review"}
	} else {
		out.Trace.Kind = "review"
		out.Trace.KindSource = "review"
		result := modelprefs.ResolveCell(chain, "review", out.Trace.Bucket)
		traceCell(&out.Trace, result)
		if result.Cell != nil && result.Cell.Mode != "auto" {
			profiles, err := listProfiles(ctx, tx)
			if err != nil {
				return out, err
			}
			preferred := expandPreference(*result.Cell, profiles)
			if len(preferred) == 0 {
				out.Trace.Fallback = "preferred profile unavailable"
			}
			var pick *Profile
			for _, profile := range preferred {
				if qualified[profile.ID] != nil {
					copy := profile
					pick = &copy
					break
				}
				reason := "not on the review ladder"
				for _, c := range out.Ladder {
					if c.ProfileID == profile.ID {
						reason = strings.Join(c.SkipReasons, "; ")
						break
					}
				}
				out.Trace.Fallback = reason
			}
			if pick != nil {
				out.Profile = pick
				out.Account = qualified[pick.ID]
				out.Trace.Fallback = ""
				if result.Cell.Mode == "latest" {
					out.Trace.LatestResolvedTo = pick.ID
				}
				for i := range out.Ladder {
					out.Ladder[i].Selected = out.Ladder[i].ProfileID == pick.ID
				}
			}
		}
	}
	if out.Profile == nil {
		out.OwnerRequired = true
		if requirement != "any" {
			out.Trace.Blocked = "no " + requirement + " model route"
		}
	} else {
		out.Trace.QualifyingAccountIDs = []string{out.Account.ID}
	}
	return out, nil
}
