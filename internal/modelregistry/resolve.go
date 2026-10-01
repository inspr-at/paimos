// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
)

// Resolution is the role choice exposed by GET /api/models/resolve.
type Resolution struct {
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
	ProfileID   string   `json:"profile_id"`
	Selected    bool     `json:"selected"`
	SkipReasons []string `json:"skip_reasons"`
}

type resolveQuery struct {
	Role         string
	AuthorFamily string
	Harness      string
}

type ladderStep struct {
	Route
	Profile Profile
}

func resolveRole(ctx context.Context, tx pgx.Tx, q resolveQuery, now time.Time) (Resolution, error) {
	role, ok := roleByName(q.Role)
	if !ok {
		return Resolution{}, fail(http.StatusBadRequest, "unknown model role")
	}
	author, err := NormalizeAuthorFamily(q.AuthorFamily)
	if err != nil {
		return Resolution{}, fail(http.StatusBadRequest, err.Error())
	}
	q.AuthorFamily = author
	if role.cross && q.AuthorFamily == "" {
		return Resolution{}, fail(http.StatusBadRequest, "review-gate requires author_family")
	}
	if q.Harness != "" && !validHarness(q.Harness) {
		return Resolution{}, fail(http.StatusBadRequest, "unsupported model harness")
	}
	steps, err := loadLadder(ctx, tx, q.Role)
	if err != nil {
		return Resolution{}, err
	}
	if q.Harness != "" && !ladderHasHarness(steps, q.Harness) {
		return Resolution{}, fail(http.StatusBadRequest, "role and harness combination is unsupported")
	}
	health, err := agentaccounts.HarnessHealthAt(ctx, tx, now)
	if err != nil {
		return Resolution{}, err
	}
	out := Resolution{Role: q.Role, AuthorFamily: q.AuthorFamily, Ladder: []Candidate{}, Source: "aeon"}
	for _, step := range steps {
		reasons := skipReasons(step, role, q, now, health)
		candidate := Candidate{ProfileID: step.ProfileID, SkipReasons: reasons}
		if len(reasons) == 0 && out.Profile == nil {
			profile := step.Profile
			candidate.Selected = true
			out.Profile = &profile
			command, err := commandTemplate(profile.Harness, profile.Model, profile.Effort, role.readOnly)
			if err != nil {
				return Resolution{}, fail(http.StatusBadRequest, "profile has no command template")
			}
			out.CommandTemplate = command
		}
		out.Ladder = append(out.Ladder, candidate)
	}
	if out.Profile == nil && role.cross {
		out.OwnerRequired = true
	}
	return out, nil
}

func loadLadder(ctx context.Context, tx pgx.Tx, role string) ([]ladderStep, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.priority, r.profile_id::text, r.state, r.reason, r.valid_until,
		       p.id::text, p.slug, p.version, p.harness, p.family, p.model, p.effort, p.tier, p.enabled, p.created_at
		FROM model_role_routes r
		JOIN model_profiles p ON p.tenant_id = r.tenant_id AND p.id = r.profile_id
		WHERE r.role = $1
		ORDER BY r.priority, r.profile_id`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ladderStep
	for rows.Next() {
		var step ladderStep
		if err := rows.Scan(
			&step.Priority, &step.ProfileID, &step.State, &step.Reason, &step.ValidUntil,
			&step.Profile.ID, &step.Profile.Slug, &step.Profile.Version, &step.Profile.Harness,
			&step.Profile.Family, &step.Profile.Model, &step.Profile.Effort, &step.Profile.Tier,
			&step.Profile.Enabled, &step.Profile.CreatedAt,
		); err != nil {
			return nil, err
		}
		step.Role = role
		out = append(out, step)
	}
	return out, rows.Err()
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
	if role.cross && step.Profile.Family == "unknown" {
		reasons = append(reasons, "unknown author family")
	}
	if role.cross && step.Profile.Family == q.AuthorFamily {
		reasons = append(reasons, "author family")
	}
	if q.Harness != "" && step.Profile.Harness != q.Harness {
		reasons = append(reasons, "harness filter")
	}
	if !step.Profile.Enabled {
		reasons = append(reasons, "policy")
	}
	if step.State != "available" && step.ValidUntil != nil && now.Before(*step.ValidUntil) {
		reasons = append(reasons, fmt.Sprintf("%s until %s: %s", step.State, step.ValidUntil.UTC().Format(time.RFC3339), step.Reason))
	}
	h := health[step.Profile.Harness]
	if h.Accounts > 0 {
		if h.Available == 0 {
			reasons = append(reasons, "account availability")
		} else if h.Dispatchable == 0 {
			reasons = append(reasons, "allowance")
		}
	}
	if reasons == nil {
		return []string{}
	}
	return reasons
}
