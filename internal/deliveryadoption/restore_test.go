// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// cloneRecoveryPoint takes an actual PostgreSQL copy of this test's isolated
// database. No production provider, host, file or credential is involved. The
// provider protocol acceptance must additionally prove native backup/restore.
func cloneRecoveryPoint(t *testing.T, f *fixture) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	parsed, err := url.Parse(f.d.URL)
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	parsed.Path = "/postgres"
	maintenance, err := pgx.Connect(ctx, parsed.String())
	if err != nil {
		t.Fatal("test maintenance connection unavailable")
	}
	defer maintenance.Close(ctx)
	name := "aeon_adoption_restore_" + strings.ReplaceAll(newUUID(), "-", "")
	f.d.App.Close()
	f.d.Admin.Close()
	if _, err = maintenance.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()+` TEMPLATE `+pgx.Identifier{f.d.Name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cleanup, parsed.String())
		if err != nil {
			t.Error("restore cleanup connection failed")
			return
		}
		defer conn.Close(cleanup)
		if _, err = conn.Exec(cleanup, `DROP DATABASE `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`); err != nil {
			t.Error("restore cleanup failed")
		}
	})
	f.d.Admin, err = pgxpool.New(ctx, f.d.URL)
	if err != nil {
		t.Fatal("reopen fixture admin")
	}
	f.d.App, err = pgxpool.New(ctx, f.d.AppURL)
	if err != nil {
		t.Fatal("reopen fixture application")
	}
	f.s.pool = f.d.App
	restoreURL, err := url.Parse(f.d.AppURL)
	if err != nil {
		t.Fatal("invalid test application URL")
	}
	restoreURL.Path = "/" + name
	restored, err := pgxpool.New(ctx, restoreURL.String())
	if err != nil {
		t.Fatal("restore connection failed")
	}
	t.Cleanup(restored.Close)
	return restored
}

func TestIsolatedRestoreProvesPreMoveSourceAndPreFirstAdoptionBoundary(t *testing.T) {
	f := newFixture(t)
	f.legacyRelease(t, f.project, "REL-1", "planning", 1)
	f.member(t, f.project, f.legacyRelease(t, f.project, "REL-2", "planning", 2), "TK-1", 0)
	first := cloneRecoveryPoint(t, f)
	j := f.prepare(t, f.project)
	point := cloneRecoveryPoint(t, f)
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	restored := *f.s
	restored.pool = point
	report, err := restored.DryRun(t.Context(), f.p, f.project)
	if err != nil || report.Fingerprint != j.Fingerprint || !report.Eligible {
		t.Fatalf("isolated restore did not prove the pre-move source: %v", err)
	}
	count := func(pool *pgxpool.Pool) int {
		n := 0
		if err := db.InTenant(dbtest.Seed(t.Context()), pool, f.p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM project_delivery`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(first) != 0 || count(point) != 0 {
		t.Fatal("pre-adoption point contains an adopted project")
	}
	later := cloneRecoveryPoint(t, f)
	if count(later) != 1 {
		t.Fatal("later project point cannot prove the pre-E zero-adoption boot condition")
	}
	// Recovery suspension is independent release evidence; restored job state
	// alone must never reactivate migration during restore validation.
	restored.cfg.RecoveryReconciled = false
	if err = restored.prerequisites(t.Context()); err != ErrPrerequisite {
		t.Fatal("restore validation did not suspend activation", err)
	}
}
