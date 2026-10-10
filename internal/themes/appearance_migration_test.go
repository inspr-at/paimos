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
	"github.com/inspr-at/paimos/internal/principallink"
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

// Seed explicit linked choices before the migration, including a canonical
// absence left by Undo. Every identity must retain its effective choice/CAS.
func TestAppearanceMigrationPreservesInheritedExplicitChoices(t *testing.T) {
	for _, choice := range []string{"theme", "default"} {
		for _, canonicalRow := range []string{"missing", "unsaved"} {
			t.Run(choice+"/"+canonicalRow, func(t *testing.T) {
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
				s := Store{d.App}
				var people []tenant.Principal
				var before Active
				var explicitTheme *string
				seeded := false
				err = db.MigrateWithHook(ctx, d.App, func(name string) error {
					if name != "1235_agent_appearance_themes.sql" {
						return nil
					}
					seeded = true
					var tid string
					if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('inherited-appearance','Inherited appearance') RETURNING id::text`).Scan(&tid); err != nil {
						return err
					}
					// Force the legacy-only alias to sort ahead of the saved alias, proving
					// that the backfill cannot insert a new winning linked selection either.
					for _, id := range []string{"ffffffff-ffff-4fff-8fff-ffffffffffff", "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "11111111-1111-4111-8111-111111111111"} {
						p := tenant.Principal{TenantID: tid, ID: id, Kind: tenant.Person}
						if _, err := d.Admin.Exec(ctx, `INSERT INTO principals(id,tenant_id,kind,name) VALUES($1,$2,'person','Linked person')`, id, tid); err != nil {
							return err
						}
						dbtest.BindRole(t, d, tid, id, "member")
						people = append(people, p)
					}
					if choice == "theme" {
						theme, err := s.Create(ctx, people[1], CreateInput{Name: "Explicit alias choice", Scope: "personal"})
						if err != nil {
							return err
						}
						explicitTheme = &theme.ID
					}
					if _, err := s.Select(ctx, people[1], SelectionInput{ThemeID: explicitTheme}); err != nil {
						return err
					}
					if canonicalRow == "unsaved" {
						if _, err := d.Admin.Exec(ctx, `INSERT INTO theme_selections(tenant_id,principal_id,theme_id,revision,unsaved_revision) VALUES($1,$2,NULL,7,7)`, tid, people[0].ID); err != nil {
							return err
						}
					}
					for i, p := range people {
						if i > 0 {
							if _, err := d.Admin.Exec(ctx, `UPDATE principals SET linked_to=$3 WHERE tenant_id=$1 AND id=$2`, tid, p.ID, people[0].ID); err != nil {
								return err
							}
						}
						if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
							_, err := tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agent-indicator','{"style":"sprite"}')`, tid, p.ID)
							return err
						}); err != nil {
							return err
						}
					}
					var err error
					before, err = s.Active(ctx, people[0])
					return err
				})
				if err != nil || !seeded {
					t.Fatalf("migration fixture: %v (seeded=%v)", err, seeded)
				}
				if before.SelectedThemeID == nil && choice == "theme" || before.SelectedThemeID != nil && choice == "default" {
					t.Fatalf("wrong pre-migration explicit choice: %+v", before)
				}
				for _, p := range people {
					after, err := s.Active(ctx, p)
					if err != nil {
						t.Fatal(err)
					}
					if !same(after, before) {
						t.Fatalf("%s displaced inherited explicit %s choice: before=%+v after=%+v", canonicalRow, choice, before, after)
					}
				}
				var n int
				if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE metadata->>'migration'='AEON-643'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				// Both legacy-only physical identities are retained without changing
				// the effective explicit choice. Each gets created + selected events.
				if n != 4 {
					t.Fatalf("legacy physical backfill event count %d, want 4", n)
				}
				if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE metadata->>'migration'='AEON-643' AND metadata->>'audience_principal_id'=$1`, people[1].ID).Scan(&n); err != nil || n != 0 {
					t.Fatalf("explicit physical choice received backfill events: %d %v", n, err)
				}
				if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events e JOIN theme_selections s ON s.tenant_id=e.tenant_id AND s.principal_id=(e.after->>'principal_id')::uuid WHERE e.type='theme.selected' AND e.metadata->>'migration'='AEON-643' AND e.after->>'unsaved'='true' AND s.unsaved_revision=s.revision AND e.after->>'theme_id'=s.theme_id::text AND (e.after->>'revision')::bigint=s.revision`).Scan(&n); err != nil || n != 2 {
					t.Fatalf("lower-priority selection audit mismatch: %d %v", n, err)
				}
				// Unlink must keep the physical explicit selection, including default.
				if _, err := d.Admin.Exec(ctx, `UPDATE principals SET linked_to=NULL WHERE tenant_id=$1 AND id=$2`, people[1].TenantID, people[1].ID); err != nil {
					t.Fatal(err)
				}
				after, err := s.Active(ctx, people[1])
				if err != nil || !same(after, before) {
					t.Fatalf("unlink changed explicit choice: %+v %v", after, err)
				}
				for _, p := range []tenant.Principal{people[0], people[2]} {
					if _, err := d.Admin.Exec(ctx, `UPDATE principals SET linked_to=NULL WHERE tenant_id=$1 AND id=$2`, p.TenantID, p.ID); err != nil {
						t.Fatal(err)
					}
					after, err := s.Active(ctx, p)
					if err != nil || after.Theme.OwnerPrincipalID == nil || *after.Theme.OwnerPrincipalID != p.ID || after.Theme.Values.Agents.Avatar != "sprite" {
						t.Fatalf("legacy physical choice lost: %+v %v", after, err)
					}
				}
			})
		}
	}
}

func ptr[T any](value T) *T { return &value }

func TestAppearanceMigrationRetainsLegacyAliasAfterExplicitCanonicalUnlink(t *testing.T) {
	for _, choice := range []string{"theme", "default"} {
		t.Run(choice, func(t *testing.T) {
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
			s := Store{d.App}
			var canonical, alias tenant.Principal
			var before Active
			seeded := false
			err = db.MigrateWithHook(ctx, d.App, func(name string) error {
				if name != "1235_agent_appearance_themes.sql" {
					return nil
				}
				seeded = true
				var tid string
				if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('legacy-alias','Legacy alias') RETURNING id::text`).Scan(&tid); err != nil {
					return err
				}
				for i, p := range []*tenant.Principal{&canonical, &alias} {
					*p = tenant.Principal{TenantID: tid, Kind: tenant.Person}
					if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, tid, fmt.Sprintf("Person %d", i)).Scan(&p.ID); err != nil {
						return err
					}
					dbtest.BindRole(t, d, tid, p.ID, "member")
				}
				var selected *string
				if choice == "theme" {
					theme, err := s.Create(ctx, canonical, CreateInput{Name: "Explicit canonical", Scope: "personal"})
					if err != nil {
						return err
					}
					selected = &theme.ID
				}
				if _, err := s.Select(ctx, canonical, SelectionInput{ThemeID: selected}); err != nil {
					return err
				}
				if _, err := d.Admin.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agent-indicator','{"style":"orbit","ring":"off","hovering":true,"size":67}'),($1,$2,'agent-state','{"palette":"tritan","dimInactive":false,"inactiveOpacity":73,"yellowMinutes":7}')`, tid, alias.ID); err != nil {
					return err
				}
				linked, err := principallink.New(d.App).Link(ctx, "legacy-alias", alias.ID, canonical.ID)
				if err != nil {
					return err
				}
				if !linked.Changed {
					return fmt.Errorf("link fixture did not change")
				}
				before, err = s.Active(ctx, alias)
				return err
			})
			if err != nil || !seeded {
				t.Fatalf("migration fixture: %v (seeded=%v)", err, seeded)
			}
			for _, p := range []tenant.Principal{canonical, alias} {
				after, err := s.Active(ctx, p)
				if err != nil || !same(after, before) {
					t.Fatalf("explicit canonical choice displaced: %+v %v; before=%+v", after, err, before)
				}
			}
			unlinked, err := principallink.New(d.App).Unlink(ctx, "legacy-alias", alias.ID)
			if err != nil || !unlinked.Changed {
				t.Fatalf("unlink: %+v %v", unlinked, err)
			}
			dbtest.BindRole(t, d, alias.TenantID, alias.ID, "member")
			after, err := s.Active(ctx, alias)
			want := Agents{Avatar: "orbit", Ring: ptr("off"), Hover: true, Size: ptr(67), Palette: "tritan", DimInactive: ptr(false), InactiveOpacity: ptr(73)}
			if err != nil || after.Theme.OwnerPrincipalID == nil || *after.Theme.OwnerPrincipalID != alias.ID || !same(after.Theme.Values.Agents, want) || after.SelectedThemeID == nil || *after.SelectedThemeID != after.Theme.ID {
				t.Fatalf("legacy alias appearance lost after unlink: %+v %v", after, err)
			}
			if _, err := s.Get(ctx, canonical, after.Theme.ID); err != pgx.ErrNoRows {
				t.Fatalf("former canonical sees alias private theme: %v", err)
			}
			var unchanged bool
			if err := d.Admin.QueryRow(ctx, `SELECT value='{"palette":"tritan","dimInactive":false,"inactiveOpacity":73,"yellowMinutes":7}'::jsonb FROM user_preferences WHERE tenant_id=$1 AND principal_id=$2 AND key='agent-state'`, alias.TenantID, alias.ID).Scan(&unchanged); err != nil || !unchanged {
				t.Fatalf("legacy preference changed: %v", err)
			}
		})
	}
}

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

// Without explicit choices a linked person saw only the canonical account's
// legacy preferences (sessions resolve to the canonical principal). An alias's
// backfill must not replace that while linked, yet must return after unlink.
func TestAppearanceMigrationKeepsCanonicalAppearanceWhileLinkedAndAliasesAfterUnlink(t *testing.T) {
	const (
		orbit  = `{"style":"orbit","ring":"off","hovering":true,"size":67}`
		sprite = `{"style":"sprite"}`
		robot3 = `{"style":"robot-3","size":44}`
	)
	cases := []struct {
		name      string
		canonical string   // legacy agent-indicator of the canonical account; empty for none
		aliases   []string // legacy agent-indicator of each alias; empty for none
		linked    string   // avatar the linked person sees before and after the migration
	}{
		{name: "alias only", aliases: []string{orbit}, linked: "robot-1"},
		{name: "two aliases", aliases: []string{orbit, sprite}, linked: "robot-1"},
		{name: "canonical and alias", canonical: robot3, aliases: []string{orbit}, linked: "robot-3"},
		{name: "canonical only", canonical: robot3, aliases: []string{""}, linked: "robot-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			s := Store{d.App}
			var canonical tenant.Principal
			aliases := make([]tenant.Principal, len(tc.aliases))
			var before Active
			seeded := false
			err = db.MigrateWithHook(ctx, d.App, func(name string) error {
				if name != "1235_agent_appearance_themes.sql" {
					return nil
				}
				seeded = true
				var tid string
				if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('linked-legacy','Linked legacy') RETURNING id::text`).Scan(&tid); err != nil {
					return err
				}
				people := append([]*tenant.Principal{&canonical}, func() (out []*tenant.Principal) {
					for i := range aliases {
						out = append(out, &aliases[i])
					}
					return
				}()...)
				for i, p := range people {
					*p = tenant.Principal{TenantID: tid, Kind: tenant.Person}
					if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, tid, fmt.Sprintf("Person %d", i)).Scan(&p.ID); err != nil {
						return err
					}
					dbtest.BindRole(t, d, tid, p.ID, "member")
				}
				legacy := append([]string{tc.canonical}, tc.aliases...)
				for i, p := range people {
					if legacy[i] == "" {
						continue
					}
					if _, err := d.Admin.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agent-indicator',$3::jsonb)`, tid, p.ID, legacy[i]); err != nil {
						return err
					}
				}
				for _, alias := range aliases {
					linked, err := principallink.New(d.App).Link(ctx, "linked-legacy", alias.ID, canonical.ID)
					if err != nil {
						return err
					}
					if !linked.Changed {
						return fmt.Errorf("link fixture did not change")
					}
				}
				before, err = s.Active(ctx, canonical)
				return err
			})
			if err != nil || !seeded {
				t.Fatalf("migration fixture: %v (seeded=%v)", err, seeded)
			}
			// Before the migration the theme carried no appearance; the linked
			// person saw the canonical account's legacy preferences.
			if before.SelectedThemeID != nil || before.Theme.ID != before.DefaultThemeID {
				t.Fatalf("fixture already had a choice: %+v", before)
			}
			for _, p := range append([]tenant.Principal{canonical}, aliases...) {
				after, err := s.Active(ctx, p)
				if err != nil || after.Theme.Values.Agents.Avatar != tc.linked {
					t.Fatalf("linked person's appearance changed: %+v %v, want %s", after, err, tc.linked)
				}
				if tc.canonical == "" && (after.Theme.ID != before.Theme.ID || after.SelectedThemeID != nil) {
					t.Fatalf("alias backfill displaced the canonical default: %+v", after)
				}
				if tc.canonical != "" && (after.Theme.OwnerPrincipalID == nil || *after.Theme.OwnerPrincipalID != canonical.ID) {
					t.Fatalf("canonical appearance not owned by the canonical account: %+v", after)
				}
			}
			if tc.canonical == "" {
				// The canonical absence is audited privately and is a valid CAS base.
				var n int
				if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE metadata->>'migration'='AEON-643' AND metadata->>'audience_principal_id'=$1 AND type='theme.selected' AND after->>'theme_id' IS NULL AND after->>'unsaved'='true' AND before->>'revision'='0'`, canonical.ID).Scan(&n); err != nil || n != 1 {
					t.Fatalf("canonical absence audit: %d %v", n, err)
				}
				if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE metadata->>'migration'='AEON-643' AND metadata->>'audience_principal_id'=$1 AND type='theme.created'`, canonical.ID).Scan(&n); err != nil || n != 0 {
					t.Fatalf("canonical without legacy appearance got a theme: %d %v", n, err)
				}
				current, err := s.Active(ctx, canonical)
				if err != nil {
					t.Fatal(err)
				}
				aliasTheme, err := s.Active(ctx, aliases[0])
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Select(ctx, canonical, SelectionInput{ThemeID: &aliasTheme.DefaultThemeID, Revision: current.Revision}); err != nil {
					t.Fatalf("select on the migrated CAS base: %v", err)
				}
			}
			for i, alias := range aliases {
				unlinked, err := principallink.New(d.App).Unlink(ctx, "linked-legacy", alias.ID)
				if err != nil || !unlinked.Changed {
					t.Fatalf("unlink: %+v %v", unlinked, err)
				}
				dbtest.BindRole(t, d, alias.TenantID, alias.ID, "member")
				separate, err := s.Active(ctx, alias)
				if err != nil {
					t.Fatal(err)
				}
				if tc.aliases[i] == "" {
					if separate.Theme.ID != separate.DefaultThemeID {
						t.Fatalf("alias without legacy appearance got a theme: %+v", separate)
					}
					continue
				}
				var want struct{ Style string }
				_ = json.Unmarshal([]byte(tc.aliases[i]), &want)
				if separate.Theme.OwnerPrincipalID == nil || *separate.Theme.OwnerPrincipalID != alias.ID || separate.Theme.Values.Agents.Avatar != want.Style || separate.SelectedThemeID == nil {
					t.Fatalf("alias appearance lost after unlink: %+v", separate)
				}
				// The canonical account keeps what it saw while linked.
				remaining, err := s.Active(ctx, canonical)
				if err != nil || remaining.Theme.Values.Agents.Avatar != tc.linked {
					t.Fatalf("canonical appearance changed by unlink: %+v %v", remaining, err)
				}
			}
		})
	}
}
