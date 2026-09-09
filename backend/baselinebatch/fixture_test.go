// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/brand"
	appdb "github.com/inspr-at/paimos/backend/db"
)

var baselineSchema struct {
	once  sync.Once
	bytes []byte
	err   error
}

func baselineDBPath(dir string) string {
	return filepath.Join(dir, brand.Default.DBFilename)
}

func closeBaselineDB() error {
	if appdb.DB == nil {
		return nil
	}
	err := appdb.DB.Close()
	appdb.DB = nil
	return err
}

func restoreEnv(key, prev string) {
	if prev == "" {
		_ = os.Unsetenv(key)
		return
	}
	_ = os.Setenv(key, prev)
}

// buildBaselineSchemaTemplate migrates current source schema once, then
// VACUUM INTO a closed snapshot. Per-case fixtures copy those bytes into a
// distinct DATA_DIR and still call db.Open so connection hooks, pool settings,
// and migrateThrough skip checks remain. Test-mode MEMORY journal is flushed
// into the snapshot; a leftover WAL sidecar would omit committed pages.
func buildBaselineSchemaTemplate() error {
	dir, err := os.MkdirTemp("", "paimos-baselinebatch-schema-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	prevDir, prevMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	if err := os.Setenv("DATA_DIR", dir); err != nil {
		return err
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		restoreEnv("DATA_DIR", prevDir)
		return err
	}
	defer func() {
		restoreEnv("DATA_DIR", prevDir)
		restoreEnv("PAIMOS_TEST_MODE", prevMode)
	}()
	if err := appdb.Open(); err != nil {
		_ = closeBaselineDB()
		return err
	}
	if _, err := appdb.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = closeBaselineDB()
		return fmt.Errorf("checkpoint baseline schema template: %w", err)
	}
	snap := filepath.Join(dir, "schema-snapshot.db")
	quoted := "'" + strings.ReplaceAll(snap, "'", "''") + "'"
	// #nosec G202 -- snap is a MkdirTemp path owned by this process; not user input.
	if _, err := appdb.DB.Exec(`VACUUM INTO ` + quoted); err != nil {
		_ = closeBaselineDB()
		return fmt.Errorf("snapshot baseline schema template: %w", err)
	}
	if err := closeBaselineDB(); err != nil {
		return err
	}
	for _, sidecar := range []string{snap + "-wal", snap + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			return fmt.Errorf("baseline schema template left %s after snapshot", sidecar)
		}
	}
	bytes, err := os.ReadFile(snap)
	if err != nil {
		return err
	}
	if len(bytes) == 0 {
		return fmt.Errorf("baseline schema template is empty")
	}
	baselineSchema.bytes = bytes
	return nil
}

func ensureBaselineSchemaTemplate(t *testing.T) {
	t.Helper()
	baselineSchema.once.Do(func() {
		baselineSchema.err = buildBaselineSchemaTemplate()
	})
	if baselineSchema.err != nil {
		t.Fatalf("build baseline schema fixture: %v", baselineSchema.err)
	}
}

func writeBaselineSchemaClone(t *testing.T, dir string) string {
	t.Helper()
	ensureBaselineSchemaTemplate(t)
	path := baselineDBPath(dir)
	if err := os.WriteFile(path, baselineSchema.bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openBaselineTestDB(t *testing.T) *sql.DB {
	t.Helper()
	oldDir, oldMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	dir := t.TempDir()
	if err := os.Setenv("DATA_DIR", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		t.Fatal(err)
	}
	writeBaselineSchemaClone(t, dir)
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = closeBaselineDB()
		restoreEnv("DATA_DIR", oldDir)
		restoreEnv("PAIMOS_TEST_MODE", oldMode)
	})
	return appdb.DB
}

func openFreshMigratedBaselineDB(t *testing.T) *sql.DB {
	t.Helper()
	oldDir, oldMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	dir := t.TempDir()
	if err := os.Setenv("DATA_DIR", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		t.Fatal(err)
	}
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = closeBaselineDB()
		restoreEnv("DATA_DIR", oldDir)
		restoreEnv("PAIMOS_TEST_MODE", oldMode)
	})
	return appdb.DB
}

type baselineSchemaSnapshot struct {
	version     int
	schemaCount int
	tables      string
	users       int
	projects    int
	batches     int
	foreignKeys int
}

func readBaselineSchemaSnapshot(t *testing.T, database *sql.DB) baselineSchemaSnapshot {
	t.Helper()
	var snap baselineSchemaSnapshot
	version, err := appdb.CurrentSchemaVersion(database)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	snap.version = version
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions`).Scan(&snap.schemaCount); err != nil {
		t.Fatal(err)
	}
	rows, err := database.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []byte
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if len(names) > 0 {
			names = append(names, ',')
		}
		names = append(names, name...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	snap.tables = string(names)
	for _, check := range []struct {
		query string
		got   *int
	}{
		{`SELECT COUNT(*) FROM users`, &snap.users},
		{`SELECT COUNT(*) FROM projects`, &snap.projects},
		{`SELECT COUNT(*) FROM baseline_batch_batches`, &snap.batches},
		{`PRAGMA foreign_keys`, &snap.foreignKeys},
	} {
		if err := database.QueryRow(check.query).Scan(check.got); err != nil {
			t.Fatalf("%s: %v", check.query, err)
		}
	}
	return snap
}

func TestBaselineSchemaTemplateMatchesFreshOpen(t *testing.T) {
	var clone baselineSchemaSnapshot
	t.Run("clone", func(t *testing.T) {
		clone = readBaselineSchemaSnapshot(t, openBaselineTestDB(t))
	})
	if appdb.DB != nil {
		t.Fatal("clone fixture retained its connection")
	}
	t.Run("fresh migrate", func(t *testing.T) {
		fresh := readBaselineSchemaSnapshot(t, openFreshMigratedBaselineDB(t))
		if clone != fresh {
			t.Fatalf("cloned schema diverged from a current-source db.Open migrate:\nclone=%+v\nfresh=%+v", clone, fresh)
		}
		if clone.version == 0 || clone.schemaCount == 0 || clone.tables == "" {
			t.Fatalf("cloned schema is empty: %+v", clone)
		}
		if clone.projects != 0 || clone.batches != 0 {
			t.Fatalf("cloned fixture carried baseline rows: %+v", clone)
		}
		if clone.foreignKeys != 1 {
			t.Fatalf("cloned fixture foreign_keys=%d", clone.foreignKeys)
		}
	})
}

func TestBaselineSchemaClonesAreIsolated(t *testing.T) {
	var firstPath string
	var schemaVersion int
	t.Run("mutate first copy", func(t *testing.T) {
		database := openBaselineTestDB(t)
		firstPath = os.Getenv("DATA_DIR")
		if _, err := database.Exec(`INSERT INTO projects(name,key) VALUES('First clone','C1')`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`CREATE TABLE fixture_only_marker (value TEXT); INSERT INTO fixture_only_marker VALUES ('first fixture')`); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&schemaVersion); err != nil {
			t.Fatal(err)
		}
	})
	if appdb.DB != nil {
		t.Fatal("fixture cleanup retained its connection")
	}
	t.Run("independent second copy", func(t *testing.T) {
		database := openBaselineTestDB(t)
		if os.Getenv("DATA_DIR") == firstPath {
			t.Fatal("fixtures reused a mutable database directory")
		}
		var projects, marker, version, foreignKeys int
		for _, check := range []struct {
			query string
			got   *int
			want  int
		}{
			{`SELECT COUNT(*) FROM projects`, &projects, 0},
			{`SELECT COUNT(*) FROM sqlite_master WHERE name='fixture_only_marker'`, &marker, 0},
			{`SELECT MAX(version) FROM schema_versions`, &version, schemaVersion},
			{`PRAGMA foreign_keys`, &foreignKeys, 1},
		} {
			if err := database.QueryRow(check.query).Scan(check.got); err != nil || *check.got != check.want {
				t.Fatalf("isolated schema check %q: got=%d want=%d err=%v", check.query, *check.got, check.want, err)
			}
		}
	})
	t.Run("concurrent file clones", func(t *testing.T) {
		ensureBaselineSchemaTemplate(t)
		paths := []string{
			writeBaselineSchemaClone(t, t.TempDir()),
			writeBaselineSchemaClone(t, t.TempDir()),
		}
		type cloneResult struct {
			id    int64
			other int
			err   error
		}
		results := make([]cloneResult, len(paths))
		var wg sync.WaitGroup
		for i, path := range paths {
			wg.Add(1)
			go func(i int, path string) {
				defer wg.Done()
				conn, err := sql.Open("sqlite", path+"?_txlock=immediate")
				if err != nil {
					results[i] = cloneResult{err: err}
					return
				}
				defer conn.Close()
				key := fmt.Sprintf("CX%d", i)
				res, err := conn.Exec(`INSERT INTO projects(name,key) VALUES(?,?)`, fmt.Sprintf("Concurrent %d", i), key)
				if err != nil {
					results[i] = cloneResult{err: err}
					return
				}
				id, err := res.LastInsertId()
				if err != nil {
					results[i] = cloneResult{err: err}
					return
				}
				otherKey := fmt.Sprintf("CX%d", 1-i)
				var other int
				if err := conn.QueryRow(`SELECT COUNT(*) FROM projects WHERE key=?`, otherKey).Scan(&other); err != nil {
					results[i] = cloneResult{err: err}
					return
				}
				results[i] = cloneResult{id: id, other: other}
			}(i, path)
		}
		wg.Wait()
		for i, result := range results {
			if result.err != nil {
				t.Fatalf("concurrent clone %d: %v", i, result.err)
			}
			if result.id <= 0 {
				t.Fatalf("concurrent clone %d inserted id=%d", i, result.id)
			}
			if result.other != 0 {
				t.Fatalf("concurrent clone %d observed the other clone's row", i)
			}
		}
		if results[0].id != results[1].id {
			t.Fatalf("clones shared sqlite_sequence: ids=%d,%d", results[0].id, results[1].id)
		}
	})
}
