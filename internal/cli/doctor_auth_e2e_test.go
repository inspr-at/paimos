// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestProjectOnlyAgentDoctorAuth(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	ctx := t.Context()
	d := dbtest.Open(t)
	if err := db.EnsureTenant(ctx, d.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	m, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{9}, 32), BootstrapTenantSlug: "aeon", BootstrapAdminEmail: "admin@example.com"}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: d.App,
		Modules:    []httpapi.Module{m, authz.New(d.App), nodes.New(d.App, nodes.SQLWriter{}), events.New(d.App), modelregistry.New(d.App)},
		Middleware: []func(http.Handler) http.Handler{m.Middleware},
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	key := mintAgent(t, srv.URL, "project-only")
	var projectID, otherProjectID, roleID string
	seed := dbtest.Seed(ctx)
	if err := db.InTenant(seed, d.App, key.TenantID, func(tx pgx.Tx) error {
		for i, id := range []*string{&projectID, &otherProjectID} {
			if err := tx.QueryRow(seed, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1::uuid,id,$2,$2 FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project' RETURNING id::text`, key.TenantID, []string{"SELF-1", "OTHER-1"}[i]).Scan(id); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(seed, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'project_identity','Project identity') RETURNING id::text`, key.TenantID).Scan(&roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(seed, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'nodes.read')`, key.TenantID, roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(seed, `UPDATE role_bindings SET role_id=$3::uuid,scope_type='project',scope_id=$4::uuid WHERE tenant_id=$1::uuid AND principal_id=$2::uuid`, key.TenantID, key.PrincipalID, roleID, projectID); err != nil {
			return err
		}
		_, err := tx.Exec(seed, `UPDATE agent_keys SET scopes=ARRAY['nodes.read'] WHERE tenant_id=$1::uuid AND principal_id=$2::uuid`, key.TenantID, key.PrincipalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := tenant.Principal{ID: key.PrincipalID, TenantID: key.TenantID, Kind: tenant.Agent}
	effective, err := authz.Load(tenant.WithPrincipal(ctx, p), d.App, p, projectID)
	if err != nil || effective.Workspace.Role != nil || effective.Project == nil || effective.Project.Role == nil {
		t.Fatal("fixture must have a project binding and no workspace binding")
	}
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", key.Token)
	missing := filepath.Join(t.TempDir(), "no-config.yaml")
	code, out, errOut := runCLI([]string{"paimos", "--config", missing, "--json", "auth", "whoami"}, "")
	if code != 0 || errOut != "" {
		t.Fatalf("project-only whoami: exit %d", code)
	}
	var who struct{ Principal client.Principal }
	if err := json.Unmarshal([]byte(out), &who); err != nil || who.Principal.ID != key.PrincipalID {
		t.Fatal("whoami must identify the project-only caller")
	}
	code, out, errOut = runCLI([]string{"paimos", "--config", missing, "--json", "doctor"}, "")
	if code != 0 || errOut != "" || strings.Contains(out, key.Token) {
		t.Fatalf("project-only doctor: exit %d", code)
	}
	var checks []doctorCheck
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatal(err)
	}
	var authOK bool
	for _, check := range checks {
		if check.Name == "auth" {
			authOK = check.Status == "ok" && check.Detail == "user="+who.Principal.Name
		}
	}
	if !authOK {
		t.Fatal("doctor auth must pass for the same caller as whoami")
	}
	hc := &http.Client{}
	header := http.Header{"Authorization": {"Bearer " + key.Token}}
	for _, path := range []string{"/api/members", "/api/agent-keys", "/api/events", "/api/models", "/api/me/profile", "/api/me/greeting", "/api/me/permissions"} {
		if status, _ := doJSON(t, hc, http.MethodGet, srv.URL+path, "", header); status != http.StatusForbidden {
			t.Errorf("project-only key reached %s: %d", path, status)
		}
	}
	if status, _ := doJSON(t, hc, http.MethodGet, srv.URL+"/api/nodes/"+projectID, "", header); status != http.StatusOK {
		t.Fatalf("project-only key lost its project access: %d", status)
	}
	if status, _ := doJSON(t, hc, http.MethodGet, srv.URL+"/api/nodes/"+otherProjectID, "", header); status != http.StatusForbidden && status != http.StatusNotFound {
		t.Fatalf("project-only key reached another project: %d", status)
	}
}
