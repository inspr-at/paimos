// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentverification"
	"github.com/inspr-at/paimos/internal/tenant"
)

type ReviewRoute struct {
	Profile *Profile
	Account *agentaccounts.Account
	Ladder  []Candidate
}

// ResolveReview uses registry pins and the flywheel's family fallback order.
// Profiles remain tenant policy; no model names or shell snippets are invented.
func ResolveReview(ctx context.Context, tx pgx.Tx, p tenant.Principal, author, projectID string, now time.Time) (ReviewRoute, error) {
	out := ReviewRoute{Ladder: []Candidate{}}
	author, err := NormalizeAuthorFamily(author)
	if err != nil {
		return out, fail(400, err.Error())
	}
	if author == "" {
		return out, fail(400, "known author family required")
	}
	if err := ensureCatalog(ctx, tx, p); err != nil {
		return out, err
	}
	steps, err := loadLadder(ctx, tx, "review-gate")
	if err != nil {
		return out, err
	}
	rank := map[string]int{"openai": 0, "xai": 1, "anthropic": 2, "cursor": 3}
	sort.SliceStable(steps, func(i, j int) bool {
		a, b := steps[i].Profile, steps[j].Profile
		if rank[a.Family] != rank[b.Family] {
			return rank[a.Family] < rank[b.Family]
		}
		if a.Tier != b.Tier {
			return a.Tier == "frontier"
		}
		return false
	})
	role, _ := roleByName("review-gate")
	for _, step := range steps {
		reasons := skipReasons(step, role, resolveQuery{Role: "review-gate", AuthorFamily: author}, now, nil)
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
				account, err = agentaccounts.ReviewAccount(ctx, tx, step.ProfileID, step.Profile.Harness, projectID, now)
				if err != nil {
					return out, err
				}
				if account == nil {
					reasons = append(reasons, "no approved account with available capacity")
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
		if len(reasons) == 0 && out.Profile == nil {
			profile := step.Profile
			out.Profile = &profile
			out.Account = account
			candidate.Selected = true
		}
		out.Ladder = append(out.Ladder, candidate)
	}
	return out, nil
}
