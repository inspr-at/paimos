// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openM162Fixture(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m162.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 161); err != nil {
		t.Fatalf("create exact M161 fixture: %v", err)
	}
	return database
}

func seedM162Owners(t *testing.T, database *sql.DB) (projectA, projectB, userA, userB int64) {
	t.Helper()
	insert := func(query string, args ...any) int64 {
		result, err := database.Exec(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	projectA = insert(`INSERT INTO projects(name,key) VALUES('M162 A','M2A')`)
	projectB = insert(`INSERT INTO projects(name,key) VALUES('M162 B','M2B')`)
	userA = insert(`INSERT INTO users(username,password,role,status) VALUES('m162-a','x','member','active')`)
	userB = insert(`INSERT INTO users(username,password,role,status) VALUES('m162-b','x','member','active')`)
	return
}

func insertM162Issue(t *testing.T, database *sql.DB, projectID, userID any, number int, issueType, slug string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO issues(project_id,user_id,issue_number,type,title,status,priority,slug)
		VALUES(?,?,?,?,?,'backlog','medium',?)`, projectID, userID, number, issueType, "M162 "+slug, slug)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigration162StopsWithActionableLegacyViolations(t *testing.T) {
	tests := []struct {
		name      string
		plant     func(*testing.T, *sql.DB, int64, int64)
		wantError string
	}{
		{
			name: "project collision",
			plant: func(t *testing.T, database *sql.DB, projectID, userID int64) {
				if _, err := database.Exec(`DROP INDEX idx_issues_type_slug_project`); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`CREATE INDEX idx_issues_type_slug_project
					ON issues(type,slug,project_id) WHERE slug IS NOT NULL`); err != nil {
					t.Fatal(err)
				}
				insertM162Issue(t, database, projectID, nil, 1, "memory", "duplicate")
				insertM162Issue(t, database, projectID, nil, 2, "memory", "duplicate")
			},
			wantError: "scope=project:",
		},
		{
			name: "user collision",
			plant: func(t *testing.T, database *sql.DB, projectID, userID int64) {
				insertM162Issue(t, database, nil, userID, 1, "memory", "duplicate")
				insertM162Issue(t, database, nil, userID, 2, "memory", "duplicate")
			},
			wantError: "scope=user:",
		},
		{
			name: "instance collision",
			plant: func(t *testing.T, database *sql.DB, projectID, userID int64) {
				insertM162Issue(t, database, nil, nil, 1, "memory", "duplicate")
				insertM162Issue(t, database, nil, nil, 2, "memory", "duplicate")
			},
			wantError: "scope=instance:0",
		},
		{
			name: "mixed ownership",
			plant: func(t *testing.T, database *sql.DB, projectID, userID int64) {
				insertM162Issue(t, database, projectID, userID, 1, "memory", "mixed")
			},
			wantError: "invalid knowledge ownership",
		},
		{
			name: "unsupported user type",
			plant: func(t *testing.T, database *sql.DB, projectID, userID int64) {
				insertM162Issue(t, database, nil, userID, 1, "ticket", "wrong_type")
			},
			wantError: `type="ticket"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openM162Fixture(t)
			projectID, _, userID, _ := seedM162Owners(t, database)
			test.plant(t, database, projectID, userID)
			err := migrateThrough(database, 162)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("M162 error=%v, want actionable %q", err, test.wantError)
			}
			if strings.Contains(test.name, "collision") && !strings.Contains(err.Error(), "ids=[") {
				t.Fatalf("M162 collision error does not name row IDs: %v", err)
			}
			var version int
			if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil || version != 161 {
				t.Fatalf("schema version=%d err=%v, want unchanged 161", version, err)
			}
			var newObjects int
			if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'idx_issues_knowledge_%' OR name LIKE 'trg_issues_scope_%' OR name LIKE 'trg_issues_user_type_%'`).Scan(&newObjects); err != nil || newObjects != 0 {
				t.Fatalf("M162 failure left %d schema objects, err=%v", newObjects, err)
			}
		})
	}
}

func TestMigration162StopsActionablyWhenLegacyIndexIsMissing(t *testing.T) {
	database := openM162Fixture(t)
	if _, err := database.Exec(`DROP INDEX idx_issues_type_slug_project`); err != nil {
		t.Fatal(err)
	}
	err := migrateThrough(database, 162)
	if err == nil || !strings.Contains(err.Error(), "M162 prerequisite is missing: index:idx_issues_type_slug_project") {
		t.Fatalf("M162 error=%v, want actionable missing prerequisite", err)
	}
	var version int
	if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil || version != 161 {
		t.Fatalf("schema version=%d err=%v, want unchanged 161", version, err)
	}
	var newObjects int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'idx_issues_knowledge_%' OR name LIKE 'trg_issues_scope_%' OR name LIKE 'trg_issues_user_type_%'`).Scan(&newObjects); err != nil || newObjects != 0 {
		t.Fatalf("missing prerequisite left %d M162 objects, err=%v", newObjects, err)
	}
}

func TestMigration162EnforcesThreeIndependentScopeIdentities(t *testing.T) {
	database := openM162Fixture(t)
	projectA, projectB, userA, userB := seedM162Owners(t, database)

	// The same type+slug is valid once in each distinct owner scope.
	insertM162Issue(t, database, projectA, nil, 1, "memory", "shared")
	insertM162Issue(t, database, projectB, nil, 1, "memory", "shared")
	insertM162Issue(t, database, nil, userA, 1, "memory", "shared")
	insertM162Issue(t, database, nil, userB, 1, "memory", "shared")
	insertM162Issue(t, database, nil, nil, 1, "memory", "shared")

	if err := migrateThrough(database, 162); err != nil {
		t.Fatalf("apply M162 to valid populated database: %v", err)
	}
	for _, name := range []string{
		"idx_issues_knowledge_project_identity",
		"idx_issues_knowledge_user_identity",
		"idx_issues_knowledge_instance_identity",
		"trg_issues_scope_owner_insert",
		"trg_issues_scope_owner_update",
		"trg_issues_user_type_insert",
		"trg_issues_user_type_update",
	} {
		var kind string
		if err := database.QueryRow(`SELECT type FROM sqlite_master WHERE name=?`, name).Scan(&kind); err != nil {
			t.Fatalf("M162 object %s missing: %v", name, err)
		}
	}

	assertRejected := func(label string, query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err == nil {
			t.Fatalf("%s was accepted", label)
		}
	}
	insertSQL := `INSERT INTO issues(project_id,user_id,issue_number,type,title,status,priority,slug)
		VALUES(?,?,?,?,?,'backlog','medium',?)`
	assertRejected("duplicate project identity", insertSQL, projectA, nil, 2, "memory", "duplicate", "shared")
	assertRejected("duplicate user identity", insertSQL, nil, userA, 2, "memory", "duplicate", "shared")
	assertRejected("duplicate instance identity", insertSQL, nil, nil, 2, "memory", "duplicate", "shared")
	assertRejected("mixed ownership", insertSQL, projectA, userA, 3, "memory", "mixed", "mixed")
	assertRejected("unsupported user type", insertSQL, nil, userA, 3, "ticket", "ticket", "ticket")

	var projectRow int64
	if err := database.QueryRow(`SELECT id FROM issues WHERE project_id=? AND slug='shared'`, projectA).Scan(&projectRow); err != nil {
		t.Fatal(err)
	}
	assertRejected("update to mixed ownership", `UPDATE issues SET user_id=? WHERE id=?`, userA, projectRow)
	var userRow int64
	if err := database.QueryRow(`SELECT id FROM issues WHERE user_id=? AND slug='shared'`, userA).Scan(&userRow); err != nil {
		t.Fatal(err)
	}
	assertRejected("update to unsupported user type", `UPDATE issues SET type='ticket' WHERE id=?`, userRow)

	// Soft-deleted knowledge no longer occupies the live identity. This
	// preserves the existing delete-then-recreate contract at every scope.
	for _, rowID := range []int64{projectRow, userRow} {
		if _, err := database.Exec(`UPDATE issues SET deleted_at=datetime('now') WHERE id=?`, rowID); err != nil {
			t.Fatalf("soft-delete identity row %d: %v", rowID, err)
		}
	}
	var instanceRow int64
	if err := database.QueryRow(`SELECT id FROM issues WHERE project_id IS NULL AND user_id IS NULL AND slug='shared'`).Scan(&instanceRow); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE issues SET deleted_at=datetime('now') WHERE id=?`, instanceRow); err != nil {
		t.Fatalf("soft-delete instance identity: %v", err)
	}
	insertM162Issue(t, database, projectA, nil, 4, "memory", "shared")
	insertM162Issue(t, database, nil, userA, 4, "memory", "shared")
	insertM162Issue(t, database, nil, nil, 4, "memory", "shared")

	var version int
	if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil || version != 162 {
		t.Fatalf("schema version=%d err=%v, want 162", version, err)
	}
	var oldIndex int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_issues_type_slug_project'`).Scan(&oldIndex); err != nil || oldIndex != 0 {
		t.Fatalf("obsolete nullable index count=%d err=%v", oldIndex, err)
	}
}

func TestMigration162ScopeLookupsUseIdentityIndexes(t *testing.T) {
	database := openM162Fixture(t)
	projectID, _, userID, _ := seedM162Owners(t, database)
	insertM162Issue(t, database, projectID, nil, 1, "memory", "indexed")
	insertM162Issue(t, database, nil, userID, 1, "memory", "indexed")
	insertM162Issue(t, database, nil, nil, 1, "memory", "indexed")
	if err := migrateThrough(database, 162); err != nil {
		t.Fatalf("apply M162: %v", err)
	}

	tests := []struct {
		name, query, index string
		args               []any
	}{
		{"project", `EXPLAIN QUERY PLAN SELECT id FROM issues WHERE project_id=? AND type=? AND slug=? AND deleted_at IS NULL`, "idx_issues_knowledge_project_identity", []any{projectID, "memory", "indexed"}},
		{"user", `EXPLAIN QUERY PLAN SELECT id FROM issues WHERE project_id IS NULL AND user_id=? AND type=? AND slug=? AND deleted_at IS NULL`, "idx_issues_knowledge_user_identity", []any{userID, "memory", "indexed"}},
		{"instance", `EXPLAIN QUERY PLAN SELECT id FROM issues WHERE project_id IS NULL AND user_id IS NULL AND type=? AND slug=? AND deleted_at IS NULL`, "idx_issues_knowledge_instance_identity", []any{"memory", "indexed"}},
	}
	for _, test := range tests {
		rows, err := database.Query(test.query, test.args...)
		if err != nil {
			t.Fatal(err)
		}
		var detail strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var line string
			if err := rows.Scan(&id, &parent, &unused, &line); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			detail.WriteString(line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if !strings.Contains(detail.String(), test.index) {
			t.Fatalf("%s lookup plan=%q, want %s", test.name, detail.String(), test.index)
		}
	}
}

func TestMigration162IgnoresDeletedIdentityCollisions(t *testing.T) {
	database := openM162Fixture(t)
	_, _, userID, _ := seedM162Owners(t, database)

	for _, owner := range []any{userID, nil} {
		rowID := insertM162Issue(t, database, nil, owner, 1, "memory", "recreated")
		if _, err := database.Exec(`UPDATE issues SET deleted_at=datetime('now') WHERE id=?`, rowID); err != nil {
			t.Fatal(err)
		}
		insertM162Issue(t, database, nil, owner, 2, "memory", "recreated")
	}
	if err := migrateThrough(database, 162); err != nil {
		t.Fatalf("M162 rejected live+tombstone identities: %v", err)
	}
}
