// SPDX-License-Identifier: AGPL-3.0-only

package quoteshowcase

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func acmeDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "quotes", "showcase", "acme-labs")
}

func TestACMEBundleShape(t *testing.T) {
	bundle, err := ReadDir(acmeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Organisations) != 1 || bundle.Organisations[0].Key != "acme-labs" || len(bundle.Organisations[0].Contacts) != 2 {
		t.Fatalf("organisation: %+v", bundle.Organisations)
	}
	if len(bundle.Profiles) != 1 || bundle.Profiles[0].Name != "ACME English" {
		t.Fatalf("profiles: %+v", bundle.Profiles)
	}
	want := []string{"acme-acceptance", "acme-fitout", "acme-platform", "acme-retainer"}
	if len(bundle.Quotes) != len(want) {
		t.Fatalf("quotes: %d", len(bundle.Quotes))
	}
	for i, key := range want {
		if bundle.Quotes[i].Key != key {
			t.Fatalf("quote order: got %s want %s", bundle.Quotes[i].Key, key)
		}
	}
	byKey := map[string]QuoteSpec{}
	for _, quote := range bundle.Quotes {
		byKey[quote.Key] = quote
	}
	if !byKey["acme-platform"].PublicLink || byKey["acme-platform"].State != "issued" || len(byKey["acme-platform"].Versions) != 1 {
		t.Fatal("platform shape")
	}
	if byKey["acme-fitout"].PublicLink || len(byKey["acme-fitout"].Versions) != 2 || byKey["acme-fitout"].State != "issued" {
		t.Fatal("fitout shape")
	}
	if byKey["acme-retainer"].State != "draft" || byKey["acme-retainer"].ProfileName != "ACME English" {
		t.Fatal("retainer shape")
	}
	if byKey["acme-acceptance"].State != "accepted" || !bundle.Organisations[0].Contacts[0].Principal {
		t.Fatal("acceptance shape")
	}
	for _, key := range []string{"sections-numbering", "optional-scope", "public-link", "locales", "variables"} {
		if bundle.Features[key] == "" {
			t.Fatalf("missing feature %s", key)
		}
	}
	if bundle.AddFiles == "" {
		t.Fatal("add_files")
	}
}

func TestUnknownBundleField(t *testing.T) {
	dir := t.TempDir()
	for _, pair := range []struct{ name, body string }{
		{"manifest.json", `{"schema":"aeon.quote-showcase.v1","features":{"x":"y"},"add_files":"add a file","extra":true}`},
	} {
		if err := os.WriteFile(filepath.Join(dir, pair.name), []byte(pair.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadDir(dir); err == nil {
		t.Fatal("expected unknown field to fail")
	}
}

func TestShowcaseDryRunApplyAndArchive(t *testing.T) {
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
	profile := quotes.ProfileBundle{Profile: []byte(`{"name":"Synthetic print","definition":{"schema":"inspr.document-profile.v1","layout_variant":"classic-v1","locale":"de-AT","fonts":[],"colors":{"ink":"#253335","muted":"#637477","soft":"#91a1a3","accent":"#287f78","rule":"#d5dfdf","paper":"#ffffff"},"typography":{"body_pt":"10"},"page":{"width_mm":"210","height_mm":"297","top_mm":"18","right_mm":"20","bottom_mm":"16","left_mm":"22"},"cover":{"top_mm":"11"},"sections":{"numbering":"upper-roman"},"positions_table":{"columns":[{"key":"position","width_mm":"9"},{"key":"description","width_mm":"71"},{"key":"quantity","width_mm":"15"},{"key":"unit","width_mm":"22"},{"key":"unit_price","width_mm":"24"},{"key":"total","width_mm":"27"}],"separator":"rule","repeat_header":true},"totals":{"vat":"note","discount":"hidden","net_label":"Net"},"payment_terms":{"position":"sections","heading":"Payment"},"acceptance":{"signature_columns":2,"gap_mm":"14","lead_mm":"28"},"footer":{"width_mm":"33","offset_mm":"0","page_number_format":"PAGE {page} OF {total}"},"labels":{"quote":"QUOTE"}}}`)}
	if _, err := quotes.ApplyProfileBundle(ctx, database.App, tenantID, "", t.TempDir(), "", profile, true, true); err != nil {
		t.Fatal(err)
	}
	bundle, err := ReadDir(acmeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	linkKey := make([]byte, 32)
	for i := range linkKey {
		linkKey[i] = byte(i + 1)
	}
	planned, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, nil, linkKey, false)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Applied || len(planned.Quotes) != 4 || len(planned.Archives) != 0 {
		t.Fatalf("dry-run report: %+v", planned)
	}
	for _, quote := range planned.Quotes {
		if quote.Action != "create" || quote.ID != "" || quote.OfferNo != "" {
			t.Fatalf("dry-run quote: %+v", quote)
		}
	}
	if planned.Quotes[0].Key != "acme-acceptance" || planned.Quotes[2].Key != "acme-platform" || planned.Quotes[2].PublicLink != "create" || planned.Quotes[3].PublicLink != "none" || planned.Quotes[3].State != "draft" || planned.Quotes[3].Versions != 0 || planned.Quotes[1].Versions != 2 {
		t.Fatalf("dry-run quotes: %+v", planned.Quotes)
	}
	var rows int
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM business_quotes`).Scan(&rows)
	}); err != nil || rows != 0 {
		t.Fatalf("dry-run wrote %d quotes: %v", rows, err)
	}
	withoutKey, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if withoutKey.Quotes[2].PublicLink != "needs_link_key" {
		t.Fatalf("link plan: %+v", withoutKey.Quotes[2])
	}
	if _, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, nil, nil, true); err == nil {
		t.Fatal("apply without a link key should fail")
	}
	applied, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, nil, linkKey, true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied {
		t.Fatal("apply flag")
	}
	again, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, nil, linkKey, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, quote := range again.Quotes {
		if quote.Action != "unchanged" {
			t.Fatalf("second apply: %+v", quote)
		}
	}
	var retainerID string
	err = db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		var orgs, contacts, people, links, otherLinks int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='organisation' AND n.fields->>'showcase_key'='acme-labs'`).Scan(&orgs); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='contact' AND n.fields->>'showcase_key' LIKE 'acme-%'`).Scan(&contacts); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM principals WHERE kind='person' AND name='Lena Hofer'`).Scan(&people); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM quote_public_links l JOIN nodes n ON n.id=l.quote_node_id WHERE n.fields->>'showcase_key'='acme-platform' AND l.revoked_at IS NULL`).Scan(&links); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM quote_public_links l JOIN nodes n ON n.id=l.quote_node_id WHERE n.fields->>'showcase_key'<>'acme-platform'`).Scan(&otherLinks); err != nil {
			return err
		}
		if orgs != 1 || contacts != 2 || people != 1 || links != 1 || otherLinks != 0 {
			t.Fatalf("rows org=%d contact=%d people=%d links=%d other=%d", orgs, contacts, people, links, otherLinks)
		}
		checks := map[string]struct {
			state   string
			version int
		}{
			"acme-platform":   {"issued", 1},
			"acme-fitout":     {"issued", 2},
			"acme-retainer":   {"draft", 0},
			"acme-acceptance": {"accepted", 1},
		}
		for key, want := range checks {
			var state string
			var version int
			var archived bool
			if err := tx.QueryRow(ctx, `SELECT q.state,q.current_version,q.archived_at IS NOT NULL FROM business_quotes q JOIN nodes n ON n.id=q.quote_node_id WHERE n.fields->>'showcase_key'=$1`, key).Scan(&state, &version, &archived); err != nil {
				return err
			}
			if state != want.state || version != want.version || archived {
				t.Fatalf("%s state=%s version=%d archived=%v", key, state, version, archived)
			}
		}
		var locale string
		if err := tx.QueryRow(ctx, `SELECT s.document->'profile'->'definition'->>'locale' FROM quote_version_snapshots s JOIN nodes n ON n.id=s.quote_node_id WHERE n.fields->>'showcase_key'='acme-platform' AND s.version=1`).Scan(&locale); err != nil {
			return err
		}
		if locale != "de-AT" {
			t.Fatalf("platform locale %s", locale)
		}
		if err := tx.QueryRow(ctx, `SELECT d.document->'profile'->'definition'->>'locale' FROM quote_drafts d JOIN nodes n ON n.id=d.quote_node_id WHERE n.fields->>'showcase_key'='acme-retainer'`).Scan(&locale); err != nil {
			return err
		}
		if locale != "en" {
			t.Fatalf("retainer locale %s", locale)
		}
		var channel string
		if err := tx.QueryRow(ctx, `SELECT d.channel FROM quote_decisions d JOIN nodes n ON n.id=d.quote_node_id WHERE n.fields->>'showcase_key'='acme-acceptance'`).Scan(&channel); err != nil {
			return err
		}
		if channel != "authenticated" {
			t.Fatalf("decision %s", channel)
		}
		return tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n WHERE n.fields->>'showcase_key'='acme-retainer'`).Scan(&retainerID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, []string{retainerID, "11111111-1111-4111-8111-111111111111"}, linkKey, true); err == nil {
		t.Fatal("missing archive id should fail")
	}
	var archived bool
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM business_quotes WHERE quote_node_id=$1::uuid`, retainerID).Scan(&archived)
	}); err != nil || archived {
		t.Fatalf("archive rolled back: archived=%v err=%v", archived, err)
	}
	archivedReport, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, []string{retainerID}, linkKey, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(archivedReport.Archives) != 1 || archivedReport.Archives[0].Action != "archive" || archivedReport.Archives[0].ID != retainerID {
		t.Fatalf("archive report: %+v", archivedReport.Archives)
	}
	repeat, err := Apply(ctx, database.App, tenantID, "", t.TempDir(), bundle, []string{retainerID}, linkKey, true)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Archives[0].Action != "already_archived" {
		t.Fatalf("repeat archive: %+v", repeat.Archives)
	}
	var before, after []byte
	var events int
	err = db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='quote.visibility_changed' AND node_id=$1::uuid`, retainerID).Scan(&events); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT before,after FROM events WHERE type='quote.visibility_changed' AND node_id=$1::uuid ORDER BY id`, retainerID).Scan(&before, &after)
	})
	if err != nil {
		t.Fatal(err)
	}
	var beforeArchived, afterArchived struct {
		Archived *bool `json:"archived"`
	}
	if err := json.Unmarshal(before, &beforeArchived); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &afterArchived); err != nil {
		t.Fatal(err)
	}
	if events != 1 || beforeArchived.Archived == nil || afterArchived.Archived == nil || *beforeArchived.Archived || !*afterArchived.Archived {
		t.Fatalf("visibility event %d %s %s", events, before, after)
	}
	raw, err := json.Marshal(planned)
	if err != nil || !json.Valid(raw) {
		t.Fatal(err)
	}
}
