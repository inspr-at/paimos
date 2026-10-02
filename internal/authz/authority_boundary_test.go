// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type authorityFixture struct {
	d                                          *dbtest.DB
	actor                                      tenant.Principal
	role, actorRole, identity, project, target string
}

func newAuthorityFixture(t *testing.T) authorityFixture {
	t.Helper()
	f := authorityFixture{d: dbtest.Open(t), actor: tenant.Principal{Kind: tenant.Person}}
	ctx := dbtest.Seed(t.Context())
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('authority','Authority') RETURNING id::text`).Scan(&f.actor.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, f.d.App, f.actor.TenantID, func(tx pgx.Tx) error {
		tid := f.actor.TenantID
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Inviter') RETURNING id::text`, tid).Scan(&f.actor.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Target') RETURNING id::text`, tid).Scan(&f.target); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'delegator','Delegator') RETURNING id::text`, tid).Scan(&f.actorRole); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'assigned','Assigned') RETURNING id::text`, tid).Scan(&f.role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'members.manage'),($1,$2,'nodes.read'),($1,$3,'nodes.read')`, tid, f.actorRole, f.role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tid, f.actor.ID, f.actorRole); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('https://issuer.example','invited','invited@example.com') RETURNING id::text`).Scan(&f.identity); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO invites(tenant_id,email,workspace_role_id,token_hash,expires_at,created_by) VALUES($1,'invited@example.com',$2,decode(repeat('ab',32),'hex'),now()+interval '1 day',$3)`, tid, f.role, f.actor.ID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'AUTH-1',id,'Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&f.project)
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// Observe a real database wait, not elapsed time. The deadline is only a
// failure bound; proceeding requires Postgres to identify the blocking tx.
func waitAuthorityBlock(t *testing.T, ctx context.Context, d *dbtest.DB, pid, blocker uint32, done <-chan error) {
	t.Helper()
	for {
		select {
		case err := <-done:
			t.Fatalf("operation completed before the barrier: %v", err)
		default:
		}
		var blocked bool
		if err := d.Admin.QueryRow(ctx, `SELECT $2::int=ANY(pg_blocking_pids($1::int))`, pid, blocker).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		runtime.Gosched()
	}
}
func authorityDeadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func lockAuthorityTenant(t *testing.T, ctx context.Context, f authorityFixture) pgx.Tx {
	t.Helper()
	tx, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, f.actor.TenantID); err != nil {
		t.Fatal(err)
	}
	return tx
}
func startAcceptance(ctx context.Context, f authorityFixture) (<-chan uint32, <-chan error) {
	started, done := make(chan uint32, 1), make(chan error, 1)
	go func() {
		done <- db.InTenant(ctx, f.d.App, f.actor.TenantID, func(tx pgx.Tx) error {
			started <- tx.Conn().PgConn().PID()
			_, err := AcceptInvite(ctx, tx, f.actor.TenantID, f.identity, "invited@example.com", "Invited", "")
			return err
		})
	}()
	return started, done
}

func TestInviteAcceptanceSerializesAuthorityChanges(t *testing.T) {
	for _, change := range []string{"demote", "deactivate", "extend-role", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			f := newAuthorityFixture(t)
			ctx := authorityDeadline(t)
			mutation := lockAuthorityTenant(t, ctx, f)
			started, done := startAcceptance(ctx, f)
			waitAuthorityBlock(t, ctx, f.d, <-started, mutation.Conn().PgConn().PID(), done)
			var err error
			switch change {
			case "demote":
				_, err = mutation.Exec(ctx, `DELETE FROM role_permissions WHERE role_id=$1 AND permission='members.manage'`, f.actorRole)
			case "deactivate":
				_, err = mutation.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, f.actor.ID)
			case "extend-role":
				_, err = mutation.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.write')`, f.actor.TenantID, f.role)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := mutation.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if change == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrNoInvite) {
				t.Fatalf("acceptance after %s: %v", change, err)
			}
			var people, bindings, consumed, events int
			if err := f.d.Admin.QueryRow(ctx, `SELECT
    (SELECT count(*) FROM principals WHERE identity_id=$1),
    (SELECT count(*) FROM role_bindings WHERE principal_id NOT IN ($2,$3)),
    (SELECT count(*) FROM invites WHERE accepted_at IS NOT NULL),
    (SELECT count(*) FROM events WHERE type IN ('invite.accepted','binding.set'))`, f.identity, f.actor.ID, f.target).Scan(&people, &bindings, &consumed, &events); err != nil {
				t.Fatal(err)
			}
			if people+bindings+consumed+events != 0 {
				t.Fatalf("denied acceptance persisted effects: %d %d %d %d", people, bindings, consumed, events)
			}
		})
	}
}

func TestInviteAcceptanceHoldsAuthorityThroughAliasWait(t *testing.T) {
	f := newAuthorityFixture(t)
	ctx := authorityDeadline(t)
	alias, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer alias.Rollback(context.Background())
	if _, err := alias.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,532))`, f.actor.TenantID); err != nil {
		t.Fatal(err)
	}
	started, done := startAcceptance(ctx, f)
	waitAuthorityBlock(t, ctx, f.d, <-started, alias.Conn().PgConn().PID(), done)
	// Acceptance has checked the inviter but is paused before any binding insert.
	// A competing demotion/role edit must not take the access lock now.
	for _, mode := range []string{"UPDATE", "NO KEY UPDATE", "SHARE"} {
		probe, err := f.d.Admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR `+mode+` NOWAIT`, f.actor.TenantID)
		_ = probe.Rollback(ctx)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "55P03" {
			t.Errorf("authority not fenced against %s before binding: %v", mode, err)
		}
	}
	if err := alias.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProjectAssignmentReadsRoleAfterAuthorityLock(t *testing.T) {
	for _, extend := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "extended"}[extend], func(t *testing.T) {
			f := newAuthorityFixture(t)
			ctx := authorityDeadline(t)
			mutation := lockAuthorityTenant(t, ctx, f)
			started, done := make(chan uint32, 1), make(chan error, 1)
			go func() {
				done <- db.InTenant(ctx, f.d.App, f.actor.TenantID, func(tx pgx.Tx) error {
					started <- tx.Conn().PgConn().PID()
					_, err := (&Module{pool: f.d.App}).setProjectBindingTx(ctx, tx, f.actor, f.project, f.target, f.role, false)
					return err
				})
			}()
			waitAuthorityBlock(t, ctx, f.d, <-started, mutation.Conn().PgConn().PID(), done)
			if extend {
				if _, err := mutation.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.write')`, f.actor.TenantID, f.role); err != nil {
					t.Fatal(err)
				}
			}
			if err := mutation.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err := <-done
			if extend && !errors.Is(err, ErrForbidden) || !extend && err != nil {
				t.Fatalf("assignment: %v", err)
			}
			var bindings int
			if err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE principal_id=$1`, f.target).Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			want := 1
			if extend {
				want = 0
			}
			if bindings != want {
				t.Fatalf("bindings=%d, want %d", bindings, want)
			}
		})
	}
}

func TestLastOwnerFenceSerializesRemovalAndDeactivation(t *testing.T) {
	f := newAuthorityFixture(t)
	ctx := authorityDeadline(t)
	if _, err := f.d.Admin.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='owner')
		WHERE tenant_id=$1 AND principal_id=$2;
`, f.actor.TenantID, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
		SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='owner'`, f.actor.TenantID, f.target); err != nil {
		t.Fatal(err)
	}
	removal, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer removal.Rollback(context.Background())
	if _, err := removal.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.actor.TenantID, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	started, done := make(chan uint32, 1), make(chan error, 1)
	go func() {
		done <- db.InTenant(ctx, f.d.App, f.actor.TenantID, func(tx pgx.Tx) error {
			started <- tx.Conn().PgConn().PID()
			_, err := tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE tenant_id=$1 AND id=$2`, f.actor.TenantID, f.target)
			return err
		})
	}()
	waitAuthorityBlock(t, ctx, f.d, <-started, removal.Conn().PgConn().PID(), done)
	if err := removal.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var pe *pgconn.PgError
	if err := <-done; !errors.As(err, &pe) || pe.Code != "23514" {
		t.Fatalf("last owner deactivation after concurrent removal: %v", err)
	}
	var remaining int
	if err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM role_bindings b
		JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
		JOIN principals p ON p.tenant_id=b.tenant_id AND p.id=b.principal_id
		WHERE b.tenant_id=$1 AND b.scope_type='workspace' AND r.key='owner' AND p.status='active'`, f.actor.TenantID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("active owners after concurrent mutations: %d", remaining)
	}
}
