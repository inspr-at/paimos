// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Risk: changing only the advisory mode can leave every use serialized on the
// debounced usage row. All grants must reach the barrier before any can commit.
func TestKeyScopeUsesOverlapThroughAuthorization(t *testing.T) {
	m, owner, key, _ := trimFixture(t)
	prefix, secret, _ := parseBearer("Bearer " + key.Token)
	agent, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok {
		t.Fatal("authentication failed")
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), agent), 15*time.Second)
	defer cancel()
	use := func(tx pgx.Tx) error { return authz.RequireTx(ctx, tx, agent, "nodes.read", authz.Scope{}) }
	// Persist the first use: requests within the debounce window must not take
	// a conflicting tuple lock just to discover that no write is due.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, use); err != nil {
		t.Fatal(err)
	}
	const workers = 4
	entered, done, release := make(chan uint32, workers), make(chan error, workers), make(chan struct{})
	for range workers {
		go func() {
			done <- db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
				if err := use(tx); err != nil {
					return err
				}
				entered <- tx.Conn().PgConn().PID()
				select {
				case <-release:
					return use(tx) // Re-enter the held shared fence as real writes do.
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
	}
	pids := map[uint32]bool{}
	for range workers {
		select {
		case pid := <-entered:
			pids[pid] = true
		case err := <-done:
			t.Fatalf("use completed before all grants overlapped: %v", err)
		case <-ctx.Done():
			t.Fatal("same-key grants did not overlap: ", ctx.Err())
		}
	}
	if len(pids) != workers {
		t.Fatal("overlap did not use independent PostgreSQL transactions")
	}
	close(release)
	for range workers {
		if err := dbtest.Await(t, ctx, done); err != nil {
			t.Fatal(err)
		}
	}
}

// Risk: a waiting scope changer must drain existing uses, bar later uses, and
// reject stale authorization snapshots after commit (including repeatable reads).
func TestKeyScopeChangeDrainsUsesAndRejectsQueuedStaleScope(t *testing.T) {
	for _, change := range []string{"trim", "manual edit"} {
		for _, snapshot := range []bool{false, true} {
			name := change + "/read committed"
			if snapshot {
				name = change + "/repeatable read"
			}
			t.Run(name, func(t *testing.T) {
				m, owner, key, in := trimFixture(t)
				proposal := proposeTrim(t, m, owner, key.ID, in)
				prefix, secret, _ := parseBearer("Bearer " + key.Token)
				agent, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
				if err != nil || !ok {
					t.Fatal("authentication failed")
				}
				original := m.pool
				pool, barrier, ctx := dbtest.BarrierPool(t, original, func(sql string) bool {
					return strings.Contains(sql, "UPDATE agent_keys SET scopes=")
				})
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				m.pool = pool
				authctx := tenant.WithPrincipal(ctx, agent)
				entered, release := make(chan uint32, 1), make(chan struct{})
				used, changed, queued := make(chan error, 1), make(chan error, 1), make(chan error, 1)
				go func() {
					used <- db.InTenant(authctx, original, owner.TenantID, func(tx pgx.Tx) error {
						if err := authz.RequireTx(authctx, tx, agent, "nodes.read", authz.Scope{}); err != nil {
							return err
						}
						entered <- tx.Conn().PgConn().PID()
						select {
						case <-release:
							// A queued exclusive waiter cannot strand shared re-entry.
							return authz.RequireTx(authctx, tx, agent, "nodes.read", authz.Scope{})
						case <-ctx.Done():
							return ctx.Err()
						}
					})
				}()
				holder := dbtest.Await(t, ctx, entered)
				changeFinished := make(chan struct{})
				go func() {
					defer close(changeFinished)
					if change == "trim" {
						_, err := trimMutation(t, m, owner, proposal, "approve")
						changed <- err
					} else {
						_, err := m.agentKeyScopes(tenant.WithPrincipal(ctx, owner), owner, key.ID, &keyScopeDelta{Remove: []string{"nodes.write"}})
						changed <- err
					}
				}()
				if lock := dbtest.BlockedOrDone(t, ctx, original, holder, changeFinished); lock != "advisory" {
					t.Fatalf("scope change bypassed in-flight use: %q", lock)
				}
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					enter := db.InTenant
					if snapshot {
						enter = db.InTenantReadSnapshot
					}
					queued <- enter(authctx, original, owner.TenantID, func(tx pgx.Tx) error {
						return authz.RequireTx(authctx, tx, agent, "nodes.write", authz.Scope{})
					})
				}()
				// Observe both the exclusive changer and the later shared use queued
				// on the exact lock held by the first use; no scheduling/timing guess.
				for {
					var exclusive, shared int
					err := original.QueryRow(ctx, `SELECT count(*) FILTER (WHERE w.mode='ExclusiveLock'),count(*) FILTER (WHERE w.mode='ShareLock')
					 FROM pg_locks h JOIN pg_locks w USING(locktype,database,classid,objid,objsubid)
					 WHERE h.pid=$1 AND h.locktype='advisory' AND h.granted AND NOT w.granted`, holder).Scan(&exclusive, &shared)
					if err != nil {
						t.Fatal(err)
					}
					if exclusive == 1 && shared == 1 {
						break
					}
					select {
					case err := <-queued:
						t.Fatalf("new use bypassed queued scope change: %v", err)
					case err := <-changed:
						t.Fatalf("scope change escaped drain barrier: %v", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					default:
					}
				}
				close(release)
				if err := dbtest.Await(t, ctx, used); err != nil {
					t.Fatal(err)
				}
				changer := barrier.Wait(t, ctx)
				if lock := dbtest.BlockedOrDone(t, ctx, original, changer, finished); lock != "advisory" {
					t.Fatalf("new use bypassed uncommitted scope change: %q", lock)
				}
				barrier.Release()
				if err := dbtest.Await(t, ctx, changed); err != nil {
					t.Fatal(err)
				}
				err = dbtest.Await(t, ctx, queued)
				if snapshot {
					var pe *pgconn.PgError
					if !errors.As(err, &pe) || pe.Code != "40001" {
						t.Fatalf("stale repeatable-read key snapshot was not rejected: %v", err)
					}
				} else if !errors.Is(err, authz.ErrForbidden) {
					t.Fatalf("queued use retained removed scope: %v", err)
				}
			})
		}
	}
}
