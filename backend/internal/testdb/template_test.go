package testdb

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
)

func TestTemplateIsolationAndProductionOpen(t *testing.T) {
	var versions []string
	for i, mode := range []string{"1", ""} {
		t.Run([]string{"memory", "wal"}[i], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DATA_DIR", dir)
			t.Setenv("PAIMOS_TEST_MODE", mode)
			start := time.Now()
			Prepare(t)
			t.Logf("prepare (cold=%t): %s", i == 0, time.Since(start))
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != brand.Default.DBFilename {
				t.Fatalf("fixture must copy only the database file: entries=%v err=%v", entries, err)
			}
			start = time.Now()
			if err := db.Open(); err != nil {
				t.Fatal(err)
			}
			t.Logf("db.Open on migrated copy: %s", time.Since(start))
			database := db.DB
			t.Cleanup(func() { _ = database.Close(); db.DB = nil })
			var changes int
			if err := database.QueryRow("SELECT total_changes()").Scan(&changes); err != nil || changes != 0 {
				t.Fatalf("already-migrated Open wrote rows: changes=%d err=%v", changes, err)
			}
			var journal string
			wantJournal := []string{"memory", "wal"}[i]
			if err := database.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != wantJournal {
				t.Fatalf("journal=%q want=%q err=%v", journal, wantJournal, err)
			}
			if got := database.Stats().MaxOpenConnections; got != db.DefaultMaxOpenConnections {
				t.Fatalf("pool limit=%d", got)
			}
			// Keep two connections checked out to verify the production hook
			// also initializes connections opened after db.Open returns.
			for range 2 {
				conn, err := database.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				var foreignKeys, busy int
				if err := conn.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
					t.Fatalf("foreign_keys=%d err=%v", foreignKeys, err)
				}
				if err := conn.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != db.DefaultBusyTimeoutMS {
					t.Fatalf("busy_timeout=%d err=%v", busy, err)
				}
				var folded string
				if err := conn.QueryRowContext(context.Background(), "SELECT paimos_casefold('HELLO')").Scan(&folded); err != nil || folded != "hello" {
					t.Fatalf("registered SQL function=%q err=%v", folded, err)
				}
			}
			rows, err := database.Query("SELECT version || ':' || applied_at FROM schema_versions ORDER BY version")
			if err != nil {
				t.Fatal(err)
			}
			var gotVersions []string
			for rows.Next() {
				var version string
				if err := rows.Scan(&version); err != nil {
					t.Fatal(err)
				}
				gotVersions = append(gotVersions, version)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = rows.Close()
			if len(gotVersions) == 0 {
				t.Fatal("template has no migrations")
			}
			if i == 0 {
				versions = gotVersions
			} else if !reflect.DeepEqual(versions, gotVersions) {
				t.Fatal("migration versions or timestamps changed between copies")
			}
			for _, table := range []string{"users", "sessions", "projects", "issues"} {
				var count int
				if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("fixture leaked rows into %s: count=%d err=%v", table, count, err)
				}
			}
			if _, err := database.Exec("INSERT INTO users(username,password) VALUES('fixture-only','disabled')"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("DELETE FROM schema_versions WHERE version=1"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".secret-key"), []byte("synthetic fixture marker"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}
