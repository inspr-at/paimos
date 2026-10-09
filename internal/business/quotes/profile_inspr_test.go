// SPDX-License-Identifier: AGPL-3.0-only
package quotes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/quotepdf"
)

// The neutral INSPR document profile (AEON-155) is checked in as a bundle
// directory; `just quote-profile-inspr` packs it into the operator tar.
const insprProfileDir = "profiles/inspr"

type insprProvenance struct {
	Fonts []struct {
		File        string `json:"file"`
		Family      string `json:"family"`
		Weight      int    `json:"weight"`
		Style       string `json:"style"`
		License     string `json:"license"`
		LicenseFile string `json:"license_file"`
		SHA256      string `json:"sha256"`
	} `json:"fonts"`
	Mark struct {
		File         string `json:"file"`
		OriginSHA256 string `json:"origin_sha256"`
		SHA256       string `json:"sha256"`
	} `json:"mark"`
}

func insprBundle(t *testing.T) (ProfileBundle, profileWrite) {
	t.Helper()
	bundle, err := ReadProfileBundleDir(insprProfileDir)
	if err != nil {
		t.Fatal(err)
	}
	var in profileWrite
	decoder := json.NewDecoder(bytes.NewReader(bundle.Profile))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		t.Fatalf("profile.json: %v", err)
	}
	return bundle, in
}

func fileSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestINSPRProfileBundleSources(t *testing.T) {
	bundle, in := insprBundle(t)
	if in.Name != "INSPR" || in.Definition.LayoutVariant != "classic-v1" || in.Definition.Locale != "de-AT" {
		t.Fatalf("profile identity: %q %q %q", in.Name, in.Definition.LayoutVariant, in.Definition.Locale)
	}
	var provenance insprProvenance
	if err := json.Unmarshal(bundle.Files["provenance.json"], &provenance); err != nil {
		t.Fatalf("provenance.json: %v", err)
	}
	// Every face the profile loads is an unmodified OFL release recorded with
	// its licence text in the bundle, and nothing else is loaded.
	recorded := map[string]bool{}
	for _, font := range provenance.Fonts {
		data := bundle.Files[font.File]
		if fileSHA(data) != font.SHA256 {
			t.Errorf("%s: digest differs from provenance", font.File)
		}
		if profileAssetType(data) != "font/woff2" {
			t.Errorf("%s: not a WOFF2 font", font.File)
		}
		license := string(bundle.Files[font.LicenseFile])
		if font.License != "OFL-1.1" || !strings.Contains(license, "SIL Open Font License, Version 1.1") || !strings.Contains(license, "Reserved Font Name") {
			t.Errorf("%s: licence %q (%s) is not the recorded OFL text", font.File, font.License, font.LicenseFile)
		}
		recorded[font.File] = true
	}
	if len(in.Definition.Fonts) != len(provenance.Fonts) {
		t.Fatalf("profile loads %d faces, provenance records %d", len(in.Definition.Fonts), len(provenance.Fonts))
	}
	for _, font := range in.Definition.Fonts {
		if !recorded[font.AssetID] {
			t.Errorf("font %s has no provenance", font.AssetID)
		}
	}
	for _, need := range []struct {
		role   string
		weight int
		style  string
	}{{"body", 400, "normal"}, {"body", 400, "italic"}, {"body", 600, "normal"}, {"body", 700, "normal"}, {"display", 400, "normal"}} {
		found := false
		for _, font := range in.Definition.Fonts {
			found = found || font.Role == need.role && font.Weight == need.weight && font.Style == need.style
		}
		if !found {
			t.Errorf("no %s face at %d %s; the classic-v1 layout would synthesise it", need.role, need.weight, need.style)
		}
	}
	// The mark is the design reference's exact mark: only non-geometry
	// metadata the SVG validator rejects was removed.
	mark := bundle.Files[in.Definition.Footer.AssetID]
	if in.Definition.Footer.AssetID != provenance.Mark.File || fileSHA(mark) != provenance.Mark.SHA256 || provenance.Mark.OriginSHA256 != "970f026a738885b60bf9fac122b3563fe2b9fe242199845920acdc3975628396" {
		t.Fatal("footer mark does not match its recorded provenance")
	}
	if profileAssetType(mark) != "image/svg+xml" || !bytes.Contains(mark, []byte(`viewBox="0 0 1254 1254"`)) || !bytes.Contains(mark, []byte(`fill="#0E6F6C"`)) || !bytes.Contains(mark, []byte(`fill="#D69B31"`)) || bytes.Count(mark, []byte("<path ")) != 5 {
		t.Fatal("footer mark is not the exact two-colour mark")
	}
	// No company, register or tax data rides in the profile.
	raw := strings.ToLower(string(bundle.Profile))
	for _, forbidden := range []string{"example-business", "gmbh", "iban", "firmenbuch", "@"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("profile.json carries company-specific text %q", forbidden)
		}
	}
	// The definition passes the API validator once paths become asset IDs.
	definition := in.Definition
	definition.Fonts = append([]profileFont(nil), in.Definition.Fonts...)
	kinds := map[string]string{}
	for i := range definition.Fonts {
		id := syntheticAssetID(fileSHA(bundle.Files[definition.Fonts[i].AssetID]))
		kinds[id] = profileAssetType(bundle.Files[definition.Fonts[i].AssetID])
		definition.Fonts[i].AssetID = id
	}
	markID := syntheticAssetID(fileSHA(mark))
	kinds[markID] = "image/svg+xml"
	definition.Footer.AssetID = markID
	if err := validateProfileWithAssets(definition, func(id string) (string, error) {
		if kind, ok := kinds[id]; ok {
			return kind, nil
		}
		return "", pgx.ErrNoRows
	}); err != nil {
		t.Fatalf("INSPR profile rejected: %v", err)
	}
	// The packed tar is reproducible and reads back to the same bundle.
	var first, second bytes.Buffer
	if err := WriteProfileBundleTar(&first, bundle); err != nil {
		t.Fatal(err)
	}
	if err := WriteProfileBundleTar(&second, bundle); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("bundle tar is not reproducible")
	}
	back, err := ReadProfileBundleTar(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Profile, bundle.Profile) || len(back.Files) != len(bundle.Files) {
		t.Fatal("bundle tar lost files")
	}
	for name, data := range bundle.Files {
		if !bytes.Equal(back.Files[name], data) {
			t.Fatalf("bundle tar changed %s", name)
		}
	}
}

// TestINSPRProfileAppliesAndRendersSampleQuote applies the packed bundle to a
// fresh tenant as the operator command does, then prints sample quotes under
// it through the quote PDF renderer (Playwright's Chromium or the image's).
func TestINSPRProfileAppliesAndRendersSampleQuote(t *testing.T) {
	database := dbtest.Open(t)
	ctx := context.Background()
	tenantID := "1a5e0155-0000-4000-8000-000000000155"
	var adminID string
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,'inspr','INSPR')`, tenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Synthetic Admin',ARRAY['admin']) RETURNING id::text`, tenantID).Scan(&adminID); err != nil {
			return err
		}
		if err := dbtest.BindLegacyTx(ctx, tx, tenantID, adminID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO quote_settings(tenant_id,revision,numbering_time_zone,default_currency,sender,defaults,layout,updated_by_principal_id) VALUES($1::uuid,1,'Europe/Vienna','EUR','{}'::jsonb,'{}'::jsonb,'{}'::jsonb,$2::uuid)`, tenantID, adminID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	source, _ := insprBundle(t)
	var packed bytes.Buffer
	if err := WriteProfileBundleTar(&packed, source); err != nil {
		t.Fatal(err)
	}
	bundle, err := ReadProfileBundleTar(&packed)
	if err != nil {
		t.Fatal(err)
	}
	filesDir := t.TempDir()
	plan, err := ApplyProfileBundle(ctx, database.App, tenantID, adminID, filesDir, "", bundle, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "create" || plan.AssetsCreated != 8 || !plan.DefaultChange || plan.Applied {
		t.Fatalf("dry run: %+v", plan)
	}
	applied, err := ApplyProfileBundle(ctx, database.App, tenantID, adminID, filesDir, "", bundle, true, true)
	if err != nil || applied.Action != "create" || applied.Revision != 1 || applied.ProfileID == "" {
		t.Fatalf("apply: %+v: %v", applied, err)
	}
	replay, err := ApplyProfileBundle(ctx, database.App, tenantID, adminID, filesDir, "", bundle, true, true)
	if err != nil || replay.Action != "unchanged" || replay.AssetsCreated != 0 || replay.DefaultChange {
		t.Fatalf("replay: %+v: %v", replay, err)
	}
	var snapshot *documentProfileSnapshot
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		settings, err := readSettings(ctx, tx)
		if err != nil {
			return err
		}
		if settings.DefaultProfileID != applied.ProfileID {
			t.Errorf("default profile %q, want %q", settings.DefaultProfileID, applied.ProfileID)
		}
		snapshot, err = readProfileSnapshot(ctx, tx, applied.ProfileID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if !quotepdf.Available() {
		t.Skip("Chromium unavailable")
	}
	if _, err := os.Stat("../../../web/dist/quote-print.html"); err != nil {
		t.Skip("build web assets first")
	}
	// The product's own profile preview quote: cover and terms, then items.
	renderINSPRSample(t, database, tenantID, filesDir, snapshot, "inspr-profile-sample", [][]string{
		{"ANGEBOT", "Modernisierung der Hallensteuerung", "Beispiel Stahl GmbH", "Ihr Unternehmen", "I. LEISTUNGSBESCHREIBUNG", "Seite 1 von 2"},
		{"II. LEISTUNGSAUFSTELLUNG", "Bestandsaufnahme", "Nettosumme", "€ 21.096,00", "Seite 2 von 2"},
	})
	// A longer quote: the terms run onto a second page, the items start on
	// their own page as classic-v1 lays them out and continue on a fourth
	// under a repeated table header, followed by the totals and signatures.
	renderINSPRSample(t, database, tenantID, filesDir, snapshot, "inspr-profile-sample-long", [][]string{
		{"ANGEBOT", "I. LEISTUNGSBESCHREIBUNG", "Ausgangslage", "Seite 1 von 4"},
		{"Zahlungsbedingungen", "Seite 2 von 4"},
		{"II. LEISTUNGSAUFSTELLUNG", "POS. LEISTUNG", "Bestandsaufnahme", "Seite 3 von 4"},
		{"POS. LEISTUNG", "Nachbetreuung", "Nettosumme", "€ 36.492,00", "Ort, Datum, Unterschrift Auftraggeber", "Seite 4 von 4"},
	})
}

// renderINSPRSample prints testdata/<name>.json under the applied profile and
// checks the text of each page; AEON_PROFILE_SAMPLE_DIR keeps <name>.pdf.
func renderINSPRSample(t *testing.T, database *dbtest.DB, tenantID, filesDir string, snapshot *documentProfileSnapshot, name string, pages [][]string) {
	t.Helper()
	ctx := t.Context()
	sample, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(sample, &document); err != nil {
		t.Fatal(err)
	}
	if document["profile"], err = json.Marshal(snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := quotepdf.LoadProfileAssets(ctx, database.App, attachments.Store{FilesDir: filesDir}, tenantID, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 8 {
		t.Fatalf("profile assets loaded: %d, want 8", len(assets))
	}
	renderCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	pdf, err := quotepdf.Render(renderCtx, os.DirFS("../../../web/dist"), quotepdf.Payload{Document: raw, OfferNo: "A260925-07", ProfileAssets: assets})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if dir := os.Getenv("AEON_PROFILE_SAMPLE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, name+".pdf"), pdf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	box := regexp.MustCompile(`/MediaBox\s*\[\s*0\s+0\s+(59[45](?:\.[0-9]+)?)\s+(84[12](?:\.[0-9]+)?)\s*\]`)
	if !box.Match(pdf) {
		t.Fatalf("%s: PDF is not A4", name)
	}
	// Chromium embeds the profile's own faces; no fallback font is printed.
	for _, face := range []string{"SourceSans3-Regular", "SourceSans3-Semibold", "SourceSerif4Display-Regular"} {
		if !bytes.Contains(pdf, []byte(face)) {
			t.Errorf("%s: PDF does not embed %s", name, face)
		}
	}
	if bytes.Contains(pdf, []byte("JetBrains")) {
		t.Errorf("%s: PDF falls back to the app's monospace font", name)
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Log("pdftotext unavailable; PDF text check requires the release runner")
		return
	}
	cmd := exec.CommandContext(renderCtx, "pdftotext", "-layout", "-", "-")
	cmd.Stdin = bytes.NewReader(pdf)
	text, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	printed := strings.Split(strings.TrimSuffix(string(text), "\f"), "\f")
	if len(printed) != len(pages) {
		t.Fatalf("%s: %d pages, want %d", name, len(printed), len(pages))
	}
	// Tracked capitals come out of pdftotext with gaps between letters, and
	// CSS sets some labels in capitals: compare without spaces or case.
	squash := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), "")) }
	for i, wants := range pages {
		page := squash(printed[i])
		for _, want := range wants {
			if !strings.Contains(page, squash(want)) {
				t.Errorf("%s page %d missing %q", name, i+1, want)
			}
		}
	}
}
