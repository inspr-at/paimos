// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Playwright consumes the real Build + HTTP output, not a hand-annotated API
// response. In the PMA-shaped tenant there is no AEON project or ticket source.
func TestProductNotesBrowserFixture(t *testing.T) {
	out := os.Getenv("AEON_RELEASE_FIXTURE_OUT")
	if out == "" {
		t.Skip("browser fixture export only")
	}
	dir := repo(t)
	const v = "260923143005.0.0"
	const bundle = `{"schema":"aeon.product-release-notes.v1","product":"PAIMOS AEON","repository":"inspr-at/aeon","releases":{"260923143005.0.0":{"snapshot_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","captured_at":"2026-09-23T14:30:05Z","release_revision":1,"items":[{"key":"AEON-15","pill_en":"Clear release notes","pill_de":"Klare Release Notizen","benefit_en":"Read what changed in every workspace.","benefit_de":"Lies die Änderungen in jedem Arbeitsbereich.","group":"features"},{"key":"AEON-16","pill_en":"Reliable release switches","pill_de":"Zuverlässige Release Umschalter","benefit_en":"Switch between benefits and commits.","benefit_de":"Wechsle zwischen Nutzen und Commits.","group":"fixes"}]},"260923134631.0.0":{"snapshot_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","captured_at":"2026-09-23T13:46:31Z","release_revision":1,"items":[{"key":"AEON-13","pill_en":"Earlier release notes","pill_de":"","benefit_en":"This release is available in English.","benefit_de":"","group":"features"}]}}}`
	path := filepath.Join(dir, "internal/releasehistory/data/product-notes.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(bundle), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := Build(context.Background(), Options{Repo: dir, Repository: "inspr-at/aeon"})
	if err != nil {
		t.Fatal(err)
	}
	mod := NewWith(h, v)
	mod.UseTickets(func(context.Context, string, []string) (map[string]TicketMeta, error) {
		return map[string]TicketMeta{}, nil
	})
	mux := http.NewServeMux()
	mod.Mount(mux)
	req := httptest.NewRequest("GET", "/api/releases", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", TenantID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Person}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("history status %d", w.Code)
	}
	var response Response
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, w.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}
