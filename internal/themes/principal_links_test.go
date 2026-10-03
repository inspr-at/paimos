// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/inspr-at/paimos/internal/tenant"
)

func linkPeople(t *testing.T, f fixture, from, to tenant.Principal) {
	t.Helper()
	out, err := principallink.New(f.d.App).Link(t.Context(), "themes", from.ID, to.ID)
	if err != nil || !out.Changed {
		t.Fatalf("link: %+v %v", out, err)
	}
}

func unlinkPerson(t *testing.T, f fixture, p tenant.Principal) {
	t.Helper()
	out, err := principallink.New(f.d.App).Unlink(t.Context(), "themes", p.ID)
	if err != nil || !out.Changed {
		t.Fatalf("unlink: %+v %v", out, err)
	}
	// The fixture has no legacy/import role to restore on unlink.
	dbtest.BindRole(t, f.d, p.TenantID, p.ID, "member")
}

func assertActive(t *testing.T, f fixture, p tenant.Principal, themeID string, revision int64) {
	t.Helper()
	a, err := f.s.Active(t.Context(), p)
	if err != nil || a.Theme.ID != themeID || a.Revision != revision {
		t.Fatalf("active for %s: %+v %v; want theme %s revision %d", p.ID, a, err, themeID, revision)
	}
}

func assertPrivate(t *testing.T, f fixture, p tenant.Principal, theme Theme, audienceID string) {
	t.Helper()
	if _, err := f.s.Get(t.Context(), p, theme.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("private theme visible to %s: %v", p.ID, err)
	}
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), p), f.d.App, p.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type LIKE 'theme.%' AND metadata->>'audience_principal_id'=$1`, audienceID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatalf("private audit visible to %s: %d", p.ID, n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedThemeOwnershipAuditAndUndo(t *testing.T) {
	f := setup(t)
	old := mustTheme(t, f.s, f.member, "Before linking", "personal")
	created := lastEvent(t, f, "theme.created")
	untouched := mustTheme(t, f.s, f.member, "Undo through canonical identity", "personal")
	untouchedCreated := lastEvent(t, f, "theme.created")
	linkPeople(t, f, f.member, f.other)
	for _, p := range []tenant.Principal{f.member, f.other} {
		got, err := f.s.Get(t.Context(), p, old.ID)
		if err != nil || !same(got, old) {
			t.Fatalf("linked read: %+v %v", got, err)
		}
	}
	name := "Edited through canonical person"
	updated, err := f.s.Update(t.Context(), f.other, old.ID, UpdateInput{Revision: 1, Name: &name})
	if err != nil || updated.Revision != 2 || updated.OwnerPrincipalID == nil || *updated.OwnerPrincipalID != f.member.ID {
		t.Fatalf("linked edit changed ownership or failed: %+v %v", updated, err)
	}
	undo(t, f, f.member, lastEvent(t, f, "theme.updated"), 201)
	if err := f.s.Delete(t.Context(), f.other, old.ID, 3); err != nil {
		t.Fatal(err)
	}
	undo(t, f, f.member, lastEvent(t, f, "theme.deleted"), 201)
	assertPrivate(t, f, f.owner, old, f.member.ID)
	assertPrivate(t, f, f.agent, old, f.member.ID)
	creatorAgent := f.agent
	creatorAgent.KeyCreatorID = f.other.ID
	assertPrivate(t, f, creatorAgent, old, f.member.ID)
	agentChoice, err := f.s.Active(t.Context(), creatorAgent)
	if err != nil || agentChoice.Theme.Scope != "default" || agentChoice.Revision != 0 {
		t.Fatalf("agent inherited linked creator's choice: %+v %v", agentChoice, err)
	}
	newTheme := mustTheme(t, f.s, f.member, "Created while linked", "personal")
	if newTheme.OwnerPrincipalID == nil || *newTheme.OwnerPrincipalID != f.other.ID {
		t.Fatalf("new theme did not use canonical owner: %+v", newTheme)
	}
	// A canonical member (without events.undo_other) can undo the alias's creation.
	undo(t, f, f.other, untouchedCreated, 201)
	if _, err := f.s.Get(t.Context(), f.other, untouched.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("linked creation undo did not delete the theme: %v", err)
	}
	// The original creation is stale after edits; it must fail for CAS, not authority.
	undo(t, f, f.other, created, 409)
	unlinkPerson(t, f, f.member)
	got, err := f.s.Get(t.Context(), f.member, old.ID)
	if err != nil || got.OwnerPrincipalID == nil || *got.OwnerPrincipalID != f.member.ID || got.Revision != 5 {
		t.Fatalf("unlink stranded old theme: %+v %v", got, err)
	}
	assertPrivate(t, f, f.other, old, f.member.ID)
	assertPrivate(t, f, f.member, newTheme, f.other.ID)
	if err := f.s.Delete(t.Context(), f.member, old.ID, got.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedSelectionsUseDeterministicAliasPriorityAndExplicitDefault(t *testing.T) {
	f := setup(t)
	a := mustTheme(t, f.s, f.member, "First alias", "personal")
	b := mustTheme(t, f.s, f.other, "Second alias", "personal")
	for _, c := range []struct {
		person tenant.Principal
		theme  Theme
	}{{f.member, a}, {f.other, b}} {
		if _, err := f.s.Select(t.Context(), c.person, SelectionInput{ThemeID: &c.theme.ID}); err != nil {
			t.Fatal(err)
		}
	}
	linkPeople(t, f, f.member, f.owner)
	linkPeople(t, f, f.other, f.owner)
	winner := a
	if f.other.ID < f.member.ID {
		winner = b
	}
	for _, p := range []tenant.Principal{f.owner, f.member, f.other} {
		assertActive(t, f, p, winner.ID, 1)
	}
	// Seed an explicit canonical choice to model the collision without editing
	// either alias row. A saved null means default, never "no saved choice".
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO theme_selections(tenant_id,principal_id,theme_id,revision) VALUES($1,$2,NULL,7)`, f.owner.TenantID, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	def, err := f.s.Active(t.Context(), f.owner)
	if err != nil || def.Theme.Scope != "default" || def.Revision != 7 {
		t.Fatalf("explicit canonical default lost to aliases: %+v %v", def, err)
	}
	assertActive(t, f, f.member, def.Theme.ID, 7)
	assertActive(t, f, f.other, def.Theme.ID, 7)
	unlinkPerson(t, f, f.member)
	unlinkPerson(t, f, f.other)
	assertActive(t, f, f.member, a.ID, 1)
	assertActive(t, f, f.other, b.ID, 1)
	assertActive(t, f, f.owner, def.Theme.ID, 7)
}

func TestLinkedSelectionInheritanceCollisionAndUndo(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "inherit", true: "canonical wins"}[collision], func(t *testing.T) {
			f := setup(t)
			def, err := f.s.Active(t.Context(), f.member)
			if err != nil {
				t.Fatal(err)
			}
			old := mustTheme(t, f.s, f.member, "Alias choice", "personal")
			if _, err := f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: &old.ID}); err != nil {
				t.Fatal(err)
			}
			selected := lastEvent(t, f, "theme.selected")
			var canonicalTheme Theme
			if collision {
				canonicalTheme = mustTheme(t, f.s, f.other, "Canonical choice", "personal")
				if _, err := f.s.Select(t.Context(), f.other, SelectionInput{ThemeID: &canonicalTheme.ID}); err != nil {
					t.Fatal(err)
				}
			}
			linkPeople(t, f, f.member, f.other)
			if collision {
				assertActive(t, f, f.member, canonicalTheme.ID, 1)
				assertActive(t, f, f.other, canonicalTheme.ID, 1)
				undo(t, f, f.other, selected, 409) // Never overwrite the winning row.
			} else {
				assertActive(t, f, f.member, old.ID, 1)
				assertActive(t, f, f.other, old.ID, 1)
				undo(t, f, f.other, selected, 201)
				assertActive(t, f, f.other, def.Theme.ID, 2)
				if _, err := f.s.Select(t.Context(), f.other, SelectionInput{ThemeID: &old.ID, Revision: 1}); !errors.Is(err, ErrConflict) {
					t.Fatalf("linked selection accepted stale revision: %v", err)
				}
				if _, err := f.s.Select(t.Context(), f.other, SelectionInput{ThemeID: &old.ID, Revision: 2}); err != nil {
					t.Fatal(err)
				}
				assertActive(t, f, f.member, old.ID, 3)
			}
			assertPrivate(t, f, f.owner, old, f.member.ID)
			assertPrivate(t, f, f.agent, old, f.member.ID)
			unlinkPerson(t, f, f.member)
			if collision {
				assertActive(t, f, f.member, old.ID, 1)
				assertActive(t, f, f.other, canonicalTheme.ID, 1)
			} else {
				assertActive(t, f, f.member, old.ID, 3)
				assertActive(t, f, f.other, def.Theme.ID, 0)
			}
		})
	}
}

func TestUnlinkHidesASelectionFromTheFormerLinkedPerson(t *testing.T) {
	f := setup(t)
	old := mustTheme(t, f.s, f.member, "Original", "personal")
	if _, err := f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: &old.ID}); err != nil {
		t.Fatal(err)
	}
	linkPeople(t, f, f.member, f.other)
	canonicalTheme := mustTheme(t, f.s, f.other, "Canonical private", "personal")
	if _, err := f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: &canonicalTheme.ID, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	selected := lastEvent(t, f, "theme.selected")
	unlinkPerson(t, f, f.member)
	a, err := f.s.Active(t.Context(), f.member)
	if err != nil || a.Theme.Scope != "default" || a.SelectedThemeID != nil || a.FallbackNotice != nil || a.Revision != 2 {
		t.Fatalf("unlink exposed private choice or failed fallback: %+v %v", a, err)
	}
	assertPrivate(t, f, f.member, canonicalTheme, f.other.ID)
	undo(t, f, f.member, selected, 201) // The alias's row returns to its own theme.
	assertActive(t, f, f.member, old.ID, 3)
	// Another link/unlink leaves a before-snapshot that is now private. Undo
	// must reject that restore, rather than report success or leak the theme.
	linkPeople(t, f, f.member, f.other)
	if _, err := f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: &canonicalTheme.ID, Revision: 3}); err != nil {
		t.Fatal(err)
	}
	unlinkPerson(t, f, f.member)
	if _, err := f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: nil, Revision: 4}); err != nil {
		t.Fatal(err)
	}
	undo(t, f, f.member, lastEvent(t, f, "theme.selected"), 409)
	current, err := f.s.Active(t.Context(), f.member)
	if err != nil || current.Theme.Scope != "default" || current.Revision != 5 {
		t.Fatalf("private restore committed: %+v %v", current, err)
	}
}

func TestThemeWriteRechecksLinkAfterAuthorityFence(t *testing.T) {
	f := setup(t)
	old := mustTheme(t, f.s, f.member, "Original", "personal")
	linkPeople(t, f, f.member, f.other)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.Contains(sql, "pg_advisory_xact_lock") })
	tx, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.member.TenantID); err != nil {
		t.Fatal(err)
	}
	result, done := make(chan error, 1), make(chan struct{})
	name := "Must not commit after unlink"
	go func() {
		_, err := (Store{pool}).Update(ctx, f.other, old.ID, UpdateInput{Revision: 1, Name: &name})
		result <- err
		close(done)
	}()
	barrier.Wait(t, ctx)
	barrier.Release()
	if dbtest.BlockedOrDone(t, ctx, f.d.Admin, tx.Conn().PgConn().PID(), done) == "" {
		t.Fatal("write did not wait for link authority fence")
	}
	if _, err := tx.Exec(ctx, `UPDATE principals SET linked_to=NULL WHERE tenant_id=$1 AND id=$2`, f.member.TenantID, f.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, result); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("write retained former linked person's authority: %v", err)
	}
	var nameNow string
	if err := f.d.Admin.QueryRow(ctx, `SELECT name FROM themes WHERE tenant_id=$1 AND id=$2`, f.member.TenantID, old.ID).Scan(&nameNow); err != nil || nameNow != old.Name {
		t.Fatalf("post-unlink write committed: %s %v", nameNow, err)
	}
}
