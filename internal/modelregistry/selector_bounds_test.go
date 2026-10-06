// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type selectorObservedTx struct {
	pgx.Tx
	decoded, queries int
}
type selectorObservedRows struct {
	pgx.Rows
	owner *selectorObservedTx
}

func (r *selectorObservedRows) Scan(dest ...any) error {
	r.owner.decoded++
	return r.Rows.Scan(dest...)
}
func (tx *selectorObservedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if err == nil && strings.Contains(sql, "model_profiles") {
		tx.queries++
		return &selectorObservedRows{Rows: rows, owner: tx}, nil
	}
	return rows, err
}
func (tx *selectorObservedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "model_profiles") {
		tx.queries++
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func TestSelectorValidationBoundedLargeCatalog(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		// Keep the whole catalog in place, including unrelated profiles in the same
		// family/effort, matching revisions, and an oversized review ladder.
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled)
   SELECT $1,'large-'||n,'1','claude','anthropic',CASE WHEN n%2=0 THEN 'claude-opus-5' ELSE 'claude-sonnet-5' END,'xhigh','strong',true FROM generate_series(1,1024) n`, p.TenantID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state)
   SELECT tenant_id,'review-gate',1000+row_number() OVER(ORDER BY id),id,'available' FROM model_profiles WHERE slug LIKE 'large-%'`)
		if err != nil {
			return err
		}
		var pinned string
		if err = tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='large-1024'`).Scan(&pinned); err != nil {
			return err
		}
		for _, cell := range []modelprefs.Cell{{Mode: "pinned", ProfileID: pinned}, {Mode: "latest", Family: "anthropic", Line: "opus", Effort: "xhigh"}} {
			for _, kind := range []string{"backend", "review"} {
				t.Run(cell.Mode+"/"+kind, func(t *testing.T) {
					observed := &selectorObservedTx{Tx: tx}
					if err := validateSelector(t.Context(), observed, cell, kind); err != nil {
						t.Fatal(err)
					}
					if observed.decoded != 0 || observed.queries != 1 {
						t.Fatalf("selector %s/%s decoded %d catalog rows in %d queries; require one bounded existence result", cell.Mode, kind, observed.decoded, observed.queries)
					}
				})
			}
		}
		for _, tt := range []struct {
			cell       modelprefs.Cell
			kind, code string
		}{
			{modelprefs.Cell{Mode: "pinned", ProfileID: "00000000-0000-0000-0000-000000000001"}, "backend", "unknown_profile"},
			{modelprefs.Cell{Mode: "latest", Family: "anthropic", Line: "missing", Effort: "xhigh"}, "backend", "unknown_line"},
			{modelprefs.Cell{Mode: "latest", Family: "anthropic", Line: "opus", Effort: "high"}, "review", "review_floor"},
		} {
			got := validateSelector(t.Context(), tx, tt.cell, tt.kind)
			if got == nil || got.Error() != tt.code {
				t.Fatalf("want %s, got %v", tt.code, got)
			}
		}
		return nil
	})
}

func TestSelectorLatestSQLMatchesProfileLine(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		// Seeded aliases, every harness, plus compound numeric/suffix model forms.
		for i, tt := range []struct{ h, f, m string }{
			{"codex", "openai", "gpt-6.10-sol"}, {"claude", "anthropic", "claude-opus-5-5"},
			{"pi", "anthropic", "anthropic/claude-fable-5"}, {"grok", "xai", "grok-4.7-build-fast"},
			{"cursor", "xai", "grok-4.7-xhigh-fast"}, {"cursor", "cursor", "composer-2.5-fast"},
			{"opencode", "google", "google/gemini-2.5-pro"}, {"opencode", "local", "ollama/qwen3-coder"},
		} {
			if _, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: fmt.Sprintf("line-parity-%d", i), Version: "1", Harness: tt.h, Family: tt.f, Model: tt.m, Effort: "high", Tier: "strong"}); err != nil {
				return err
			}
		}
		profiles, err := listProfiles(t.Context(), tx)
		if err != nil {
			return err
		}
		for _, profile := range profiles {
			family, line, version := ProfileLine(profile)
			if version == "" {
				continue
			}
			cell := modelprefs.Cell{Mode: "latest", Family: family, Line: line, Effort: profile.Effort, Harness: profile.Harness}
			if err := validateSelector(t.Context(), tx, cell, "backend"); err != nil {
				t.Fatalf("%s/%s expected %s: %v", profile.Harness, profile.Model, line, err)
			}
			cell.Line += "-absent"
			if err := validateSelector(t.Context(), tx, cell, "backend"); err == nil || err.Error() != "unknown_line" {
				t.Fatalf("%s/%s wrong line accepted: %v", profile.Harness, profile.Model, err)
			}
		}
		return nil
	})
}

func TestSelectorChecksReuseAndSeparateReviewFloor(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		observed := &selectorObservedTx{Tx: tx}
		checks := selectorChecks{}
		cell := modelprefs.Cell{Mode: "latest", Family: "anthropic", Line: "opus", Effort: "xhigh"}
		for _, kind := range []string{"backend", "other", "review", "review"} {
			if err := checks.validate(t.Context(), observed, cell, kind); err != nil {
				return err
			}
		}
		if observed.queries != 2 {
			t.Fatalf("same selector must reuse validation but check review separately: %d", observed.queries)
		}
		return nil
	})
}
