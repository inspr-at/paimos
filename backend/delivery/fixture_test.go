package delivery

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/brand"
	appdb "github.com/inspr-at/paimos/backend/db"
)

var deliverySchema struct {
	once  sync.Once
	bytes []byte
	err   error
}

func deliveryDBPath(dir string) string {
	return filepath.Join(dir, brand.Default.DBFilename)
}

func closeDeliveryDB() error {
	if appdb.DB == nil {
		return nil
	}
	err := appdb.DB.Close()
	appdb.DB = nil
	return err
}

// buildDeliverySchemaTemplate migrates current source schema once in a
// process-owned directory, checkpoints, and captures immutable database bytes.
// Test mode uses an in-memory journal, so closing the pool flushes committed
// migrations into the main file before the copy; a leftover WAL sidecar means
// the snapshot would omit those pages.
func buildDeliverySchemaTemplate() error {
	dir, err := os.MkdirTemp("", "paimos-delivery-schema-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	prevDir, prevMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	if err := os.Setenv("DATA_DIR", dir); err != nil {
		return err
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		return err
	}
	defer func() {
		_ = os.Setenv("DATA_DIR", prevDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", prevMode)
	}()
	if err := appdb.Open(); err != nil {
		_ = closeDeliveryDB()
		return err
	}
	if _, err := appdb.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = closeDeliveryDB()
		return fmt.Errorf("checkpoint delivery schema template: %w", err)
	}
	if err := closeDeliveryDB(); err != nil {
		return err
	}
	path := deliveryDBPath(dir)
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			return fmt.Errorf("delivery schema template left %s after checkpoint", sidecar)
		}
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(bytes) == 0 {
		return fmt.Errorf("delivery schema template is empty")
	}
	deliverySchema.bytes = bytes
	return nil
}

func ensureDeliverySchemaTemplate(t *testing.T) {
	t.Helper()
	deliverySchema.once.Do(func() {
		deliverySchema.err = buildDeliverySchemaTemplate()
	})
	if deliverySchema.err != nil {
		t.Fatalf("build delivery schema fixture: %v", deliverySchema.err)
	}
}

func writeDeliverySchemaClone(t *testing.T, dir string) string {
	t.Helper()
	ensureDeliverySchemaTemplate(t)
	path := deliveryDBPath(dir)
	if err := os.WriteFile(path, deliverySchema.bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openDeliveryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	oldDir, oldMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	dir := t.TempDir()
	if err := os.Setenv("DATA_DIR", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		t.Fatal(err)
	}
	writeDeliverySchemaClone(t, dir)
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = closeDeliveryDB()
		_ = os.Setenv("DATA_DIR", oldDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", oldMode)
	})
	return appdb.DB
}

func openFreshMigratedDeliveryDB(t *testing.T) *sql.DB {
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
		_ = closeDeliveryDB()
		_ = os.Setenv("DATA_DIR", oldDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", oldMode)
	})
	return appdb.DB
}

type deliverySchemaSnapshot struct {
	version     int
	schemaCount int
	tables      string
	users       int
	deliveries  int
	issues      int
	foreignKeys int
}

func readDeliverySchemaSnapshot(t *testing.T, database *sql.DB) deliverySchemaSnapshot {
	t.Helper()
	var snap deliverySchemaSnapshot
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
		{`SELECT COUNT(*) FROM deliveries`, &snap.deliveries},
		{`SELECT COUNT(*) FROM issues`, &snap.issues},
		{`PRAGMA foreign_keys`, &snap.foreignKeys},
	} {
		if err := database.QueryRow(check.query).Scan(check.got); err != nil {
			t.Fatalf("%s: %v", check.query, err)
		}
	}
	return snap
}

func TestDeliverySchemaTemplateMatchesFreshOpen(t *testing.T) {
	var clone deliverySchemaSnapshot
	t.Run("clone", func(t *testing.T) {
		clone = readDeliverySchemaSnapshot(t, openDeliveryTestDB(t))
	})
	if appdb.DB != nil {
		t.Fatal("clone fixture retained its connection")
	}
	t.Run("fresh migrate", func(t *testing.T) {
		fresh := readDeliverySchemaSnapshot(t, openFreshMigratedDeliveryDB(t))
		if clone != fresh {
			t.Fatalf("cloned schema diverged from a current-source db.Open migrate:\nclone=%+v\nfresh=%+v", clone, fresh)
		}
		if clone.version == 0 || clone.schemaCount == 0 || clone.tables == "" {
			t.Fatalf("cloned schema is empty: %+v", clone)
		}
		if clone.deliveries != 0 || clone.issues != 0 {
			t.Fatalf("cloned fixture carried delivery rows: %+v", clone)
		}
		if clone.foreignKeys != 1 {
			t.Fatalf("cloned fixture foreign_keys=%d", clone.foreignKeys)
		}
	})
}

func TestDeliverySchemaClonesAreIsolated(t *testing.T) {
	var firstPath string
	var schemaVersion int
	t.Run("mutate first copy", func(t *testing.T) {
		database := openDeliveryTestDB(t)
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
		database := openDeliveryTestDB(t)
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
		ensureDeliverySchemaTemplate(t)
		paths := []string{
			writeDeliverySchemaClone(t, t.TempDir()),
			writeDeliverySchemaClone(t, t.TempDir()),
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
