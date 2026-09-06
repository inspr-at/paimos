package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration176AdditiveIdempotentAndGuarded(t *testing.T) {
	database, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "lifecycle.db")+"?_txlock=immediate")
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if e = migrateThrough(database, 175); e != nil {
		t.Fatal(e)
	}
	var before int
	if e = database.QueryRow(`SELECT COUNT(*) FROM harness_sessions`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = migrateThrough(database, 176); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range []string{"lifecycle_runtimes", "lifecycle_runtime_sessions", "lifecycle_intents", "lifecycle_intent_events"} {
		var found int
		if e = database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found); e != nil || found != 1 {
			t.Fatal("missing lifecycle table")
		}
	}
	var after, version int
	if e = database.QueryRow(`SELECT COUNT(*) FROM harness_sessions`).Scan(&after); e != nil || after != before {
		t.Fatal("migration rewrote existing harness projection")
	}
	if e = database.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=176`).Scan(&version); e != nil || version != 1 {
		t.Fatal("migration applied more than once")
	}
	var guards int
	if e = database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'lifecycle_%'`).Scan(&guards); e != nil || guards < 10 {
		t.Fatal("missing immutable guards")
	}
}
