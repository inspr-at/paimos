// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration166AddsOnlyNullableUserCommandPaletteOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m166.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 163); err != nil {
		t.Fatalf("migrate through M163: %v", err)
	}
	result, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m166-existing','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	if err := migrateThrough(database, 166); err != nil {
		t.Fatalf("apply M166: %v", err)
	}
	var override sql.NullString
	if err := database.QueryRow(`SELECT command_palette_shortcut FROM users WHERE id=?`, userID).Scan(&override); err != nil {
		t.Fatal(err)
	}
	if override.Valid {
		t.Fatalf("existing user override=%q, want NULL inherit", override.String)
	}
	var instanceRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key='command_palette_shortcut'`).Scan(&instanceRows); err != nil {
		t.Fatal(err)
	}
	if instanceRows != 0 {
		t.Fatalf("migration seeded %d instance overrides, want zero", instanceRows)
	}
	for _, reserved := range []int{164, 165} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=?`, reserved).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("reserved M%d application count=%d, want zero", reserved, count)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := migrateThrough(reopened, 166); err != nil {
		t.Fatalf("reopen M166: %v", err)
	}
	var violations int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign_key_check=%d err=%v", violations, err)
	}
}
