// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

// Resolution is the role choice exposed by GET /api/models/resolve.
type Resolution struct {
	Preview         bool        `json:"preview,omitempty"`
	Role            string      `json:"role"`
	AuthorFamily    string      `json:"author_family"`
	Profile         *Profile    `json:"profile"`
	Ladder          []Candidate `json:"ladder"`
	CommandTemplate string      `json:"command_template,omitempty"`
	OwnerRequired   bool        `json:"owner_required"`
	Source          string      `json:"source"`
}

// Candidate is one ladder step and why it was or was not selected.
type Candidate struct {
	Stage       string   `json:"stage,omitempty"`
	ProfileID   string   `json:"profile_id"`
	Selected    bool     `json:"selected"`
	SkipReasons []string `json:"skip_reasons"`
}

type resolveQuery struct {
	Role         string
	AuthorFamily string
	Harness      string
	ProjectID    string
	OffHarnesses []string
}

type ladderStep struct {
	Retired  bool       `json:"retired"`
	RetireAt *time.Time `json:"retire_at"`
	Route
	Profile         Profile
	SuppressedUntil *time.Time `json:"suppressed_until"`
}

// validateResolveQuery checks request structure without reading or preparing a catalog.
func validateResolveQuery(q resolveQuery) (resolveQuery, error) {
	role, ok := roleByName(q.Role)
	if !ok {
		return q, fail(http.StatusBadRequest, "unknown model role")
	}
	author, err := NormalizeAuthorFamily(q.AuthorFamily)
	if err != nil {
		return q, fail(http.StatusBadRequest, err.Error())
	}
	q.AuthorFamily = author
	if role.cross && q.AuthorFamily == "" {
		return q, fail(http.StatusBadRequest, "review-gate requires author_family")
	}
	if q.Harness != "" && !validHarness(q.Harness) {
		return q, fail(http.StatusBadRequest, "unsupported model harness")
	}
	return q, nil
}

func resolveRole(ctx context.Context, tx pgx.Tx, q resolveQuery, now time.Time) (Resolution, error) {
	return resolveRoleWithCatalog(ctx, tx, q, now, nil)
}

func resolveRoleWithCatalog(ctx context.Context, tx pgx.Tx, q resolveQuery, now time.Time, catalog *preferencePreviewCatalog) (Resolution, error) {
	q, err := validateResolveQuery(q)
	if err != nil {
		return Resolution{}, err
	}
	role, _ := roleByName(q.Role)
	steps, err := catalog.ladder(ctx, tx, q.Role)
	if err != nil {
		return Resolution{}, err
	}
	if q.Harness != "" && !ladderHasHarness(steps, q.Harness) {
		return Resolution{}, fail(http.StatusBadRequest, "role and harness combination is unsupported")
	}
	health, err := agentaccounts.HarnessHealthAt(ctx, tx, now, q.ProjectID)
	if err != nil {
		return Resolution{}, err
	}
	out := Resolution{Role: q.Role, AuthorFamily: q.AuthorFamily, Ladder: []Candidate{}, Source: "aeon"}
	for _, step := range steps {
		reasons := skipReasons(step, role, q, now, health)
		// Retain catalog-only previews until a harness has an account pool.
		// Enrolled pools use the board's exact allowance, project and capacity
		// check, rather than accepting any healthy account of this harness.
		if len(reasons) == 0 && health[step.Profile.Harness].Accounts > 0 {
			ids, err := agentaccounts.QualifyingAccountIDs(ctx, tx, step.ProfileID, step.Profile.Harness, q.ProjectID, "any", now)
			if err != nil {
				return Resolution{}, err
			}
			if len(ids) == 0 {
				reasons = append(reasons, "no qualified account with available capacity")
			}
		}
		candidate := Candidate{ProfileID: step.ProfileID, SkipReasons: reasons}
		if len(reasons) == 0 && out.Profile == nil {
			profile := step.Profile
			candidate.Selected = true
			out.Profile = &profile
			command, err := commandTemplate(profile.Harness, profile.Model, profile.Effort, role.readOnly)
			if err != nil {
				return Resolution{}, fail(http.StatusBadRequest, "profile has no command template")
			}
			out.Preview = health[profile.Harness].Accounts == 0
			if !out.Preview {
				out.CommandTemplate = command
			}
		}
		out.Ladder = append(out.Ladder, candidate)
	}
	if out.Profile == nil && role.cross {
		out.OwnerRequired = true
	}
	return out, nil
}

func loadLadder(ctx context.Context, tx pgx.Tx, role string) ([]ladderStep, error) {
	return loadLadderLimit(ctx, tx, role, 0)
}

func loadLadderLimit(ctx context.Context, tx pgx.Tx, role string, limit int) ([]ladderStep, error) {
	steps, _, err := loadLadderSnapshot(ctx, tx, role, limit)
	return steps, err
}

// Deprecated ladder-shaped previews are derived from Models; dispatch uses
// resolveBoardWork. No reader here accesses either retained ladder table.
func loadLadderSnapshot(ctx context.Context, tx pgx.Tx, role string, limit int) ([]ladderStep, string, error) {
	return derivedRoleSteps(ctx, tx, nil, role, limit)
}

func ladderHasHarness(steps []ladderStep, harness string) bool {
	for _, step := range steps {
		if step.Profile.Harness == harness {
			return true
		}
	}
	return false
}

func skipReasons(step ladderStep, role roleDef, q resolveQuery, now time.Time, health map[string]agentaccounts.HarnessHealth) []string {
	var reasons []string
	if slices.Contains(q.OffHarnesses, step.Profile.Harness) {
		reasons = append(reasons, "harness unavailable in the plan")
	}
	// Immutable legacy mislabels remain in history, but are retired from
	// dispatch. The same check guards ordinary resolution and managed reviews.
	if harnesslaunch.ModelFamily(step.Profile.Harness, step.Profile.Model) != step.Profile.Family {
		reasons = append(reasons, "profile family does not match its harness/provider binding")
	}
	if role.cross && step.Profile.Family == "unknown" {
		reasons = append(reasons, "unknown author family")
	}
	if role.cross && step.Profile.Family == q.AuthorFamily {
		reasons = append(reasons, "author family")
	}
	if q.Harness != "" && step.Profile.Harness != q.Harness {
		reasons = append(reasons, "harness filter")
	}
	if KnownInvalid(step.Profile.Model) {
		reasons = append(reasons, "known invalid model")
	}
	if step.SuppressedUntil != nil && now.Before(*step.SuppressedUntil) {
		reasons = append(reasons, "model invalid until "+step.SuppressedUntil.UTC().Format(time.RFC3339))
	}
	if role.name == "review-gate-security" && step.Profile.Family == "anthropic" {
		reasons = append(reasons, "security review policy")
	}
	if step.Retired || step.RetireAt != nil && !now.Before(*step.RetireAt) {
		reasons = append(reasons, "retired")
	}
	if !step.Profile.Enabled {
		reasons = append(reasons, "policy")
	}
	if step.State != "available" && (step.ValidUntil == nil || now.Before(*step.ValidUntil)) {
		if step.ValidUntil == nil {
			reasons = append(reasons, "paused: "+step.Reason)
		} else {
			reasons = append(reasons, fmt.Sprintf("paused until %s: %s", step.ValidUntil.UTC().Format(time.RFC3339), step.Reason))
		}
	}
	h := health[step.Profile.Harness]
	if h.Accounts > 0 && h.ContextDenied == h.Accounts {
		reasons = append(reasons, agentaccounts.ContextSkipReason)
	} else if h.Accounts > 0 {
		if h.Dispatchable == 0 {
			if len(h.Reasons) > 0 {
				reasons = append(reasons, h.Reasons...)
			} else {
				reasons = append(reasons, "No account has a free agent slot")
			}
		}
	}
	if reasons == nil {
		return []string{}
	}
	return reasons
}
