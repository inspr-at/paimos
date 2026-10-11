// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"fmt"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Risk: the retired ladder writer still decodes an out-of-range priority and
// prepares or changes the catalog. PUT is 410 before any body read (Models v2).
func TestPriorityOverflowDoesNotPrepareCatalog(t *testing.T) {
	for _, additive := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "additive"}[additive], func(t *testing.T) {
			reset(t)
			p := makePrincipal(t, "priority-overflow", "person", "Owner", []string{"admin"})
			id := "00000000-0000-0000-0000-000000000001"
			if additive {
				incompleteCatalogFixture(t, p)
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles`).Scan(&id)
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := catalogSetupState(t, p)
			body := fmt.Sprintf(`[{"role":"build","priority":2147483648,"profile_id":%q,"state":"available"}]`, id)
			status, raw := call(t, &p, http.MethodPut, "/api/models/routes", body)
			if status != http.StatusGone || !strings.HasPrefix(errorText(raw), "Role ladders moved to Settings > Models") {
				t.Errorf("wrong retired-writer refusal: status=%d body=%s", status, raw)
			}
			if after := catalogSetupState(t, p); after != before {
				t.Error("priority overflow changed profiles, routes, seed events or event counter")
			}
		})
	}
}

// Risk: a retained ladder row at the largest stored priority overflows or
// loses its place when Models v2 migrates the ladder into the Default's order.
func TestMaximumRoutePriorityCanBeStored(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "priority-maximum", "person", "Owner", []string{"admin"})
	profiles := decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", http.StatusOK)
	last, first := profileBySlug(profiles, "claude-opus-xhigh"), profileBySlug(profiles, "codex-6-1-sol-high")
	if last.ID == "" || first.ID == "" {
		t.Fatal("missing catalog fixture")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if err := insertRoute(t.Context(), tx, p.TenantID, Route{Role: "build", Priority: math.MaxInt32, ProfileID: last.ID, State: "available"}); err != nil {
			return err
		}
		if err := insertRoute(t.Context(), tx, p.TenantID, Route{Role: "build", Priority: 1, ProfileID: first.ID, State: "available"}); err != nil {
			return err
		}
		var priority int
		if err := tx.QueryRow(t.Context(), `SELECT priority FROM model_role_routes WHERE role='build' AND profile_id=$1`, last.ID).Scan(&priority); err != nil {
			return err
		}
		if priority != math.MaxInt32 {
			t.Fatalf("maximum priority not stored: %d", priority)
		}
		if _, err := tx.Exec(t.Context(), `SELECT aeon_migrate_models_v2($1)`, p.TenantID); err != nil {
			return err
		}
		var rank, want []string
		if err := tx.QueryRow(t.Context(), `SELECT o.rank FROM model_pref_orders o JOIN model_pref_profiles w ON w.id=o.profile_id
 WHERE w.scope='workspace' AND o.column_key='other' AND o.situation='first'`).Scan(&rank); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT array_agg(aeon_models_line(family,harness,model) ORDER BY id=$2::uuid) FROM model_profiles WHERE id IN ($1::uuid,$2::uuid)`, first.ID, last.ID).Scan(&want); err != nil {
			return err
		}
		if !slices.Equal(rank, want) {
			t.Fatalf("maximum priority lost its place: rank %v, want %v", rank, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlacementSecurityReviewRejectsInvalidStructureBeforePreparation(t *testing.T) {
	for _, tc := range []struct{ name, query, reason string }{
		{"missing author", "&area=backend", "review-gate requires author_family"},
		{"invalid author", "&area=backend&author_family=invalid", `unknown author family "invalid": use openai, anthropic, xai, cursor, google or local (aliases: codex, claude, grok, gemini)`},
		{"invalid harness", "&area=backend&author_family=openai&harness=invalid", "unsupported model harness"},
		{"invalid mode", "&author_family=openai&mode=invalid", "invalid resolution mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			p := makePrincipal(t, "invalid-security-placement", "person", "Owner", []string{"admin"})
			before := catalogSetupState(t, p)
			status, raw := call(t, &p, http.MethodGet, "/api/models/resolve?role=review-gate-security"+tc.query, "")
			var refusal struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &refusal); err != nil {
				t.Fatal(err)
			}
			if status != http.StatusBadRequest || refusal.Error != tc.reason {
				t.Errorf("wrong structural refusal: status=%d body=%s; want %s", status, raw, tc.reason)
			}
			if after := catalogSetupState(t, p); after != before {
				t.Error("invalid security placement persisted catalog preparation")
			}
		})
	}
}

func TestPlacementSecurityReviewRetainsOwnerRequiredExplanation(t *testing.T) {
	for _, ticketRole := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit role", true: "ticket role"}[ticketRole], func(t *testing.T) {
			reset(t)
			p := makePrincipal(t, "security-placement", "person", "Owner", []string{"admin"})
			path := "/api/models/resolve?role=review-gate-security&area=backend&author_family=codex"
			if ticketRole {
				var ticket string
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields)
					 SELECT $1,id,'SECURITY-1','Security review', '{"route_role":"review-gate-security","area":"backend"}'
					 FROM node_kinds WHERE slug='work' RETURNING id::text`, p.TenantID).Scan(&ticket)
				}); err != nil {
					t.Fatal(err)
				}
				path = "/api/models/resolve?ticket=" + ticket + "&author_family=codex"
			}
			got := decode[struct {
				WorkResolution
				Preference PreferenceDecision `json:"preference"`
			}](t, &p, http.MethodGet, path, "", http.StatusOK)
			if got.Role != "review-gate-security" || got.AuthorFamily != "openai" || !got.OwnerRequired || got.Profile != nil || got.CommandTemplate != "" || len(got.Ladder) == 0 {
				t.Fatalf("security review selected an ordinary route: %+v", got)
			}
			for _, candidate := range got.Ladder {
				if candidate.Selected || len(candidate.SkipReasons) == 0 {
					t.Fatalf("security refusal lost candidate reason: %+v", candidate)
				}
			}
			const reason = "no qualified security review profile/account"
			if got.Trace.Blocked != reason || got.Preference.Blocked == nil || got.Preference.Blocked.Reason != reason || got.Preference.Role != "review-gate-security" {
				t.Fatalf("security explanation lost: trace=%+v preference=%+v", got.Trace, got.Preference)
			}
			for _, rule := range []string{"security_review", "cross_family", "review_qualification", "residency"} {
				if !slices.Contains(got.Trace.Hard, rule) || !slices.Contains(got.Preference.HardRules, rule) {
					t.Errorf("missing hard rule %s: trace=%v preference=%v", rule, got.Trace.Hard, got.Preference.HardRules)
				}
			}
		})
	}
}
