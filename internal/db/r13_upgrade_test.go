// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// The baseline closure is origin/main at cbcab384a05fc3dff9c5112be57ee17c20db473b:
// 149 SQL files, ending at 0995. Hash name + NUL + exact bytes + NUL in name order.
// Fixed fixture dates make this upgrade independent of wall-clock schedules.
func TestR13UpgradeFromMain0995(t *testing.T) {
	names := migrationNames(t)
	hash := sha256.New()
	var baseline, pending []string
	seen := map[string]string{}
	for _, name := range names {
		number := strings.SplitN(name, "_", 2)[0]
		if previous := seen[number]; previous != "" {
			t.Fatalf("duplicate migration number: %s and %s", previous, name)
		}
		seen[number] = name
		if number > "0995" {
			pending = append(pending, name)
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%s%c", name, 0)
		hash.Write(body)
		hash.Write([]byte{0})
		baseline = append(baseline, name)
	}
	if len(baseline) != 149 || fmt.Sprintf("%x", hash.Sum(nil)) != "0680fac546383acb2741eeea2a297e0a951b42ad3e5fcdff90d7b60d10815b85" {
		t.Fatal("main 0995 baseline was changed; upgrade proof needs the exact published schema")
	}
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	stopped := errors.New("baseline reached")
	var applied []string
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name > "0995_agent_creation.sql" {
			return stopped
		}
		applied = append(applied, name)
		return nil
	})
	if !errors.Is(err, stopped) || !reflect.DeepEqual(applied, baseline) {
		t.Fatalf("baseline migration order: %v", err)
	}
	var count int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != len(baseline) {
		t.Fatalf("baseline count %d: %v", count, err)
	}
	fixed := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	var tenantID, sourceID string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('r13-upgrade','R13 upgrade') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO doctrine_sources(tenant_id,repository,visibility,commit_sha,paths,pinned_at,created_at,updated_at)
   VALUES($1,'inspr-at/inspr-modules','public',repeat('a',40),ARRAY['docs'], $2,$2,$2) RETURNING id::text`, tenantID, fixed).Scan(&sourceID)
	})
	if err != nil {
		t.Fatal(err)
	}
	applied = nil
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error { applied = append(applied, name); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, pending) {
		t.Fatalf("upgrade did not follow filename order: %v", applied)
	}
	// Exercise the replaced unpin trigger against data from the old schema.
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE doctrine_sources SET commit_sha=repeat('b',40) WHERE id=$1`, sourceID); err != nil {
			return err
		}
		var at time.Time
		if err := tx.QueryRow(t.Context(), `SELECT created_at FROM doctrine_sources WHERE id=$1`, sourceID).Scan(&at); err != nil {
			return err
		}
		if !at.Equal(fixed) {
			return errors.New("old fixture date changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second upgrade is a no-op, including applied_at; do not silently replay DDL.
	var before, after string
	snapshot := `SELECT string_agg(version || ':' || applied_at::text, ',' ORDER BY version) FROM schema_migrations`
	if err := d.App.QueryRow(t.Context(), snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error { return fmt.Errorf("replayed %s", name) }); err != nil {
		t.Fatal(err)
	}
	if err := d.App.QueryRow(t.Context(), snapshot).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("idempotent upgrade changed migration ledger")
	}
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != len(names) {
		t.Fatalf("integrated count %d: %v", count, err)
	}
	t.Logf("upgraded %d baseline migrations with %d ordered migrations; replay unchanged", len(baseline), len(pending))
}
