// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Pause exactly at the tree acquisition, after any preceding desk fence. This
// barrier proves tenant-before-tree serialization without depending on the
// deadlock detector or on whether two goroutines happen to overlap.
type deskTreeBarrier struct {
	pgx.Tx
	reached chan struct{}
	release chan struct{}
}

func (tx deskTreeBarrier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "pg_advisory_xact_lock") && !strings.Contains(sql, "aeon-pairing:") {
		close(tx.reached)
		select {
		case <-tx.release:
		case <-ctx.Done():
			return pgconn.CommandTag{}, ctx.Err()
		}
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

// NOWAIT makes the competing authority write report the specific lock conflict
// immediately. Every other statement and fence uses the real database.
type tenantNowait struct{ pgx.Tx }

func (tx tenantNowait) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "FROM tenants") && strings.Contains(sql, "FOR ") {
		sql += " NOWAIT"
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func TestDeskFenceDoesNotInvertProjectAccessWrite(t *testing.T) {
	for _, fence := range []string{"project mutation", "project write"} {
		t.Run(fence, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			project, err := f.d.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer project.Rollback(ctx)
			if _, err = project.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, f.person.TenantID); err != nil {
				t.Fatal(err)
			}
			reached, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error { return treeLock(ctx, deskTreeBarrier{tx, reached, release}, f.person.TenantID) })
			}()
			select {
			case <-reached:
			case <-ctx.Done():
				t.Fatal("desk did not reach tree barrier")
			}
			lock := authz.LockProjectMutation
			if fence == "project write" {
				lock = authz.LockProjectWrite
			}
			err = lock(ctx, tenantNowait{project}, f.person.TenantID)
			close(release)
			_ = project.Rollback(ctx)
			deskErr := <-done
			var conflict *pgconn.PgError
			if !errors.As(err, &conflict) || conflict.Code != "55P03" {
				t.Fatalf("project authority write must serialize on the tenant before the desk tree fence: %v", err)
			}
			if deskErr != nil {
				t.Fatalf("desk fence: %v", deskErr)
			}
		})
	}
}
func TestDeskFenceAcquiresTenantTreeBeforePairing(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	reached, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error { return treeLock(ctx, deskTreeBarrier{tx, reached, release}, f.person.TenantID) })
	}()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal("desk did not reach tree barrier")
	}
	var available bool
	err := db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('aeon-pairing:'||current_setting('aeon.tenant_id'),0))`).Scan(&available)
	})
	close(release)
	deskErr := <-done
	if err != nil || deskErr != nil {
		t.Fatalf("pairing probe: %v; desk: %v", err, deskErr)
	}
	if !available {
		t.Fatal("desk acquired pairing before the tree fence")
	}
	if err := db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := treeLock(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		return db.InTenant(ctx, f.d.App, f.person.TenantID, func(probe pgx.Tx) error {
			return probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('aeon-pairing:'||$1,0))`, f.person.TenantID).Scan(&available)
		})
	}); err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("desk completed its resource fence without holding pairing")
	}
}
