// SPDX-License-Identifier: AGPL-3.0-only

package doctrine_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/doctrinerepo"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: changing the host mirror must preserve both HTTP and managed-session
// delivery at the indexed pin, with an explicit stale notice and no reindex.
func TestDoctrineMarkerAdvancePreservesSessionDelivery(t *testing.T) {
	d := dbtest.Open(t)
	var tid, project string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('mirror-session','Mirror session') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: "owner"}
	agent := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Name: "builder"}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','owner') RETURNING id::text`, tid).Scan(&owner.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','builder') RETURNING id::text`, tid).Scan(&agent.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'MIRROR-1','Mirror' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	agent.KeyCreatorID = owner.ID
	dbtest.BindRole(t, d, tid, owner.ID, "admin")
	mux := http.NewServeMux()
	rules.New(d.App).Mount(mux)
	dir := t.TempDir()
	repository := doctrinerepo.Default().Private()
	commit, next := strings.Repeat("3", 40), strings.Repeat("4", 40)
	tree := filepath.Join(dir, repository)
	if err := os.MkdirAll(filepath.Join(tree, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, raw := range map[string]string{
		filepath.Join(tree, ".aeon-commit"):              commit + "\n",
		filepath.Join(tree, "docs/AGENTS-KERNEL.md"):     "# Kernel\n\n## Safety\n\n- Keep the private rule on the harness channel.\n",
		filepath.Join(dir, "host-mirror.allowlist.json"): `{"grants":[{"tenant_id":"` + tid + `","repository":"` + repository + `"}]}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
			t.Fatal(err)
		}
	}
	m := doctrine.New(d.App, doctrine.Options{MirrorDir: dir, DefaultSource: true, App: doctrine.AppConfig{TenantID: tid}})
	doctrine.SimulateReadOnlyMirrorForTest(m)
	handler := m.CatalogMiddleware(mux)
	call := func(method, path string, in any) []byte {
		t.Helper()
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), owner))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var layer rules.Layer
	if err := json.Unmarshal(call("POST", "/api/rules/layers", rules.Scope{Layer: "company"}), &layer); err != nil {
		t.Fatal(err)
	}
	var set rules.Set
	if err := json.Unmarshal(call("POST", "/api/rules/sets", map[string]any{"layer_id": layer.ID, "name": "Safety"}), &set); err != nil {
		t.Fatal(err)
	}
	floor := rules.Rule{Identity: "safety", Text: "Keep the locked company floor.", Why: "Safety applies to every session.", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "fixture:floor", EditedHere: true}}
	call("PUT", "/api/rules/sets/"+set.ID+"/draft", map[string]any{"expected_revision": 1, "name": "Safety", "rules": []rules.Rule{floor}})
	call("POST", "/api/rules/sets/"+set.ID+"/publish", map[string]any{"expected_revision": 2, "version": "260928120000.0.0"})
	if err := m.EnsureDefaultSource(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := "/api/rules/merged?project_id=" + project + "&person_id=" + owner.ID + "&role=builder&harness=codex"
	call("GET", path, nil)
	if err := os.WriteFile(filepath.Join(tree, ".aeon-commit"), []byte(next+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var delivered rules.Merged
	if err := json.Unmarshal(call("GET", path, nil), &delivered); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(delivered.Body, commit) || !strings.Contains(delivered.Body, "stale") || strings.Contains(delivered.Body, next) || !strings.Contains(delivered.Floor, floor.Text) {
		t.Fatalf("HTTP delivery lost the stale pin or safety floor: %+v", delivered)
	}
	var managed rules.Merged
	var deliveryErr error
	// The middleware supplies the same host policy to managed delivery as it
	// does to the HTTP endpoint, without granting another tenant's authority.
	m.CatalogMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deliveryErr = db.InTenant(r.Context(), d.App, tid, func(tx pgx.Tx) error {
			var err error
			managed, err = rules.ForManagedSession(r.Context(), tx, agent, project, "", "codex", rules.LegacyMaxBytes)
			return err
		})
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if deliveryErr != nil || managed.Body != delivered.Body || managed.SHA256 != delivered.SHA256 {
		t.Fatalf("managed delivery differs: body=%q err=%v", managed.Body, deliveryErr)
	}
}
