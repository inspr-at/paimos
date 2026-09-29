// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/rulesimport"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestImportDraftOnlyIdempotentAndTenantIsolated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS-KERNEL.md")
	body := "# Synthetic kernel\n\n## Safety\n\n<!-- aeon-rule: safety.floor -->\n- 🔴 Never print synthetic secrets.\n  Why: secrets stay inside the tenant.\n  Details: PACKTOKEN stays in details.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal := importProposal(t, path)
	if !strings.Contains(proposal.Rules[0].Sources[0].HeadingPath, "Safety") || proposal.Rules[0].Sources[0].SHA256 == "" {
		t.Fatalf("lineage %+v", proposal.Rules[0].Sources)
	}

	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('rules-import','Rules import') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	admin := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: "importer"}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','importer') RETURNING id::text`, tid).Scan(&admin.ID)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, admin.ID, "admin")

	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(p tenant.Principal, method, urlPath string, in any, want int) []byte {
		t.Helper()
		var reader *strings.Reader
		if in == nil {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(string(jsonBytes(in)))
		}
		req := httptest.NewRequest(method, urlPath, reader)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: status %d want %d: %s", method, urlPath, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var layer Layer
	if err := json.Unmarshal(call(admin, "POST", "/api/rules/layers", Scope{Layer: "company"}, 200), &layer); err != nil {
		t.Fatal(err)
	}
	var set Set
	if err := json.Unmarshal(call(admin, "POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": "Imported safety"}, 200), &set); err != nil {
		t.Fatal(err)
	}
	var publishedHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/publish") || strings.Contains(r.URL.Path, "/restore") {
			publishedHits++
			http.Error(w, "publish refused", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), admin)))
	}))
	defer srv.Close()
	api := client.New(srv.URL, "synthetic-test-token")

	first, err := rulesimport.ApplyDraft(ctx, api, proposal, rulesimport.Target{SetID: set.ID, Revision: set.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != "draft" || first.Added != 1 || first.Updated != 0 || first.Revision != set.Revision+1 {
		t.Fatalf("first import %+v", first)
	}
	second, err := rulesimport.ApplyDraft(ctx, api, proposal, rulesimport.Target{SetID: set.ID, Revision: first.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if second.Mode != "unchanged" || second.Unchanged != 1 || second.Updated != 0 || second.Revision != first.Revision {
		t.Fatalf("re-import %+v", second)
	}

	updatedBody := strings.Replace(body, "secrets stay inside the tenant.", "secrets stay inside the tenant boundary.", 1)
	if err := os.WriteFile(path, []byte(updatedBody), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := importProposal(t, path)
	third, err := rulesimport.ApplyDraft(ctx, api, changed, rulesimport.Target{SetID: set.ID, Revision: second.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if third.Mode != "draft" || third.Updated != 1 || third.Added != 0 || third.Revision != second.Revision+1 {
		t.Fatalf("hash update %+v", third)
	}
	if publishedHits != 0 {
		t.Fatal("import requested publication")
	}

	var stored Set
	if err := json.Unmarshal(call(admin, "GET", "/api/rules/sets/"+set.ID, nil, 200), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.PublishedVersion != "" || stored.Revision != third.Revision || len(stored.Rules) != 1 {
		t.Fatalf("stored draft %+v", stored)
	}
	if stored.Rules[0].Why != "secrets stay inside the tenant boundary." || !strings.Contains(stored.Rules[0].Details, "PACKTOKEN") || !strings.Contains(stored.Rules[0].Details, "heading_path") {
		t.Fatalf("updated draft lost lineage or details: %+v", stored.Rules[0])
	}
	if stored.Rules[0].Source.EditedHere {
		t.Fatal("import set edited_here")
	}
	edited := stored.Rules[0]
	edited.Why = "Edited in Aeon after import."
	var editedSet Set
	if err := json.Unmarshal(call(admin, "PUT", "/api/rules/sets/"+set.ID+"/draft", map[string]any{
		"expected_revision": stored.Revision,
		"name":              stored.Name,
		"rules":             []Rule{edited},
	}, 200), &editedSet); err != nil {
		t.Fatal(err)
	}
	if _, err := rulesimport.ApplyDraft(ctx, api, changed, rulesimport.Target{SetID: set.ID, Revision: editedSet.Revision}); !errors.Is(err, rulesimport.ErrDraftConflict) {
		t.Fatalf("diverged Aeon edit was overwritten: %v", err)
	}
	var kept Set
	if err := json.Unmarshal(call(admin, "GET", "/api/rules/sets/"+set.ID, nil, 200), &kept); err != nil {
		t.Fatal(err)
	}
	if kept.Revision != editedSet.Revision || len(kept.Rules) != 1 || kept.Rules[0].Why != "Edited in Aeon after import." || !strings.Contains(kept.Rules[0].Details, "PACKTOKEN") {
		t.Fatalf("Aeon edit was not kept: %+v", kept.Rules)
	}

	var foreign string
	if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('rules-import-foreign','Foreign') RETURNING id::text`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	fp := tenant.Principal{TenantID: foreign, Kind: tenant.Person, Name: "foreign-admin"}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, foreign, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','foreign-admin') RETURNING id::text`, foreign).Scan(&fp.ID)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, foreign, fp.ID, "admin")
	call(fp, "GET", "/api/rules/sets/"+set.ID, nil, 404)
	var visible int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, foreign, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id=$1::uuid`, set.ID).Scan(&visible)
	}); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("foreign tenant saw %d rule nodes", visible)
	}
	var owned, published int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE tenant_id=$1 AND id=$2::uuid`, tid, set.ID).Scan(&owned); err != nil || owned != 1 {
		t.Fatal("owner row missing", err, owned)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='rules.published'`, tid).Scan(&published); err != nil || published != 0 {
		t.Fatal("draft import published", err, published)
	}
	var replaced int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='rules.draft_replaced'`, tid).Scan(&replaced); err != nil || replaced != 3 {
		t.Fatal("draft writes", err, replaced)
	}
}

func importProposal(t *testing.T, path string) rulesimport.Proposal {
	t.Helper()
	proposal, err := rulesimport.Build(context.Background(), rulesimport.Request{Context: rulesimport.ContextTemplate, Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rulesimport.MapDraft(proposal); err != nil {
		t.Fatal(err)
	}
	return proposal
}
