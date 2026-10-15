// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk R15: an adopted preference board bypasses project family policy, or the
// ordinary review-off setting removes the compiled routine evaluator floor.
func TestRoutineEvaluationHonorsLivePolicyInBothRegistryRoutes(t *testing.T) {
	p, _ := boardFixture(t)
	project := editorProject(t, p, "EVAL-1")
	profiles := decode[[]Profile](t, &p, "GET", "/api/models", "", http.StatusOK)
	opus := profileBySlug(profiles, "claude-opus-xhigh")
	minimalAccount(t, p, opus)
	author := VerifiedModel{Harness: "codex", RequestedModel: "gpt-6.1-sol", EffectiveModel: "gpt-6.1-sol", Evidence: "vendor_reported"}
	for _, board := range []bool{false, true} {
		if board {
			boardDecode[boardWriteResult](t, boardCall(t, p, "PUT", "/api/model-preferences/orders/review:openai/first?for=default", map[string]any{"rank": []string{"anthropic:opus"}, "not": []string{}, "revision": 0}, ""), 200)
		}
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `INSERT INTO cross_family_policies(tenant_id,project_id,mode,allowed_families,updated_by) VALUES($1,$2,'off',ARRAY[]::text[],$3) ON CONFLICT (tenant_id,(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid))) DO UPDATE SET mode='off',allowed_families=ARRAY[]::text[]`, p.TenantID, project, p.ID); err != nil {
				return err
			}
			q := WorkQuery{ProjectID: project, PersonID: &p.ID}
			route, policy, err := ResolveRoutineEvaluationFor(t.Context(), tx, p, q, author, time.Now())
			if err != nil {
				return err
			}
			if route.Profile == nil || route.Profile.ID != opus.ID || policy.Mode != "off" {
				t.Fatalf("off setting lost independent pinned route (board=%v): %+v", board, route)
			}
			if _, err := tx.Exec(t.Context(), `UPDATE cross_family_policies SET mode='allowlist',allowed_families=ARRAY['xai'] WHERE project_id=$1`, project); err != nil {
				return err
			}
			route, policy, err = ResolveRoutineEvaluationFor(t.Context(), tx, p, q, author, time.Now())
			if err != nil {
				return err
			}
			if route.Profile != nil || !route.OwnerRequired || policy.Mode != "allowlist" {
				t.Fatalf("project policy bypassed (board=%v): %+v", board, route)
			}
			wrong := author
			wrong.Evidence = "requested"
			if _, _, err = ResolveRoutineEvaluationFor(t.Context(), tx, p, q, wrong, time.Now()); err == nil || !strings.Contains(err.Error(), "unverified") {
				t.Fatalf("unverified author wrong failure: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNormalizeAuthorFamily(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"claude", "anthropic"}, {"codex", "openai"}, {"grok", "xai"},
		{"anthropic", "anthropic"}, {"openai", "openai"}, {"xai", "xai"}, {"cursor", "cursor"},
		{"", ""}, {"  ", ""}, {" codex ", "openai"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeAuthorFamily(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeAuthorFamily(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestAuthorFamilyRejections(t *testing.T) {
	for _, input := range []string{"pi", "unknown"} {
		t.Run(input, func(t *testing.T) {
			got, err := NormalizeAuthorFamily(input)
			if err == nil || got != "" {
				t.Fatalf("invalid family accepted: %q, %v", got, err)
			}
			for _, accepted := range []string{"openai", "anthropic", "xai", "cursor", "codex", "claude", "grok"} {
				if !strings.Contains(err.Error(), accepted) {
					t.Errorf("error %q omits accepted value %q", err, accepted)
				}
			}
			if input == "pi" && (!strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "pass the model family")) {
				t.Fatalf("pi must request an explicit family: %v", err)
			}
			// Both entry points reject invalid families before accessing a DB.
			_, resolveErr := resolveRole(context.Background(), nil, resolveQuery{Role: "review-gate", AuthorFamily: input}, time.Now())
			_, reviewErr := ResolveReview(context.Background(), nil, tenant.Principal{}, input, "", time.Now())
			for _, serverErr := range []error{resolveErr, reviewErr} {
				var he *httpError
				if !errors.As(serverErr, &he) || he.status != http.StatusBadRequest || he.msg != err.Error() {
					t.Errorf("server error = %v; want HTTP 400 with %q", serverErr, err)
				}
			}
		})
	}
}

func TestProfileFamilyMustMatchProvider(t *testing.T) {
	for _, tc := range []struct{ harness, model, family string }{
		{"grok", "grok-4.7", "anthropic"},
		{"claude", "fable", "xai"},
		{"codex", "gpt-6-astra", "anthropic"},
		{"gemini", "gemini-2.5-pro", "openai"},
		{"cursor", "grok-4.7-xhigh", "anthropic"},
		{"cursor", "auto", "openai"},
		{"pi", "anthropic/claude-opus-5", "xai"},
		{"pi", "openrouter/stealth/space-bunny-alpha", "anthropic"},
		{"opencode", "custom/private-model", "openai"},
		{"opencode", "google/gemini-2.5-pro", "openai"},
	} {
		t.Run(tc.harness+"/"+tc.model, func(t *testing.T) {
			effort := "xhigh"
			if tc.harness == "gemini" {
				effort = "16384"
			}
			if err := validateProfile(profileWrite{Slug: "fixture", Version: "1", Harness: tc.harness, Model: tc.model, Family: tc.family, Effort: effort, Tier: "frontier"}); err == nil {
				t.Fatal("mislabelled family accepted")
			}
			role, _ := roleByName("review-gate")
			step := ladderStep{Profile: Profile{Harness: tc.harness, Model: tc.model, Family: tc.family, Enabled: true}}
			reasons := skipReasons(step, role, resolveQuery{AuthorFamily: "local"}, time.Now(), nil)
			if len(reasons) == 0 {
				t.Fatal("legacy mislabelled profile remained routable")
			}
		})
	}
	for _, profile := range catalogProfiles() {
		if err := validateProfile(profileWrite{Slug: profile.Slug, Version: profile.Version, Harness: profile.Harness, Model: profile.Model, Family: profile.Family, Effort: profile.Effort, Tier: profile.Tier}); err != nil {
			t.Fatalf("existing catalog pin %s rejected: %v", profile.Slug, err)
		}
	}
}

// Risk R1/R14: a cross-family route is unusable or inherits tools/hooks, while
// fixing it accidentally admits same-family, unapproved or unenrolled routes.
func TestReviewResolutionBindsCodexIsolationAndExplainsMissingGrok(t *testing.T) {
	p, _ := boardFixture(t)
	profiles := decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	pin := profileBySlug(profiles, "codex-6-1-sol-xhigh")
	account := minimalAccount(t, p, pin)
	got := decode[WorkResolution](t, &p, "GET", "/api/models/resolve?role=review-gate&author_family=claude&harness=codex", "", 200)
	const want = "codex exec --ignore-user-config -s read-only -m gpt-6.1-sol -c model_reasoning_effort=xhigh '{prompt}'"
	if got.OwnerRequired || got.Profile == nil || got.Profile.ID != pin.ID || got.CommandTemplate != want || len(got.Trace.QualifyingAccountIDs) != 1 || got.Trace.QualifyingAccountIDs[0] != account {
		t.Fatalf("isolated route not usable: %+v", got)
	}
	for _, unsafe := range []string{"", strings.Replace(want, "--ignore-user-config ", "", 1), strings.Replace(want, "-s read-only ", "", 1), strings.Replace(want, "read-only", "workspace-write", 1)} {
		if err := ValidateReviewCommand("codex", pin.Model, pin.Effort, unsafe); err == nil {
			t.Fatalf("unsafe route accepted: %s", unsafe)
		}
	}
	same := decode[WorkResolution](t, &p, "GET", "/api/models/resolve?role=review-gate&author_family=codex&harness=codex", "", 200)
	if same.Profile != nil || !same.OwnerRequired || same.CommandTemplate != "" {
		t.Fatalf("author family bypassed: %+v", same)
	}
	grok := decode[WorkResolution](t, &p, "GET", "/api/models/resolve?role=review-gate&author_family=claude&harness=grok", "", 200)
	explained := false
	for _, c := range grok.Ladder {
		for _, r := range c.SkipReasons {
			explained = explained || strings.Contains(r, "no registered Grok account")
		}
	}
	if grok.Profile != nil || !grok.OwnerRequired || !explained {
		t.Fatalf("missing Grok account reported as capacity: %+v", grok)
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET allowed_model_profile_ids='{}' WHERE id=$1`, account)
		return err
	})
	denied := decode[WorkResolution](t, &p, "GET", "/api/models/resolve?role=review-gate&author_family=claude&harness=codex", "", 200)
	if denied.Profile != nil || !denied.OwnerRequired || denied.CommandTemplate != "" {
		t.Fatalf("isolation bypassed account model policy: %+v", denied)
	}
}
