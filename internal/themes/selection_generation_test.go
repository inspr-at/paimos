// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func activeHTTP(t *testing.T, h http.Handler, p tenant.Principal) Active {
	t.Helper()
	w := call(t, h, p, "GET", "/api/me/theme", "")
	expect(t, w, 200)
	var a Active
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSelectionGenerationMigrationPreservesAuditAndLegacyWrites(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	f := fixture{d: d, s: Store{d.App}}
	p := tenant.Principal{Kind: tenant.Person}
	var oldEvent events.Event
	seeded := false
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1208_theme_selection_generation.sql" {
			return nil
		}
		seeded = true
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('themes','Themes') RETURNING id::text`).Scan(&p.TenantID); err != nil {
			return err
		}
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Existing person') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		dbtest.BindRole(t, d, p.TenantID, p.ID, "member")
		f.owner, f.member = p, p
		return db.InTenant(tenant.WithPrincipal(t.Context(), p), d.App, p.TenantID, func(tx pgx.Tx) error {
			choice := Selection{PrincipalID: p.ID, Revision: 7}
			if err := saveSelection(t.Context(), tx, p.TenantID, choice); err != nil {
				return err
			}
			var err error
			oldEvent, err = events.Append(t.Context(), tx, p, events.Change{Type: "theme.selected",
				Before: Selection{PrincipalID: p.ID, Revision: 6}, After: choice, Metadata: audience(&p.ID)})
			return err
		})
	})
	if err != nil || !seeded {
		t.Fatalf("migration fixture did not run: %v", err)
	}
	current, err := f.s.Active(t.Context(), p)
	if err != nil || current.Theme.Scope != "default" {
		t.Fatalf("migrated choice: %+v %v", current, err)
	}
	assertActive(t, f, p, current.Theme.ID, 7)
	storedEvent := lastEvent(t, f, "theme.selected")
	if current.Revision <= 7 || storedEvent.ID != oldEvent.ID || !same(storedEvent.Before, oldEvent.Before) || !same(storedEvent.After, oldEvent.After) {
		t.Fatalf("backfill reused a prior revision or rewrote audit: %+v", current)
	}
	if _, err := f.s.Select(t.Context(), p, SelectionInput{Revision: 7}); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted pre-migration cached revision: %v", err)
	}
	undo(t, f, p, oldEvent, 201)
	restored := assertActive(t, f, p, current.Theme.ID, 8)
	if restored.Revision == current.Revision {
		t.Fatal("undo restored an old generation")
	}
	// An earlier binary knows only the original columns. Its writes still work,
	// but the trigger must invalidate the newer client's cached generation.
	if _, err := d.Admin.Exec(t.Context(), `UPDATE theme_selections SET revision=revision+1 WHERE tenant_id=$1 AND principal_id=$2`, p.TenantID, p.ID); err != nil {
		t.Fatal(err)
	}
	legacyWrite := assertActive(t, f, p, current.Theme.ID, 9)
	if legacyWrite.Revision == restored.Revision {
		t.Fatal("previous-binary writer reused a generation")
	}
	if _, err := f.s.Select(t.Context(), p, SelectionInput{Revision: restored.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted generation from before legacy write: %v", err)
	}
	var after events.Event
	if err := d.Admin.QueryRow(t.Context(), `SELECT id,actor_principal_id::text,type,before,after,at,undo_of FROM events WHERE tenant_id=$1 AND id=$2`, p.TenantID, oldEvent.ID).
		Scan(&after.ID, &after.ActorPrincipalID, &after.Type, &after.Before, &after.After, &after.At, &after.UndoOf); err != nil || !same(after, oldEvent) {
		t.Fatalf("historical event changed: %+v %v", after, err)
	}
}

func TestSelectionCASRejectsChangedWinningIdentity(t *testing.T) {
	for _, topology := range []string{"link", "unlink"} {
		t.Run(topology, func(t *testing.T) {
			f := setup(t)
			a := mustTheme(t, f.s, f.owner, "Alias choice", "workspace")
			b := mustTheme(t, f.s, f.owner, "Canonical choice", "workspace")
			for _, c := range []struct {
				person tenant.Principal
				theme  Theme
			}{{f.member, a}, {f.other, b}} {
				if _, err := f.s.Select(t.Context(), c.person, SelectionInput{ThemeID: &c.theme.ID}); err != nil {
					t.Fatal(err)
				}
				var saved Selection
				if err := json.Unmarshal(lastEvent(t, f, "theme.selected").After, &saved); err != nil || saved.Revision != 1 {
					t.Fatalf("fixture must have equal physical revisions: %+v %v", saved, err)
				}
			}
			mux := http.NewServeMux()
			New(f.d.App).Mount(mux)
			if topology == "unlink" {
				linkPeople(t, f, f.member, f.other)
			}
			stale := activeHTTP(t, mux, f.member)
			wantBefore, wantAfter := a.ID, b.ID
			if topology == "link" {
				linkPeople(t, f, f.member, f.other)
			} else {
				wantBefore, wantAfter = b.ID, a.ID
				unlinkPerson(t, f, f.member)
			}
			current := activeHTTP(t, mux, f.member)
			if stale.Theme.ID != wantBefore || current.Theme.ID != wantAfter {
				t.Fatalf("winning identity did not switch: %+v -> %+v", stale, current)
			}
			beforeEvents := eventCount(t, f)
			expect(t, call(t, mux, f.member, "PUT", "/api/me/theme", fmt.Sprintf(`{"theme_id":null,"revision":%d}`, stale.Revision)), 409)
			if got := activeHTTP(t, mux, f.member); !same(got, current) || eventCount(t, f) != beforeEvents {
				t.Fatalf("stale choice changed state or audit: %+v -> %+v", current, got)
			}
			// A client can recover using the generation returned by its new read.
			expect(t, call(t, mux, f.member, "PUT", "/api/me/theme", fmt.Sprintf(`{"theme_id":null,"revision":%d}`, current.Revision)), 200)
			got := activeHTTP(t, mux, f.member)
			if got.Theme.Scope != "default" || got.SelectedThemeID != nil || got.Revision == current.Revision || eventCount(t, f) != beforeEvents+1 {
				t.Fatalf("fresh choice was not committed exactly once: %+v", got)
			}
		})
	}
}

func TestInitialExplicitDefaultPersistsAndWinsAfterLink(t *testing.T) {
	f := setup(t)
	mux := http.NewServeMux()
	New(f.d.App).Mount(mux)
	initial := activeHTTP(t, mux, f.other)
	if initial.Revision != 0 || initial.Theme.Scope != "default" {
		t.Fatalf("unexpected initial choice: %+v", initial)
	}
	beforeEvents := eventCount(t, f)
	expect(t, call(t, mux, f.other, "PUT", "/api/me/theme", `{"theme_id":null,"revision":0}`), 200)
	explicit := activeHTTP(t, mux, f.other)
	if explicit.Revision == 0 || explicit.SelectedThemeID != nil || explicit.Theme.ID != initial.Theme.ID || eventCount(t, f) != beforeEvents+1 {
		t.Fatalf("first explicit default was not saved and audited: %+v", explicit)
	}
	var saved Selection
	if err := json.Unmarshal(lastEvent(t, f, "theme.selected").After, &saved); err != nil || saved.PrincipalID != f.other.ID || saved.ThemeID != nil || saved.Revision != 1 {
		t.Fatalf("explicit default audit: %+v %v", saved, err)
	}
	// Repeating the explicit choice is a true no-op, including its generation.
	expect(t, call(t, mux, f.other, "PUT", "/api/me/theme", fmt.Sprintf(`{"theme_id":null,"revision":%d}`, explicit.Revision)), 200)
	if got := activeHTTP(t, mux, f.other); !same(got, explicit) || eventCount(t, f) != beforeEvents+1 {
		t.Fatalf("repeated default changed state or audit: %+v", got)
	}
	alias := mustTheme(t, f.s, f.member, "Alias preference", "personal")
	choice, err := f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: &alias.ID})
	if err != nil {
		t.Fatal(err)
	}
	linkPeople(t, f, f.member, f.other)
	for _, p := range []tenant.Principal{f.member, f.other} {
		if got := activeHTTP(t, mux, p); !same(got, explicit) {
			t.Fatalf("explicit canonical default lost after link: %+v", got)
		}
	}
	unlinkPerson(t, f, f.member)
	if got := activeHTTP(t, mux, f.member); !same(got, choice) {
		t.Fatalf("unlink lost the alias's original preference: %+v", got)
	}
	if got := activeHTTP(t, mux, f.other); !same(got, explicit) {
		t.Fatalf("unlink lost explicit canonical default: %+v", got)
	}
}
