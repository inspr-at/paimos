// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestAppearanceMigrationPreservesPhysicalOwnersGeometryAndBehaviour(t *testing.T) {
	ctx := t.Context()
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	people := map[string]tenant.Principal{}
	var originalValues Values
	var otherTenant string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1235_agent_appearance_themes.sql" {
			return nil
		}
		var tid string
		if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('appearance','Appearance') RETURNING id::text`).Scan(&tid); err != nil {
			return err
		}
		if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('appearance-other','Other') RETURNING id::text`).Scan(&otherTenant); err != nil {
			return err
		}
		return db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			for _, name := range []string{"alice", "alias", "palette", "behaviour", "invalid", "chosen", "agent"} {
				p := tenant.Principal{TenantID: tid, Kind: tenant.Person}
				if name == "agent" {
					p.Kind = tenant.Agent
					p.Scopes = []string{"profile.read"}
				}
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tid, p.Kind, name).Scan(&p.ID); err != nil {
					return err
				}
				people[name] = p
			}
			if _, err := tx.Exec(ctx, `UPDATE principals SET linked_to=$2 WHERE tenant_id=$1 AND id=$3`, tid, people["alice"].ID, people["alias"].ID); err != nil {
				return err
			}
			originalValues = Porcelain()
			originalValues.Primary.Light = "#315a82"
			raw, _ := json.Marshal(originalValues)
			if _, err := tx.Exec(ctx, `UPDATE themes SET config=$2 WHERE tenant_id=$1 AND scope='default'`, tid, raw); err != nil {
				return err
			}
			prefs := map[string]map[string]string{
				"alice":     {"agent-indicator": `{"style":"playful","ring":"off","hovering":true,"size":44.5}`, "agent-state": `{"palette":"colour-blind","dimInactive":false,"inactiveOpacity":71,"yellowMinutes":7,"redMinutes":19}`},
				"alias":     {"agent-indicator": `{"style":"orbit","hovering":true}`, "agent-state": `{"palette":"tritan"}`},
				"palette":   {"agent-state": `{"palette":"protan","yellowMinutes":8}`},
				"behaviour": {"agent-state": `{"yellowMinutes":7,"redMinutes":15}`},
				"invalid":   {"agent-indicator": `{"style":"unknown","ring":"wrong","hovering":"true","size":200}`, "agent-state": `{"palette":"wrong","inactiveOpacity":-1}`},
				"chosen":    {"agent-indicator": `{"style":"sprite"}`},
				"agent":     {"agent-indicator": `{"style":"robot-5"}`},
			}
			for name, keys := range prefs {
				for key, value := range keys {
					if _, err := tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,$3,$4::jsonb)`, tid, people[name].ID, key, value); err != nil {
						return err
					}
				}
			}
			// A person's explicit default already chosen through the theme API
			// must survive, even when they also have legacy appearance rows.
			_, err := tx.Exec(ctx, `INSERT INTO theme_selections(tenant_id,principal_id,theme_id,revision) VALUES($1,$2,NULL,1)`, tid, people["chosen"].ID)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range people {
		dbtest.BindRole(t, d, p.TenantID, p.ID, "member")
	}
	s := Store{d.App}
	for name, want := range map[string]Agents{
		"alice":   {Avatar: "robot-5", Ring: ptr("off"), Hover: true, Size: ptr(45), Palette: "deutan", DimInactive: ptr(false), InactiveOpacity: ptr(71)},
		"palette": {Avatar: "robot-1", Palette: "protan", DimInactive: ptr(true), InactiveOpacity: ptr(55)},
		"invalid": {Avatar: "robot-1", Size: ptr(100), Palette: "standard", DimInactive: ptr(true), InactiveOpacity: ptr(40)},
	} {
		p := people[name]
		active, err := s.Active(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if active.Theme.Scope != "personal" || active.Theme.OwnerPrincipalID == nil || *active.Theme.OwnerPrincipalID != p.ID || !same(active.Theme.Values.Agents, want) || active.SelectedThemeID == nil || *active.SelectedThemeID != active.Theme.ID || active.Revision == 0 {
			t.Fatalf("%s: incorrect migrated theme %+v", name, active)
		}
		if !same(active.Theme.Values.Primary, originalValues.Primary) {
			t.Fatalf("%s did not copy workspace colours", name)
		}
		mux := http.NewServeMux()
		New(d.App).Mount(mux)
		w := call(t, mux, p, "GET", "/api/me/theme", "")
		expect(t, w, 200)
		var fromAPI Active
		if err := json.Unmarshal(w.Body.Bytes(), &fromAPI); err != nil {
			t.Fatal(err)
		}
		if !same(fromAPI.Theme.Values, active.Theme.Values) {
			t.Fatal("API does not serve migrated appearance")
		}
	}
	for _, name := range []string{"behaviour", "chosen", "agent"} {
		active, err := s.Active(ctx, people[name])
		if err != nil {
			t.Fatal(err)
		}
		if active.Theme.Scope != "default" || !same(active.Theme.Values, originalValues) {
			t.Fatalf("%s unexpectedly migrated", name)
		}
	}
	alice := people["alice"]
	alias := people["alias"]
	// While linked, the canonical saved choice wins. Unlink reveals the alias's
	// physically owned theme, without moving owners or rewriting its audit.
	linked, err := s.Active(ctx, alias)
	if err != nil {
		t.Fatal(err)
	}
	if *linked.Theme.OwnerPrincipalID != alice.ID {
		t.Fatal("canonical choice did not win")
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE principals SET linked_to=NULL WHERE tenant_id=$1 AND id=$2`, alias.TenantID, alias.ID); err != nil {
		t.Fatal(err)
	}
	separate, err := s.Active(ctx, alias)
	if err != nil {
		t.Fatal(err)
	}
	if *separate.Theme.OwnerPrincipalID != alias.ID || separate.Theme.Values.Agents.Avatar != "orbit" || separate.Theme.Values.Agents.Size != nil || separate.Theme.Values.Agents.Ring != nil || separate.Theme.Values.Agents.Palette != "tritan" {
		t.Fatalf("alias appearance lost: %+v", separate)
	}
	if _, err := s.Get(ctx, alice, separate.Theme.ID); err != pgx.ErrNoRows {
		t.Fatalf("unlinked person sees private theme: %v", err)
	}
	var n, events int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM themes WHERE scope='personal'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("migrated %d themes, want 4", n)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE metadata->>'migration'='AEON-643' AND metadata->>'audience_principal_id' IS NOT NULL`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 8 {
		t.Fatalf("private migration event count %d", events)
	}
	var legacy []byte
	if err := d.Admin.QueryRow(ctx, `SELECT value FROM user_preferences WHERE tenant_id=$1 AND principal_id=$2 AND key='agent-state'`, alice.TenantID, alice.ID).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(legacy), `"yellowMinutes": 7`) || !strings.Contains(string(legacy), `"redMinutes": 19`) || !strings.Contains(string(legacy), `"palette": "colour-blind"`) {
		t.Fatalf("legacy behaviour/rollback data changed: %s", legacy)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM themes WHERE tenant_id=$1 AND scope='personal'`, otherTenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("appearance crossed tenants")
	}
	var enabled string
	if err := d.Admin.QueryRow(ctx, `SELECT tgenabled::text FROM pg_trigger WHERE tgname='themes_guard'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != "O" {
		t.Fatal("normal owner guard was not restored")
	}
	if err := db.MigrateWithHook(ctx, d.App, func(name string) error { return fmt.Errorf("replayed %s", name) }); err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](value T) *T { return &value }

func TestAgentAppearanceAPIRoundTripAndBounds(t *testing.T) {
	f := setup(t)
	mux := http.NewServeMux()
	New(f.d.App).Mount(mux)
	theme := mustTheme(t, f.s, f.member, "Appearance", "personal")
	for _, avatar := range []string{"pulse", "robot-1", "robot-2", "robot-3", "robot-4", "robot-5", "orbit", "quill", "sprite"} {
		values := theme.Values
		values.Agents = Agents{Avatar: avatar, Ring: ptr("still"), Hover: true, Size: ptr(67), Palette: "tritan", DimInactive: ptr(false), InactiveOpacity: ptr(73)}
		raw, _ := json.Marshal(UpdateInput{Revision: theme.Revision, Values: &values})
		w := call(t, mux, f.member, "PATCH", "/api/themes/"+theme.ID, string(raw))
		expect(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &theme); err != nil {
			t.Fatal(err)
		}
		if !same(theme.Values, values) {
			t.Fatal("appearance did not round trip")
		}
	}
	values := theme.Values
	values.Agents.InactiveOpacity = ptr(81)
	raw, _ := json.Marshal(UpdateInput{Revision: theme.Revision, Values: &values})
	expect(t, call(t, mux, f.member, "PATCH", "/api/themes/"+theme.ID, string(raw)), 400)
	values.Agents.InactiveOpacity = ptr(70)
	raw, _ = json.Marshal(UpdateInput{Revision: theme.Revision, Values: &values})
	expect(t, call(t, mux, f.other, "PATCH", "/api/themes/"+theme.ID, string(raw)), 404)
	for _, field := range []string{"dim_inactive", "inactive_opacity"} {
		raw, _ := json.Marshal(UpdateInput{Revision: theme.Revision, Values: &values})
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		payload["values"].(map[string]any)["agents"].(map[string]any)[field] = nil
		raw, _ = json.Marshal(payload)
		expect(t, call(t, mux, f.member, "PATCH", "/api/themes/"+theme.ID, string(raw)), 400)
	}
	// Null native geometry and the original contract still work.
	values.Agents = Porcelain().Agents
	raw, _ = json.Marshal(UpdateInput{Revision: theme.Revision, Values: &values})
	expect(t, call(t, mux, f.member, "PATCH", "/api/themes/"+theme.ID, string(raw)), 200)
}
