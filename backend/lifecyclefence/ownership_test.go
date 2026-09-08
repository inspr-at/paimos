// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecyclefence

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRuntimeSessionOwnershipSQLMatchesExactScopeAndPreservesV1(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE runtime_row(registration_json TEXT);
CREATE TABLE session_row(account_label TEXT, account_key TEXT, dispatch_profile_id TEXT, dispatch_profile_version TEXT);`); err != nil {
		t.Fatal(err)
	}
	v3 := `{"schema_version":3,"account_scopes":[{"account_label":"chatgpt","accounts":[{"key":"codex-work","label":"Work"}],"profiles":[{"id":"codex-sol-high","version":"1"}]},{"account_label":"cursor_context","accounts":[{"key":"cursor-op","label":"Cursor"}],"profiles":[{"id":"cursor-composer","version":"1"}]}]}`
	v1 := `{"generation":"g","host":"h","account_label":"chatgpt","workspaces":[],"profiles":[]}`
	cases := []struct {
		name, registration, class, key, profile, version string
		want                                             int
	}{
		{"v1 chatgpt", v1, "chatgpt", "", "", "", 1},
		{"v1 cursor class", v1, "cursor_context", "", "", "", 0},
		{"v3 chatgpt exact", v3, "chatgpt", "codex-work", "codex-sol-high", "1", 1},
		{"v3 cursor exact", v3, "cursor_context", "cursor-op", "cursor-composer", "1", 1},
		{"v3 chatgpt vs cursor session", v3, "cursor_context", "codex-work", "codex-sol-high", "1", 0},
		{"v3 wrong key", v3, "chatgpt", "cursor-op", "codex-sol-high", "1", 0},
		{"v3 wrong profile version", v3, "chatgpt", "codex-work", "codex-sol-high", "999", 0},
		{"v3 cursor profile under chatgpt", v3, "chatgpt", "codex-work", "cursor-composer", "1", 0},
	}
	sqlText := `SELECT ` + RuntimeSessionOwnershipSQL("runtime_row", "session_row") + ` FROM runtime_row JOIN session_row`
	for _, tc := range cases {
		if _, err = db.Exec(`DELETE FROM runtime_row`); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`DELETE FROM session_row`); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO runtime_row(registration_json) VALUES(?)`, tc.registration); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO session_row VALUES(?,?,?,?)`, tc.class, tc.key, tc.profile, tc.version); err != nil {
			t.Fatal(err)
		}
		var got int
		if err = db.QueryRow(sqlText).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
	if RuntimeSessionOwnershipSQL("runtime;drop", "session") != "(0)" {
		t.Fatal("non-identifier alias must fail closed")
	}
}
