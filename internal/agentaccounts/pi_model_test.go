// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/openrouter"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPiAccountModelPermissionCatalogAndImmutablePins(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "pi-model", "person", "admin", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", []string{"admin"})
	viewer := addPrincipal(t, admin.TenantID, "person", "viewer", []string{"viewer"})
	foreign := makePrincipal(t, "pi-foreign", "person", "other", []string{"admin"})
	token := issueKey(t, runner, []string{"account.manage", "account.read"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/models" || r.Header.Get("Authorization") != "" {
			t.Error("catalog used credential")
		}
		fmt.Fprint(w, `{"data":[{"id":"vendor/model","pricing":{"prompt":"1","completion":"1"}}]}`)
	}))
	defer server.Close()
	mod := &Module{pool: appPool, openRouter: openrouter.Catalog{Client: openrouter.Client{Base: server.URL}}}
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"pi-local","harness":"pi","daemon_id":"daemon-pi","label":"Local pi"}`, 201, &a)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET provider='openrouter' WHERE id=$1`, a.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/agent-accounts/" + a.ID + "/model"
	for _, p := range []struct {
		who    *tenant.Principal
		token  string
		status int
	}{{&runner, token, 403}, {&viewer, "", 403}, {&foreign, "", 404}, {nil, "", 401}} {
		callStatus(t, mod, p.who, p.token, "PUT", path, `{"model":"vendor/model"}`, p.status, nil)
	}
	for _, body := range []string{`{"model":"not-a-slug"}`, `{"model":"vendor/model","api_key":"obviously-fake-key"}`, `{"model":"vendor/model","provider":"openai"}`} {
		callStatus(t, mod, &admin, "", "PUT", path, body, 400, nil)
	}
	callStatus(t, mod, &admin, "", "PUT", path, `{"model":"vendor/model"}`, 200, &a)
	if a.Model != "vendor/model" || a.ModelStatus != "known" || len(a.AllowedProfileIDs) != 1 {
		t.Fatal("model pin missing")
	}
	first := a.AllowedProfileIDs[0]
	callStatus(t, mod, &admin, "", "PUT", path, `{"model":"stealth/unlisted"}`, 200, &a)
	if a.ModelStatus != "unknown" || !a.ModelDataNote || a.AllowedProfileIDs[0] == first {
		t.Fatal("unknown model not saved separately")
	}
	if scalar(t, admin, `SELECT count(*) FROM model_profiles WHERE id=$1 AND model='openrouter/vendor/model'`, first) != 1 {
		t.Fatal("prior run pin rewritten")
	}
	var catalog Catalog
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/catalog", "", 200, &catalog)
	models := catalog.Hosts[0].Harnesses[0].Accounts[0].Models
	if len(models) != 1 || models[0].Model != "openrouter/stealth/unlisted" {
		t.Fatal("planning catalog not updated")
	}
	mod.openRouter = openrouter.Catalog{Client: openrouter.Client{Base: "http://127.0.0.1:1"}}
	callStatus(t, mod, &admin, "", "PUT", path, `{"model":"vendor/offline"}`, 200, &a)
	if a.ModelStatus != "unknown" {
		t.Fatal("outage was not advisory")
	}
}
