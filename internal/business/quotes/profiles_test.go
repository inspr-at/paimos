// SPDX-License-Identifier: AGPL-3.0-only
package quotes

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

func syntheticProfile() profileDefinition {
	return profileDefinition{
		Schema: "inspr.document-profile.v1", LayoutVariant: "classic-v1", Locale: "de-AT", Fonts: []profileFont{},
		Colors:     map[string]string{"ink": "#253335", "muted": "#637477", "soft": "#91a1a3", "accent": "#287f78", "rule": "#d5dfdf", "paper": "#ffffff"},
		Typography: map[string]string{"body_pt": "10", "title_pt": "17"},
		Page:       profilePage{WidthMM: "210", HeightMM: "297", TopMM: "18", RightMM: "20", BottomMM: "16", LeftMM: "22"},
		Cover:      map[string]string{"top_mm": "11"}, Sections: map[string]string{"numbering": "upper-roman"},
		PositionsTable: profileTable{Columns: []profileColumn{{Key: "position", WidthMM: "9"}, {Key: "description", WidthMM: "71"}, {Key: "quantity", WidthMM: "15"}, {Key: "unit", WidthMM: "22"}, {Key: "unit_price", WidthMM: "24"}, {Key: "total", WidthMM: "27"}}, Separator: "rule", RepeatHeader: true},
		Totals:         profileTotals{VAT: "note", Discount: "hidden", NetLabel: "Net total"}, PaymentTerms: profilePayment{Position: "sections", Heading: "Payment"},
		Acceptance: profileAcceptance{SignatureColumns: 2, GapMM: "14", LeadMM: "28"}, Footer: profileFooter{WidthMM: "33", OffsetMM: "0", PageNumberFormat: "PAGE {page} OF {total}"},
		Labels: map[string]string{"quote": "QUOTE"},
	}
}

func TestProfileGeometryAndSVGValidation(t *testing.T) {
	for _, value := range []string{"-6", "-0.5", "0", "10"} {
		if !validProfileSignedDecimal(value, -6, 10) {
			t.Fatalf("valid footer offset %q rejected", value)
		}
	}
	for _, value := range []string{"-6.01", "10.01", "-50", "2px", ""} {
		if validProfileSignedDecimal(value, -6, 10) {
			t.Fatalf("invalid footer offset %q accepted", value)
		}
	}
	if !safeProfileSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path d="M0 0L10 10" fill="#123456"/></svg>`)) {
		t.Fatal("safe SVG rejected")
	}
	if safeProfileSVG([]byte("<svg\nonload=\"alert(1)\"></svg>")) {
		t.Fatal("unsafe SVG accepted")
	}
}

func TestProfileAssetRejectsCustomerBeforeDatabaseAccess(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	req := httptest.NewRequest(http.MethodGet, "/api/quote-profiles/assets/"+id, nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: id, TenantID: id, Kind: tenant.Person, Roles: []string{"customer"}}))
	rec := httptest.NewRecorder()
	(&Module{}).profileAssetGet(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("customer profile asset status %d", rec.Code)
	}
}

func TestProfileRevisionsAssetsAndTenantIsolation(t *testing.T) {
	t.Setenv("AEON_FILES_DIR", t.TempDir())
	database := dbtest.Open(t)
	ctx := context.Background()
	reg := plugins.NewRegistry()
	for _, id := range []string{"business_costs", "business_crm"} {
		plug := plugins.Plugin{Manifest: plugins.Manifest{ID: id, Version: "1", Owner: "aeon", Permissions: []string{fence.PermNodesContribute}}}
		digest, err := plugins.Digest(plug)
		if err != nil {
			t.Fatal(err)
		}
		plug.Manifest.DigestSHA256 = digest
		if err = reg.Register(plug); err != nil {
			t.Fatal(err)
		}
	}
	quotePlugin, err := ManifestPlugin()
	if err != nil {
		t.Fatal(err)
	}
	if err = reg.Register(quotePlugin); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	actors := map[string]tenant.Principal{}
	for _, spec := range []struct{ tenantID, slug string }{{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "example-one"}, {"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "example-two"}} {
		err = db.InTenant(dbtest.Seed(ctx), database.App, spec.tenantID, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,$2,'Synthetic tenant')`, spec.tenantID, spec.slug); e != nil {
				return e
			}
			var id string
			if e := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Example Admin',ARRAY['admin']) RETURNING id::text`, spec.tenantID).Scan(&id); e != nil {
				return e
			}
			actors[spec.slug] = tenant.Principal{ID: id, TenantID: spec.tenantID, Kind: tenant.Person, Roles: []string{"admin"}}
			if e := dbtest.BindLegacyTx(ctx, tx, spec.tenantID, id); e != nil {
				return e
			}
			for _, pluginID := range []string{"business_costs", "business_crm", PluginID} {
				plug, _ := reg.Lookup(pluginID)
				if _, e := tx.Exec(ctx, `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id) VALUES($1::uuid,$2,$3,$4,$5,true,$6,$7::uuid)`, spec.tenantID, pluginID, plug.Manifest.Version, plug.Manifest.DigestSHA256, plug.Manifest.Owner, plug.Manifest.Permissions, id); e != nil {
					return e
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	module, err := New(database.App, reg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	module.Mount(mux)
	call := func(actor, method, path string, body []byte) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(tenant.WithPrincipal(ctx, actors[actor]))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	definition := syntheticProfile()
	input, _ := json.Marshal(profileWrite{Name: "Beispiel Stahl GmbH", Definition: definition})
	status, body := call("example-one", "POST", "/api/quote-profiles", input)
	if status != 201 {
		t.Fatalf("create %d %s", status, body)
	}
	var first profileRow
	if err = json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.Definition.LayoutVariant != "classic-v1" {
		t.Fatalf("first revision %+v", first)
	}
	status, _ = call("example-two", "GET", "/api/quote-profiles/"+first.ID, nil)
	if status != 404 {
		t.Fatalf("cross tenant read %d", status)
	}
	definition.Colors["accent"] = "#558877"
	input, _ = json.Marshal(profileWrite{ExpectedRevision: 1, Name: first.Name, Definition: definition})
	status, body = call("example-one", "PATCH", "/api/quote-profiles/"+first.ID, input)
	if status != 200 {
		t.Fatalf("update %d %s", status, body)
	}
	status, body = call("example-one", "GET", "/api/quote-profiles/"+first.ID+"?revision=1", nil)
	if status != 200 {
		t.Fatalf("old revision %d %s", status, body)
	}
	var old profileRow
	if err = json.Unmarshal(body, &old); err != nil {
		t.Fatal(err)
	}
	if old.Definition.Colors["accent"] != first.Definition.Colors["accent"] {
		t.Fatal("retained revision changed")
	}
	status, _ = call("example-one", "PATCH", "/api/quote-profiles/"+first.ID, input)
	if status != 409 {
		t.Fatalf("stale update %d", status)
	}
	status, body = call("example-one", "POST", "/api/quote-profiles/"+first.ID+"/undo", []byte(`{"expected_revision":2}`))
	if status != 200 {
		t.Fatalf("undo %d %s", status, body)
	}
	var restored profileRow
	if err = json.Unmarshal(body, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 3 || restored.Definition.Colors["accent"] != first.Definition.Colors["accent"] {
		t.Fatal("undo did not append restored revision")
	}
	actor := actors["example-one"]
	if err = db.InTenant(dbtest.Seed(ctx), database.App, actor.TenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO quote_settings(tenant_id,revision,numbering_time_zone,default_currency,sender,defaults,layout,updated_by_principal_id,default_profile_id) VALUES($1::uuid,1,'UTC','EUR','{}'::jsonb,'{}'::jsonb,'{}'::jsonb,$2::uuid,$3::uuid)`, actor.TenantID, actor.ID, first.ID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	status, body = call("example-one", "DELETE", "/api/quote-profiles/"+first.ID, nil)
	if status != 204 {
		t.Fatalf("archive %d %s", status, body)
	}
	status, body = call("example-one", "GET", "/api/quotes/settings", nil)
	var settings quoteSettings
	if status != 200 || json.Unmarshal(body, &settings) != nil || settings.DefaultProfileID != "" || settings.Revision != 2 {
		t.Fatalf("archive did not clear default profile: %d %s", status, body)
	}
	status, body = call("example-one", "POST", "/api/quote-profiles/"+first.ID+"/undo", []byte(`{"expected_revision":3}`))
	if status != 200 || json.Unmarshal(body, &restored) != nil || restored.Archived {
		t.Fatalf("archive undo %d %s", status, body)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 30, G: 60, B: 90, A: 255})
	var pngBytes bytes.Buffer
	if err = png.Encode(&pngBytes, img); err != nil {
		t.Fatal(err)
	}
	status, body = call("example-one", "POST", "/api/quote-profiles/assets", pngBytes.Bytes())
	if status != 201 {
		t.Fatalf("upload %d %s", status, body)
	}
	var asset profileAsset
	if err = json.Unmarshal(body, &asset); err != nil {
		t.Fatal(err)
	}
	status, body = call("example-one", "GET", "/api/quote-profiles/assets/"+asset.ID, nil)
	if status != 200 || !bytes.Equal(body, pngBytes.Bytes()) {
		t.Fatalf("asset content %d", status)
	}
	status, _ = call("example-two", "GET", "/api/quote-profiles/assets/"+asset.ID, nil)
	if status != 404 {
		t.Fatalf("cross tenant asset %d", status)
	}
	status, _ = call("example-one", "POST", "/api/quote-profiles/assets", []byte(`<svg><script>bad</script></svg>`))
	if status != 400 {
		t.Fatalf("unsafe SVG accepted: %d", status)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 12 4"><circle cx="2" cy="2" r="2" fill="#287f78"/></svg>`)
	status, body = call("example-one", "POST", "/api/quote-profiles/assets", svg)
	if status != 201 || json.Unmarshal(body, &asset) != nil {
		t.Fatalf("safe brand asset upload %d", status)
	}
	brandID := asset.ID
	svgReq := httptest.NewRequest(http.MethodGet, "/api/quote-profiles/assets/"+brandID, nil).WithContext(tenant.WithPrincipal(ctx, actors["example-one"]))
	svgRec := httptest.NewRecorder()
	mux.ServeHTTP(svgRec, svgReq)
	if svgRec.Code != 200 || svgRec.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" || svgRec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("svg asset %d csp %q", svgRec.Code, svgRec.Header().Get("Content-Security-Policy"))
	}
	font := make([]byte, 48)
	copy(font, "wOF2")
	binary.BigEndian.PutUint32(font[8:12], uint32(len(font)))
	binary.BigEndian.PutUint16(font[12:14], 1)
	status, body = call("example-one", "POST", "/api/quote-profiles/assets", font)
	if status != 201 || json.Unmarshal(body, &asset) != nil || asset.ContentType != "font/woff2" {
		t.Fatalf("profile font upload %d", status)
	}
	status, body = call("example-one", "GET", "/api/quote-profiles/assets/"+asset.ID, nil)
	if status != 200 || !bytes.Equal(body, font) {
		t.Fatalf("profile font roundtrip %d", status)
	}
	fontReq := httptest.NewRequest(http.MethodGet, "/api/quote-profiles/assets/"+asset.ID, nil).WithContext(tenant.WithPrincipal(ctx, actors["example-one"]))
	fontRec := httptest.NewRecorder()
	mux.ServeHTTP(fontRec, fontReq)
	if fontRec.Code != 200 || fontRec.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("font CSP %q", fontRec.Header().Get("Content-Security-Policy"))
	}
	// A registered asset whose bytes are not in the store (a restored database
	// without its files) is not found, not a server error (AEON-140).
	stored, err := os.ReadDir(os.Getenv("AEON_FILES_DIR"))
	if err != nil || len(stored) == 0 {
		t.Fatalf("store is empty: %v", err)
	}
	for _, entry := range stored {
		if err := os.RemoveAll(filepath.Join(os.Getenv("AEON_FILES_DIR"), entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	status, _ = call("example-one", "GET", "/api/quote-profiles/assets/"+asset.ID, nil)
	if status != 404 {
		t.Fatalf("asset without bytes %d, want 404", status)
	}
	definition.Cover["brand_asset_id"] = brandID
	definition.Footer.DotsAssetID = brandID
	definition.Fonts = []profileFont{{Role: "body", Family: "Synthetic", Weight: 400, Style: "normal", AssetID: asset.ID}}
	input, _ = json.Marshal(profileWrite{ExpectedRevision: restored.Revision, Name: restored.Name, Definition: definition})
	status, body = call("example-one", "PATCH", "/api/quote-profiles/"+first.ID, input)
	if status != 200 || json.Unmarshal(body, &restored) != nil || restored.Definition.Cover["brand_asset_id"] != brandID {
		t.Fatalf("brand and font profile revision %d", status)
	}
}
