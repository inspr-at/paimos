// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestStatusDefinitions(t *testing.T) {
	help := defaultStatusHelp()
	want := []string{"new", "backlog", "open", "blocked", "in_progress", "qa", "done", "delivered", "accepted", "cancelled", "archived"}
	states := []string{}
	for _, def := range help.Definitions {
		states = append(states, def.State)
		if def.Meaning == "" || def.Hint == "" || def.SetBy == "" || def.Exit != (def.State == "cancelled" || def.State == "archived") {
			t.Fatalf("incomplete definition: %+v", def)
		}
		for _, key := range def.Rules {
			if _, ok := help.Autopilot.Rules[key]; !ok {
				t.Fatalf("missing rule %s", key)
			}
		}
	}
	if !reflect.DeepEqual(states, want) || help.Queued.IsStatus || !strings.Contains(help.Queued.Meaning, "AEON-522") {
		t.Fatalf("status order or queued: %+v", help)
	}
	for key, days := range map[string]int{"new": 7, "backlog": 90, "blocked": 14, "progress": 3, "done": 14, "publish": 0, "accept": 30} {
		if rule := help.Autopilot.Rules[key]; !rule.Enabled || rule.Days != days {
			t.Fatalf("default %s: %+v", key, rule)
		}
	}
	for _, master := range []bool{true, false} {
		for _, mode := range []string{"inherit", "on", "off"} {
			for _, on := range []bool{true, false} {
				h := defaultStatusHelp()
				h.Autopilot.Enabled = master
				h.Autopilot.ProjectMode = mode
				h.Autopilot.Rules["accept"] = statusRule{Enabled: on, Days: 1}
				resolveStatusHelp(&h)
				effective := mode == "on" || mode == "inherit" && master
				if h.Autopilot.EffectiveEnabled != effective {
					t.Fatal("incorrect project override")
				}
				accepted := h.Definitions[8]
				if effective && on {
					if !strings.Contains(accepted.Hint, "1 day after") || accepted.SetBy != "Person, or the 1-day rule" {
						t.Fatalf("live accepted hint: %+v", accepted)
					}
				} else if accepted.SetBy != "Person" || strings.Contains(accepted.Hint, "automatically") {
					t.Fatalf("off accepted hint: %+v", accepted)
				}
			}
		}
	}
	// Instances do not share mutable settings or definitions.
	h := defaultStatusHelp()
	h.Autopilot.Rules["new"] = statusRule{}
	if !defaultStatusHelp().Autopilot.Rules["new"].Enabled {
		t.Fatal("defaults mutated")
	}
}

func TestStatusHelpLiveLimitsAndProjectOverrides(t *testing.T) {
	p := newPrincipal(t, "live-status-help")
	var absent bool
	if err := appPool.QueryRow(t.Context(), `SELECT to_regclass('status_autopilot_settings') IS NULL`).Scan(&absent); err != nil {
		t.Fatal(err)
	}
	if absent {
		// The external Part B storage contract, in this test's disposable database.
		// Production persistence stays in 1078; Part A's migration owns no settings.
		_, err := appPool.Exec(t.Context(), `CREATE TABLE status_autopilot_settings(tenant_id uuid PRIMARY KEY REFERENCES tenants(id),enabled bool NOT NULL,rules jsonb NOT NULL);
   CREATE TABLE status_autopilot_projects(tenant_id uuid REFERENCES tenants(id),project_id uuid,mode text,PRIMARY KEY(tenant_id,project_id));
   ALTER TABLE status_autopilot_settings ENABLE ROW LEVEL SECURITY; ALTER TABLE status_autopilot_settings FORCE ROW LEVEL SECURITY;
   ALTER TABLE status_autopilot_projects ENABLE ROW LEVEL SECURITY; ALTER TABLE status_autopilot_projects FORCE ROW LEVEL SECURITY;
   CREATE POLICY test_tenant ON status_autopilot_settings USING(tenant_id=current_setting('aeon.tenant_id')::uuid);
   CREATE POLICY test_tenant ON status_autopilot_projects USING(tenant_id=current_setting('aeon.tenant_id')::uuid);`)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := appPool.Exec(context.Background(), `DROP TABLE status_autopilot_projects; DROP TABLE status_autopilot_settings`); err != nil {
				t.Error(err)
			}
		})
	}
	project := kindBySlug(t, p, "project")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Live limits"}`)
	defaults := defaultStatusHelp()
	defaults.Autopilot.Rules["accept"] = statusRule{Enabled: true, Days: 45}
	defaults.Autopilot.Rules["new"] = statusRule{Enabled: false, Days: 9}
	raw, _ := json.Marshal(defaults.Autopilot.Rules)
	write := func(enabled bool, mode string) {
		t.Helper()
		err := db.InTenant(tenant.WithPrincipal(t.Context(), p), appPool, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO status_autopilot_settings(tenant_id,enabled,rules) VALUES($1,$2,$3) ON CONFLICT(tenant_id) DO UPDATE SET enabled=excluded.enabled,rules=excluded.rules`, p.TenantID, enabled, raw)
			if err != nil {
				return err
			}
			_, err = tx.Exec(t.Context(), `INSERT INTO status_autopilot_projects(tenant_id,project_id,mode) VALUES($1,$2,$3) ON CONFLICT(tenant_id,project_id) DO UPDATE SET mode=excluded.mode`, p.TenantID, root.ID, mode)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		enabled   bool
		mode      string
		effective bool
	}{{true, "inherit", true}, {true, "off", false}, {false, "inherit", false}, {false, "on", true}} {
		write(row.enabled, row.mode)
		status, body := call(t, &p, "GET", "/api/status/help?project_id="+root.ID, "")
		help := decode[statusHelp](t, status, body, 200)
		if help.LimitsSource != "workspace" || help.Autopilot.EffectiveEnabled != row.effective || help.Autopilot.ProjectMode != row.mode || help.Autopilot.Rules["accept"].Days != 45 || help.Autopilot.Rules["new"].Enabled {
			t.Fatalf("live help: %s", body)
		}
		if strings.Contains(help.Definitions[8].SetBy, "45-day") != row.effective {
			t.Fatalf("accepted by: %s", body)
		}
	}
	foreign := addPrincipal(t, "foreign-status-help")
	status, body := call(t, &foreign, "GET", "/api/status/help", "")
	help := decode[statusHelp](t, status, body, 200)
	if help.Autopilot.Rules["accept"].Days != 30 {
		t.Fatalf("foreign workspace limits exposed: %s", body)
	}
}

func TestStatusHelpHTTP(t *testing.T) {
	p := newPrincipal(t, "status-help")
	status, body := call(t, &p, http.MethodGet, "/api/status/help", "")
	help := decode[statusHelp](t, status, body, 200)
	var installed bool
	if err := appPool.QueryRow(t.Context(), `SELECT to_regclass('status_autopilot_settings') IS NOT NULL`).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	wantSource := "defaults"
	if installed {
		wantSource = "workspace"
	}
	if len(help.Definitions) != 11 || help.LimitsSource != wantSource || help.Autopilot.Rules["accept"].Days != 30 {
		t.Fatalf("help: %s", body)
	}
	status, _ = call(t, nil, http.MethodGet, "/api/status/help", "")
	if status != 401 {
		t.Fatalf("anonymous status %d", status)
	}
	status, _ = call(t, &p, http.MethodGet, "/api/status/help?project_id=broken", "")
	if status != 400 {
		t.Fatalf("invalid project status %d", status)
	}
	project := kindBySlug(t, p, "project")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Live project"}`)
	status, body = call(t, &p, http.MethodGet, "/api/status/help?project_id="+root.ID, "")
	help = decode[statusHelp](t, status, body, 200)
	if help.ProjectID != root.ID || help.ProjectName != root.Title {
		t.Fatalf("project help: %s", body)
	}
	other := addPrincipal(t, "status-other")
	status, _ = call(t, &other, http.MethodGet, "/api/status/help?project_id="+root.ID, "")
	if status != 404 {
		t.Fatalf("foreign project status %d", status)
	}
}
