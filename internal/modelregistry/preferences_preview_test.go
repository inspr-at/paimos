// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type previewReadCounts struct{ queries, decoded int }
type previewObservedTx struct {
	pgx.Tx
	reads map[string]*previewReadCounts
}
type previewObservedRows struct {
	pgx.Rows
	counts *previewReadCounts
}

func (r *previewObservedRows) Scan(dest ...any) error {
	r.counts.decoded++
	return r.Rows.Scan(dest...)
}
func (tx *previewObservedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if err != nil || !strings.Contains(sql, "model_profiles") && !strings.Contains(sql, "model_role_routes") {
		return rows, err
	}
	key := "catalog"
	switch {
	case strings.Contains(sql, "WHERE r.role = $1"):
		key = "ladder/" + args[0].(string)
	case strings.Contains(sql, "SELECT role, priority, profile_id"):
		key = "all-routes"
	case strings.Contains(sql, "WHERE NOT EXISTS"):
		key = "picker"
	case strings.Contains(sql, "ANY($1::uuid[])"):
		key = "membership"
	}
	if tx.reads[key] == nil {
		tx.reads[key] = &previewReadCounts{}
	}
	tx.reads[key].queries++
	return &previewObservedRows{Rows: rows, counts: tx.reads[key]}, nil
}

func TestPreferenceGETBoundedLargeCatalog(t *testing.T) {
	for _, scenario := range []string{"active-catalog", "retired-catalog", "oversized-ladders"} {
		t.Run(scenario, func(t *testing.T) {
			p, h := editorFixture(t)
			project := editorProject(t, p, "PREVIEW-1")
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled)
 SELECT $1,'preview-'||n,'1','claude','anthropic',CASE WHEN n%2=0 THEN 'claude-opus-5' ELSE 'claude-sonnet-5' END,'xhigh','strong',true FROM generate_series(1,1024) n`, p.TenantID); err != nil {
					return err
				}
				if scenario == "retired-catalog" {
					if _, err := tx.Exec(t.Context(), `INSERT INTO model_profile_retirements(tenant_id,profile_id,retired_by,reason)
 SELECT tenant_id,id,$1,'Preview regression history' FROM model_profiles WHERE slug LIKE 'preview-%'`, p.ID); err != nil {
						return err
					}
				}
				if scenario == "oversized-ladders" {
					_, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state)
 SELECT p.tenant_id,r.role,1000+row_number() OVER(PARTITION BY r.role ORDER BY p.id),p.id,'available'
 FROM model_profiles p CROSS JOIN (VALUES ('build'),('build-hard'),('review-gate')) r(role) WHERE p.slug LIKE 'preview-%'`)
					return err
				}
				// Distinct selectors defeat per-cell reuse alone. Both inherited views
				// and different cells must share the same bounded catalog read.
				scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "default"})
				if err != nil {
					return err
				}
				for i, slug := range []string{"backend", "frontend", "infra", "docs", "review"} {
					kind, _, err := modelprefs.LookupKind(t.Context(), tx, slug, "")
					if err != nil {
						return err
					}
					var pin string
					if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug=$1`, fmt.Sprintf("preview-%d", i+1)).Scan(&pin); err != nil {
						return err
					}
					if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind.ID, modelprefs.Row{Cells: map[string]modelprefs.Cell{
						"normal":  {Mode: "pinned", ProfileID: pin},
						"complex": {Mode: "latest", Family: "anthropic", Line: "opus", Effort: "xhigh"},
					}}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var measured preferenceDocument
			ctx := tenant.WithPrincipal(t.Context(), p)
			if err := (&Module{pool: appPool}).readSnapshot(ctx, p.TenantID, func(tx pgx.Tx) error {
				observed := &previewObservedTx{Tx: tx, reads: map[string]*previewReadCounts{}}
				var err error
				measured, err = (&Module{}).preferenceDocument(ctx, observed, p, project)
				if err != nil {
					return err
				}
				total := 0
				for key, counts := range observed.reads {
					t.Logf("%s: %d decoded rows, %d queries", key, counts.decoded, counts.queries)
					total += counts.decoded
					if key == "all-routes" || counts.queries != 1 || counts.decoded > 257 {
						t.Errorf("unbounded or repeated preview read %s: %+v", key, counts)
					}
				}
				if total > 257*5+256 {
					t.Errorf("decoded %d rows; require picker + membership + catalog + three bounded ladders", total)
				}
				if scenario != "oversized-ladders" && (observed.reads["catalog"] == nil || observed.reads["catalog"].decoded != 257) {
					t.Error("large catalog, including retired rows, must hit its overflow sentinel exactly once")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Exercise the real middleware/GET and verify the measured document is
			// exactly what the editor receives, not an isolated resolver result.
			doc := h.prefs(t, p, project)
			if !reflect.DeepEqual(doc, measured) {
				t.Fatal("GET differs from measured preview snapshot")
			}
			gotJSON, measuredJSON := editorJSON(doc), editorJSON(measured)
			if gotJSON != measuredJSON {
				at := 0
				for at < len(gotJSON) && at < len(measuredJSON) && gotJSON[at] == measuredJSON[at] {
					at++
				}
				t.Fatalf("GET differs from measured preview snapshot at %d: got %.150s; measured %.150s", at, gotJSON[at:], measuredJSON[at:])
			}
			for level, view := range doc.Views {
				raw, err := json.Marshal(view)
				if err != nil {
					t.Fatal(err)
				}
				var flags map[string]any
				if err := json.Unmarshal(raw, &flags); err != nil {
					t.Fatal(err)
				}
				if view == nil || flags["resolution_truncated"] != true || view.ChoicesTruncated != (scenario != "retired-catalog") {
					t.Fatalf("%s did not report catalog/preview truncation independently: choices=%v resolution=%v", level, flags["choices_truncated"], flags["resolution_truncated"])
				}
				if scenario == "oversized-ladders" {
					for _, choice := range view.Choices {
						if strings.HasPrefix(choice.Profile.Slug, "preview-") && !choice.ReviewLadder {
							t.Fatal("bounded preview ladder incorrectly limited picker membership", choice.Profile.Slug)
						}
					}
				}
				blocked := 0
				for _, row := range view.Rows {
					for _, cell := range []preferenceEffective{row.Normal, row.Complex} {
						if scenario != "oversized-ladders" && cell.Selector.Mode == "auto" {
							continue
						}
						blocked++
						if cell.Profile != nil || cell.TodayVersion != "" || !strings.Contains(cell.UnavailableReason, "Model preview incomplete:") || !strings.Contains(strings.Join(row.Warnings, ";"), cell.UnavailableReason) {
							t.Errorf("%s returned a guessed or silently partial resolution: %+v", level, cell)
						}
					}
				}
				if blocked == 0 {
					t.Fatal("fixture never exercised a bounded preview")
				}
			}
		})
	}
}

func TestPreferencePreviewMatchesLiveResolution(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		var pin string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='codex-sol-xhigh'`).Scan(&pin); err != nil {
			return err
		}
		eu := "eu"
		scope, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "person", PersonID: &p.ID, Residency: &eu})
		if err != nil {
			return err
		}
		for _, slug := range []string{"backend", "review"} {
			kind, _, err := modelprefs.LookupKind(t.Context(), tx, slug, "")
			if err != nil {
				return err
			}
			if err := modelprefs.PutRow(t.Context(), tx, p, scope, kind.ID, modelprefs.Row{Cells: map[string]modelprefs.Cell{
				"normal": {Mode: "pinned", ProfileID: pin}, "complex": {Mode: "latest", Family: "anthropic", Line: "opus", Effort: "xhigh"},
			}}); err != nil {
				return err
			}
		}
		observed := &previewObservedTx{Tx: tx, reads: map[string]*previewReadCounts{}}
		doc, err := (&Module{}).preferenceDocument(t.Context(), observed, p, "")
		if err != nil {
			return err
		}
		for key, counts := range observed.reads {
			if counts.queries != 1 || counts.decoded > 256 || key == "all-routes" {
				t.Errorf("complete preview did not reuse bounded read %s: %+v", key, counts)
			}
		}
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		for level, view := range doc.Views {
			if view == nil {
				continue
			}
			for _, row := range view.Rows {
				slug := ""
				for _, kind := range doc.Kinds {
					if kind.ID == row.KindID {
						slug = kind.Slug
					}
				}
				for bucket, cell := range map[string]preferenceEffective{"normal": row.Normal, "complex": row.Complex} {
					preview := p
					q := WorkQuery{Role: "build", Area: slug, Complexity: "M"}
					if level == "default" {
						preview.Kind = tenant.Agent
						preview.KeyCreatorID = ""
					} else {
						q.PersonID = &p.ID
					}
					if bucket == "complex" {
						q.Role, q.Complexity = "build-hard", "L"
					}
					if slug == "review" {
						q.Role, q.AuthorFamily = "review-gate", "openai"
					}
					live, err := ResolveWork(t.Context(), tx, preview, q, now)
					if err != nil {
						return err
					}
					reason := live.Trace.Fallback
					if live.Trace.Blocked != "" {
						reason = live.Trace.Blocked
					}
					if !reflect.DeepEqual(cell.Profile, live.Profile) || cell.UnavailableReason != reason {
						t.Errorf("%s/%s/%s preview changed live choice or refusal: preview=%+v live=%+v", level, slug, bucket, cell, live)
					}
				}
			}
		}
		return nil
	})
}
