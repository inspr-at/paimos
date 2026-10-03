// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/jackc/pgx/v5"
)

// Stop a real node move after event-id allocation and before its tenant FK
// check. An access writer then holds its tenant fence while waiting for that
// counter. Releasing the move must let both transactions commit, not deadlock.
func TestAccessWritesDoNotDeadlockNodeMove(t *testing.T) {
	for _, operation := range []string{"invite", "operator-link", "link-tx", "link-tx-bound"} {
		t.Run(operation, func(t *testing.T) {
			p := newPrincipal(t, "access-move")
			kind := kindBySlug(t, p, "project")
			parent := mustNode(t, p, `{"kind_id":"`+kind.ID+`","title":"Destination"}`)
			node := mustNode(t, p, `{"kind_id":"`+kind.ID+`","title":"Moving"}`)
			ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 15*time.Second)
			defer cancel()
			var identity, alias, target string
			if err := db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email)
					VALUES('https://access.example.test',$1,'invited@example.test') RETURNING id::text`, p.TenantID).Scan(&identity); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name)
					VALUES($1,'person','Synthetic alias') RETURNING id::text`, p.TenantID).Scan(&alias); err != nil {
					return err
				}
				// Keep the link target distinct from the move's audit actor so a
				// principal FK lock cannot hide the tenant/event-counter cycle.
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name)
					VALUES($1,'person','Synthetic target') RETURNING id::text`, p.TenantID).Scan(&target); err != nil {
					return err
				}
				if operation == "link-tx-bound" {
					if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
						SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='viewer'`, p.TenantID, alias); err != nil {
						return err
					}
				}
				_, err := tx.Exec(ctx, `INSERT INTO invites(tenant_id,email,workspace_role_id,token_hash,expires_at,created_by)
					SELECT $1,'invited@example.test',id,decode(repeat('ab',32),'hex'),now()+interval '1 day',$2
					FROM roles WHERE tenant_id=$1 AND key='viewer'`, p.TenantID, p.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := adminPool.Exec(ctx, `CREATE FUNCTION pause_move_event() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN PERFORM pg_advisory_xact_lock(558); RETURN NEW; END $$;
				CREATE TRIGGER events_pause_move BEFORE INSERT ON events FOR EACH ROW
				WHEN (NEW.type='node.moved') EXECUTE FUNCTION pause_move_event()`); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := adminPool.Exec(context.Background(), `DROP TRIGGER events_pause_move ON events; DROP FUNCTION pause_move_event()`); err != nil {
					t.Error(err)
				}
			}()
			barrier, err := adminPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			if _, err := barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(558)`); err != nil {
				t.Fatal(err)
			}
			moveDone := make(chan error, 1)
			go func() {
				_, err := New(appPool, nil).(*Module).moveNode(ctx, p, node.ID, &parent.ID, nil, nil)
				moveDone <- err
			}()
			movePID := waitAccessMoveBlock(t, ctx, barrier.Conn().PgConn().PID(), moveDone)
			accessDone := make(chan error, 1)
			go func() {
				if operation == "operator-link" {
					_, err := principallink.New(appPool).Link(ctx, "access-move", alias, target)
					accessDone <- err
					return
				}
				accessDone <- db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error {
					if operation == "invite" {
						_, err := authz.AcceptInvite(ctx, tx, p.TenantID, identity, "invited@example.test", "Invited", "")
						return err
					}
					_, err := principallink.LinkTx(ctx, tx, p.TenantID, alias, target, p.ID, "principal.linked", "principal.unlinked")
					return err
				})
			}()
			waitAccessMoveBlock(t, ctx, movePID, accessDone)
			if err := barrier.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			for _, result := range []struct {
				name string
				done <-chan error
			}{{"move", moveDone}, {operation, accessDone}} {
				select {
				case err := <-result.done:
					if err != nil {
						t.Errorf("%s: %v", result.name, err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			var moved bool
			if err := adminPool.QueryRow(ctx, `SELECT parent_id IS NOT DISTINCT FROM $2::uuid FROM nodes WHERE id=$1`, node.ID, parent.ID).Scan(&moved); err != nil || !moved {
				t.Fatalf("move did not commit: moved=%t err=%v", moved, err)
			}
		})
	}
}

func waitAccessMoveBlock(t *testing.T, ctx context.Context, blocker uint32, done <-chan error) uint32 {
	t.Helper()
	for {
		select {
		case err := <-done:
			t.Fatalf("operation finished before lock barrier: %v", err)
		default:
		}
		var pid uint32
		if err := adminPool.QueryRow(ctx, `SELECT COALESCE(min(pid),0) FROM pg_locks
			WHERE NOT granted AND $1::int=ANY(pg_blocking_pids(pid))`, blocker).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		runtime.Gosched()
	}
}
