// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/importer"
	"github.com/jackc/pgx/v5"
)

func TestApplySerializesImporterAtItsActualAdvisoryLock(t *testing.T) {
	f := newFixture(t)
	j := f.prepare(t, f.project)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.Contains(q, "hashtextextended($1,42)") })
	worker := *f.s
	worker.pool = pool
	applied := make(chan error, 1)
	go func() { _, err := worker.apply(ctx, f.a, j); applied <- err }()
	holder := barrier.Wait(t, ctx)
	done := make(chan struct{})
	backfill := make(chan error, 1)
	go func() { _, err := importer.BackfillRelations(ctx, f.d.App, f.p.TenantID); backfill <- err; close(done) }()
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, holder, done); lock != "advisory" {
		t.Fatalf("backfill did not serialize at importer key: %q", lock)
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, applied); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, backfill); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM project_delivery`); n != 1 {
		t.Fatal("atomic adoption missing")
	}
}

func TestFinalAuthorityFenceObservesCommittedRevocation(t *testing.T) {
	f := newFixture(t)
	f.secondOwner(t)
	j := f.prepare(t, f.project)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.Contains(q, "hashtextextended($1,42)") })
	worker := *f.s
	worker.pool = pool
	done := make(chan error, 1)
	go func() { _, err := worker.apply(ctx, f.a, j); done <- err }()
	barrier.Wait(t, ctx)
	err := db.InTenant(dbtest.Seed(ctx), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1 FOR UPDATE`, f.p.TenantID).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.p.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	barrier.Release()
	if err = dbtest.Await(t, ctx, done); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("revocation failed for wrong reason: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM project_delivery`) > 0 {
		t.Fatal("revoked authority switched mode")
	}
}

func TestReportUsesOneRepeatableReadSnapshotAcrossPages(t *testing.T) {
	f := newFixture(t)
	f.legacyRelease(t, f.project, "REL-1", "planning", 1)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.Contains(q, "k.slug,coalesce(nullif(n.fields->>'project_key'") })
	worker := *f.s
	worker.pool = pool
	before, err := f.s.DryRun(ctx, f.p, f.project)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan Report, 1)
	failure := make(chan error, 1)
	go func() { r, err := worker.DryRun(ctx, f.p, f.project); result <- r; failure <- err }()
	barrier.Wait(t, ctx)
	f.node(t, "ticket", "TK-1", f.project, "open")
	barrier.Release()
	r := dbtest.Await(t, ctx, result)
	if err = dbtest.Await(t, ctx, failure); err != nil {
		t.Fatal(err)
	}
	if r.Fingerprint != before.Fingerprint || r.Counts.Members != 0 {
		t.Fatal("snapshot mixed source revisions")
	}
	fresh, err := f.s.DryRun(context.Background(), f.p, f.project)
	if err != nil || fresh.Counts.Members != 1 || fresh.Fingerprint == r.Fingerprint {
		t.Fatal("fixture did not retain concurrent source change", err)
	}
}
