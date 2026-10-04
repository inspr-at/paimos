// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"context"
	"errors"
	"testing"
	"time"

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

func assertActive(t *testing.T, f fixture, p tenant.Principal, themeID string, revision int64) Active {
	t.Helper()
	a, err := f.s.Active(t.Context(), p)
	if err != nil || a.Theme.ID != themeID {
		t.Fatalf("active for %s: %+v %v; want theme %s revision %d", p.ID, a, err, themeID, revision)
	}
	// Preserve the physical-revision assertions while checking the public CAS
	// against the exact generation stored for that row, independent of gaps.
	if revision == 0 {
		if a.Revision != 0 {
			t.Fatalf("unexpected saved choice: %+v", a)
		}
	} else {
		var physical int64
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT revision FROM theme_selections WHERE tenant_id=$1 AND generation=$2`, p.TenantID, a.Revision).Scan(&physical); err != nil || physical != revision {
			t.Fatalf("selection generation %d: physical revision %d want %d: %v", a.Revision, physical, revision, err)
		}
	}
	return a
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
	for _, explicitDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "alias priority", true: "explicit canonical default"}[explicitDefault], func(t *testing.T) {
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
			def, err := f.s.Active(t.Context(), f.owner)
			if err != nil {
				t.Fatal(err)
			}
			if explicitDefault {
				def, err = f.s.Select(t.Context(), f.owner, SelectionInput{ThemeID: nil, Revision: def.Revision})
				if err != nil || def.Revision == 0 {
					t.Fatalf("save canonical default through public path: %+v %v", def, err)
				}
			}
			linkPeople(t, f, f.member, f.owner)
			linkPeople(t, f, f.other, f.owner)
			winner := a
			if f.other.ID < f.member.ID {
				winner = b
			}
			if explicitDefault {
				winner = def.Theme
			}
			for _, p := range []tenant.Principal{f.owner, f.member, f.other} {
				assertActive(t, f, p, winner.ID, 1)
			}
			unlinkPerson(t, f, f.member)
			unlinkPerson(t, f, f.other)
			assertActive(t, f, f.member, a.ID, 1)
			assertActive(t, f, f.other, b.ID, 1)
			canonicalRevision := int64(0)
			if explicitDefault {
				canonicalRevision = 1
			}
			assertActive(t, f, f.owner, def.Theme.ID, canonicalRevision)
		})
	}
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
				restored := assertActive(t, f, f.other, def.Theme.ID, 2)
				if _, err := f.s.Select(t.Context(), f.other, SelectionInput{ThemeID: &old.ID, Revision: 1}); !errors.Is(err, ErrConflict) {
					t.Fatalf("linked selection accepted stale revision: %v", err)
				}
				if _, err := f.s.Select(t.Context(), f.other, SelectionInput{ThemeID: &old.ID, Revision: restored.Revision}); err != nil {
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
	choice, err := f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: &canonicalTheme.ID, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	selected := lastEvent(t, f, "theme.selected")
	unlinkPerson(t, f, f.member)
	a, err := f.s.Active(t.Context(), f.member)
	if err != nil || a.Theme.Scope != "default" || a.SelectedThemeID != nil || a.FallbackNotice != nil || a.Revision != choice.Revision {
		t.Fatalf("unlink exposed private choice or failed fallback: %+v %v", a, err)
	}
	assertPrivate(t, f, f.member, canonicalTheme, f.other.ID)
	undo(t, f, f.member, selected, 201) // The alias's row returns to its own theme.
	restored := assertActive(t, f, f.member, old.ID, 3)
	// Another link/unlink leaves a before-snapshot that is now private. Undo
	// must reject that restore, rather than report success or leak the theme.
	linkPeople(t, f, f.member, f.other)
	choice, err = f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: &canonicalTheme.ID, Revision: restored.Revision})
	if err != nil {
		t.Fatal(err)
	}
	unlinkPerson(t, f, f.member)
	choice, err = f.s.Select(t.Context(), f.member, SelectionInput{ThemeID: nil, Revision: choice.Revision})
	if err != nil {
		t.Fatal(err)
	}
	undo(t, f, f.member, lastEvent(t, f, "theme.selected"), 409)
	current, err := f.s.Active(t.Context(), f.member)
	if err != nil || current.Theme.Scope != "default" || current.Revision != choice.Revision {
		t.Fatalf("private restore committed: %+v %v", current, err)
	}
	assertActive(t, f, f.member, current.Theme.ID, 5)
}

func TestThemeWriteRechecksLinkAfterAuthorityFence(t *testing.T) {
	f := setup(t)
	old := mustTheme(t, f.s, f.member, "Original", "personal")
	linkPeople(t, f, f.member, f.other)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := f.d.App
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

func TestAliasThemeWriteDropsCachedCanonicalAfterUnlink(t *testing.T) {
	f := setup(t)
	private := mustTheme(t, f.s, f.other, "Former canonical person's private theme", "personal")
	linkPeople(t, f, f.member, f.other)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := f.d.App
	tx, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.member.TenantID); err != nil {
		t.Fatal(err)
	}
	result, done := make(chan error, 1), make(chan struct{})
	name := "Must remain private"
	go func() {
		// InTenant caches [alias, canonical] before the write waits. Unlink
		// must remove the canonical identity from RLS, even in this transaction.
		_, err := (Store{pool}).Update(ctx, f.member, private.ID, UpdateInput{Revision: 1, Name: &name})
		result <- err
		close(done)
	}()
	if dbtest.BlockedOrDone(t, ctx, f.d.Admin, tx.Conn().PgConn().PID(), done) == "" {
		t.Fatal("alias write did not overlap the unlink")
	}
	if _, err := tx.Exec(ctx, `UPDATE principals SET linked_to=NULL WHERE tenant_id=$1 AND id=$2`, f.member.TenantID, f.member.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRoleWith(t, tx, f.member.TenantID, f.member.ID, "member")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Require a hidden row, not merely permission rejection: the old cached
	// canonical identity must no longer reveal the theme's existence.
	if err := dbtest.Await(t, ctx, result); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cached canonical identity retained theme visibility: %v", err)
	}
	got, err := f.s.Get(t.Context(), f.other, private.ID)
	if err != nil || !same(got, private) {
		t.Fatalf("post-unlink alias changed canonical theme: %+v %v", got, err)
	}
}
