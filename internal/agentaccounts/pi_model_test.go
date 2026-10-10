// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"crypto/sha256"
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

// Risk: account.manage alone bypasses deny, or accepting a withheld pi model
// mutates its historical pin instead of creating an enabled immutable version.
func TestPiModelUnderDenyRequiresAccountUseManage(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "pi-deny", "person", "Admin", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", []string{"admin"})
	token := issueKey(t, runner, []string{"account.manage", "account.read"})
	mod := &Module{pool: appPool}
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"pi-deny","harness":"pi","daemon_id":"daemon-pi","label":"Pi"}`, 201, &a)
	ownFixtureAccount(t, admin, &a)
	full := "anthropic/claude-opus-5"
	slug := fmt.Sprintf("pi-account-%x", sha256.Sum256([]byte(full)))
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET provider='anthropic' WHERE id=$1`, a.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE account_use_rules SET new_models='deny'`); err != nil {
			return err
		}
		// The legacy pi writer is uncaused and must now be withheld by T5.
		var enabled bool
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'1','pi','anthropic',$3,'off','standard') RETURNING enabled`, admin.TenantID, slug, full).Scan(&enabled); err != nil {
			return err
		}
		if enabled {
			return fmt.Errorf("legacy pi model bypassed deny")
		}
		if _, err := tx.Exec(t.Context(), `WITH r AS (INSERT INTO roles(tenant_id,key,name) VALUES($1,'account_only','Accounts only') RETURNING id) INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,id,unnest(ARRAY['account.manage','account.read']) FROM r`, admin.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='account_only') WHERE principal_id=$1 AND scope_type='workspace'`, admin.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/agent-accounts/" + a.ID + "/model"
	beforeEvents := scalar(t, admin, `SELECT count(*) FROM events WHERE type='account.updated'`)
	callStatus(t, mod, &admin, "", "PUT", path, `{"model":"claude-opus-5"}`, 403, nil)
	if scalar(t, admin, `SELECT count(*) FROM model_profiles WHERE enabled`) != 0 || scalar(t, admin, `SELECT count(*) FROM model_profiles`) != 1 || scalar(t, admin, `SELECT count(*) FROM events WHERE type='account.updated'`) != beforeEvents {
		t.Fatal("denied activation changed pins or audit")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='admin') WHERE principal_id=$1 AND scope_type='workspace'`, admin.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	callStatus(t, mod, &admin, "", "PUT", path, `{"model":"claude-opus-5"}`, 200, &a)
	if len(a.AllowedProfileIDs) != 1 || scalar(t, admin, `SELECT count(*) FROM model_profiles WHERE id=$1 AND enabled AND version='2'`, a.AllowedProfileIDs[0]) != 1 || scalar(t, admin, `SELECT count(*) FROM model_profiles WHERE version='1' AND NOT enabled`) != 1 {
		t.Fatal("person acceptance did not preserve withheld immutable pin")
	}
}

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
	ownFixtureAccount(t, admin, &a)
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
	// Neither a registering agent nor a person can bypass the model binding via
	// display metadata. Revocation remains possible by submitting an empty grant.
	metadata := fmt.Sprintf(`{"label":"Local pi","plan":"","host_label":"","allowed_model_profile_ids":[%q]}`, first)
	for _, caller := range []struct {
		who   *tenant.Principal
		token string
	}{{&admin, ""}, {&runner, token}} {
		callStatus(t, mod, caller.who, caller.token, "PUT", "/api/agent-accounts/"+a.ID+"/metadata", metadata, 400, nil)
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
