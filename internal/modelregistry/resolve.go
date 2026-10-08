// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
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
	health, err := agentaccounts.HarnessHealthAt(ctx, tx, now)
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
	return loadLadderLimit(ctx, tx, role, 0)
}

func loadLadderLimit(ctx context.Context, tx pgx.Tx, role string, limit int) ([]ladderStep, error) {
	steps, _, err := loadLadderSnapshot(ctx, tx, role, limit)
	return steps, err
}

// Rows and ordering mode share one statement snapshot even under READ COMMITTED.
// The anchor retains mode for an empty ladder without initializing metadata.
func loadLadderSnapshot(ctx context.Context, tx pgx.Tx, role string, limit int) ([]ladderStep, string, error) {
	query := `SELECT COALESCE((SELECT ordinary_review_order_mode FROM model_review_order), 'legacy'), step.value
 FROM (SELECT 1) anchor LEFT JOIN LATERAL (
  SELECT (to_jsonb(r) - 'tenant_id') || jsonb_build_object('profile',
   (to_jsonb(p) - 'tenant_id') || jsonb_build_object(
    'display_name', d.model_display->>'display_name', 'short_name', d.model_display->>'short_name',
    'model_version', d.model_display->>'model_version', 'effort_level', coalesce(p.registered_effort_level,d.effort_level), 'provider', d.provider),
   'suppressed_until', o.suppressed_until, 'retire_at', (SELECT x.retire_at FROM model_profile_retirements x WHERE x.tenant_id=p.tenant_id AND x.profile_id=p.id), 'retired', EXISTS(SELECT 1 FROM model_profile_retirements x WHERE x.tenant_id=p.tenant_id AND x.profile_id=p.id AND x.retire_at IS NULL)) AS value,
   r.priority, r.profile_id
  FROM (` + agentaccounts.ModelRoleRoutesSQL + `) r
  JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.profile_id
  JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
  LEFT JOIN model_observations o ON o.tenant_id=p.tenant_id AND o.harness=p.harness AND o.model=p.model AND o.effort=p.effort
  WHERE r.role = $1 ORDER BY r.priority,r.profile_id`
	args := []any{role}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	query += `) step ON true ORDER BY step.priority,step.profile_id`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out, mode := []ladderStep{}, reviewOrderLegacy
	for rows.Next() {
		var step *ladderStep
		if err := rows.Scan(&mode, &step); err != nil {
			return nil, "", err
		}
		if step != nil {
			if step.Profile.Harness == "gemini" {
				step.Profile.EffortLevel = harnesslaunch.GeminiEffortLevel(step.Profile.Effort)
			}
			out = append(out, *step)
		}
	}
	return out, mode, rows.Err()
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
