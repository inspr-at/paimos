// SPDX-License-Identifier: AGPL-3.0-only

package quoteshowcase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

// seedWithINSPR prepares a tenant the way production was prepared: the real
// INSPR profile (with its fonts) is the default, then the ACME bundle adds its
// own English profile for one quote (AEON-274).
func seedWithINSPR(t *testing.T) (*dbtest.DB, string) {
	t.Helper()
	database := dbtest.Open(t)
	ctx := context.Background()
	tenantID, err := tenantbootstrap.Create(ctx, database.App, "showcase", "Showcase")
	if err != nil {
		t.Fatal(err)
	}
	sender := []byte(`{"company":"INSPR GmbH","street":"Musterstraße 1","postal_code":"8010","city":"Graz","country":"Österreich","email":"quotes@inspr.example"}`)
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		for slug, prefix := range map[string]string{"organisation": "ORG", "contact": "CON", "quote": "QUO"} {
			if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1::uuid,$2,$2,$3,$2)`, tenantID, slug, prefix); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO quote_settings(tenant_id,revision,numbering_time_zone,default_currency,sender,defaults,layout,updated_by_principal_id) SELECT $1::uuid,1,'Europe/Vienna','EUR',$2::jsonb,'{}'::jsonb,'{}'::jsonb,id FROM principals WHERE kind='agent' AND name='Tenant bootstrap'`, tenantID, sender)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inspr, err := quotes.ReadProfileBundleDir(filepath.Join(acmeDir(t), "..", "..", "profiles", "inspr"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := quotes.ApplyProfileBundle(ctx, database.App, tenantID, "", t.TempDir(), "", inspr, true, true); err != nil {
		t.Fatal(err)
	}
	return database, tenantID
}

// showcaseDocuments returns every document the quote page can load for each
// showcase quote: the draft (GET /draft serves it byte for byte) and each
// frozen version snapshot (GET /versions/{n}).
func showcaseDocuments(t *testing.T, database *dbtest.DB, tenantID string) map[string]map[string]json.RawMessage {
	t.Helper()
	ctx := context.Background()
	out := map[string]map[string]json.RawMessage{}
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT n.fields->>'showcase_key','draft',d.document FROM quote_drafts d JOIN nodes n ON n.id=d.quote_node_id
			UNION ALL SELECT n.fields->>'showcase_key','v'||s.version,s.document FROM quote_version_snapshots s JOIN nodes n ON n.id=s.quote_node_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, which string
			var raw []byte
			if err := rows.Scan(&key, &which, &raw); err != nil {
				return err
			}
			if out[key] == nil {
				out[key] = map[string]json.RawMessage{}
			}
			out[key][which] = raw
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestShowcaseQuotesAreUILoadable(t *testing.T) {
	database, tenantID := seedWithINSPR(t)
	ctx := context.Background()
	bundle, err := ReadDir(acmeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, nil, testLinkKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Quotes) != 5 {
		t.Fatalf("quotes: %+v", report.Quotes)
	}
	docs := showcaseDocuments(t, database, tenantID)
	for _, quote := range report.Quotes {
		if docs[quote.Key]["draft"] == nil && docs[quote.Key]["v1"] == nil {
			t.Fatalf("%s: no loadable document", quote.Key)
		}
		for which, raw := range docs[quote.Key] {
			if err := quotes.CheckLoadableDocument(raw); err != nil {
				t.Errorf("%s %s: %v", quote.Key, which, err)
			}
		}
	}
	var retainer struct {
		Profile struct {
			Definition struct {
				Locale string `json:"locale"`
			} `json:"definition"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(docs["acme-retainer"]["draft"], &retainer); err != nil || retainer.Profile.Definition.Locale != "en" {
		t.Fatalf("retainer does not carry the ACME English profile: %v", err)
	}
	if path := os.Getenv("AEON_SHOWCASE_FIXTURE_OUT"); path != "" {
		writeShowcaseFixture(t, path, report, docs)
	}
}

// writeShowcaseFixture dumps the documents the UI receives so the Playwright
// spec can open every showcase quote with the real API shape.
func writeShowcaseFixture(t *testing.T, path string, report Report, docs map[string]map[string]json.RawMessage) {
	t.Helper()
	type fixtureQuote struct {
		Key      string                     `json:"key"`
		OfferNo  string                     `json:"offer_no"`
		State    string                     `json:"state"`
		Title    string                     `json:"title"`
		Versions int                        `json:"versions"`
		Docs     map[string]json.RawMessage `json:"documents"`
	}
	var out []fixtureQuote
	for i, q := range report.Quotes {
		kept := map[string]json.RawMessage{}
		for which, raw := range docs[q.Key] {
			kept[which] = raw
		}
		// An issued quote's draft equals its latest version; the spec serves
		// that version as the draft, so the fixture keeps it once.
		if latest := kept[fmt.Sprintf("v%d", q.Versions)]; latest != nil && bytes.Equal(latest, kept["draft"]) {
			delete(kept, "draft")
		}
		out = append(out, fixtureQuote{Key: q.Key, OfferNo: fmt.Sprintf("A260929-%02d", i+1), State: q.State, Title: q.Title, Versions: q.Versions, Docs: kept})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Production stored A260929-04 before the fix: the ACME English revision and
// the draft's snapshot of it both had "fonts": null. Re-applying the profile
// with --refresh-drafts stores a clean revision and re-points the draft; a
// second run changes nothing (AEON-274).
func TestRefreshDraftsRepairsANullProfileSnapshot(t *testing.T) {
	database, tenantID := seedWithINSPR(t)
	ctx := context.Background()
	bundle, err := ReadDir(acmeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, nil, testLinkKey(), true); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO quote_document_profile_revisions(tenant_id,profile_id,revision,name,definition,created_by_principal_id)
			SELECT r.tenant_id,r.profile_id,2,r.name,jsonb_set(r.definition,'{fonts}','null'),r.created_by_principal_id FROM quote_document_profile_revisions r JOIN quote_document_profiles p ON p.id=r.profile_id AND r.revision=1 WHERE p.name='ACME English'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE quote_document_profiles SET current_revision=2 WHERE name='ACME English'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE quote_drafts d SET document=jsonb_set(jsonb_set(d.document,'{profile,revision}','2'),'{profile,definition,fonts}','null') FROM nodes n WHERE n.id=d.quote_node_id AND n.fields->>'showcase_key'='acme-retainer'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := quotes.CheckLoadableDocument(showcaseDocuments(t, database, tenantID)["acme-retainer"]["draft"]); err == nil {
		t.Fatal("the simulated production draft should not be loadable")
	}
	profile := quotes.ProfileBundle{Profile: bundle.Profiles[0].Raw}
	plain, err := quotes.ApplyProfileBundle(ctx, database.App, tenantID, "", t.TempDir(), "", profile, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Action != "update" || plain.Revision != 3 || plain.DraftsChanged != 0 {
		t.Fatalf("plain plan: %+v", plain)
	}
	planned, err := quotes.ApplyProfileBundleRefreshingDrafts(ctx, database.App, tenantID, "", t.TempDir(), "", profile, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Action != "update" || planned.DraftsChanged != 1 || planned.Applied {
		t.Fatalf("refresh plan: %+v", planned)
	}
	applied, err := quotes.ApplyProfileBundleRefreshingDrafts(ctx, database.App, tenantID, "", t.TempDir(), "", profile, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Action != "update" || applied.Revision != 3 || applied.DraftsChanged != 1 {
		t.Fatalf("refresh apply: %+v", applied)
	}
	for key, docs := range showcaseDocuments(t, database, tenantID) {
		for which, raw := range docs {
			if err := quotes.CheckLoadableDocument(raw); err != nil {
				t.Errorf("%s %s after repair: %v", key, which, err)
			}
		}
	}
	again, err := quotes.ApplyProfileBundleRefreshingDrafts(ctx, database.App, tenantID, "", t.TempDir(), "", profile, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Action != "unchanged" || again.Revision != 3 || again.DraftsChanged != 0 {
		t.Fatalf("second run: %+v", again)
	}
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		var events, revision int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events e JOIN nodes n ON n.id=e.node_id WHERE e.type='quote.profile_selected' AND n.fields->>'showcase_key'='acme-retainer'`).Scan(&events); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT (d.document->'profile'->>'revision')::int FROM quote_drafts d JOIN nodes n ON n.id=d.quote_node_id WHERE n.fields->>'showcase_key'='acme-retainer'`).Scan(&revision); err != nil {
			return err
		}
		if events != 1 || revision != 3 {
			return fmt.Errorf("events %d, snapshot revision %d", events, revision)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
