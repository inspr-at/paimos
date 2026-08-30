package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func openKnowledgeScopeM161(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m162.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := enableWALMode(database); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 161); err != nil {
		t.Fatalf("migrate through M161: %v", err)
	}
	return database
}

func seedM162Owner(t *testing.T, database *sql.DB) (int64, int64) {
	t.Helper()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M162 project','M162')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	user, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m162-user','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	return projectID, userID
}

func seedM162Memory(t *testing.T, database *sql.DB, projectID, userID any, number int, slug, title string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO issues(project_id,user_id,issue_number,type,title,description,status,priority,slug,category_metadata)
		VALUES(?,?,?,'memory',?,'same body','backlog','medium',?,'{}')`, projectID, userID, number, title, slug)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestMigration162DeduplicatesOnlySafeScopeIdentities(t *testing.T) {
	database := openKnowledgeScopeM161(t)
	projectID, userID := seedM162Owner(t, database)

	instanceKeep := seedM162Memory(t, database, nil, nil, 1, "shared", "same")
	seedM162Memory(t, database, nil, nil, 2, "shared", "same")
	userKeep := seedM162Memory(t, database, nil, userID, 1, "personal", "same")
	seedM162Memory(t, database, nil, userID, 2, "personal", "same")
	otherUser, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m162-other','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	otherUserID, _ := otherUser.LastInsertId()
	seedM162Memory(t, database, nil, otherUserID, 1, "personal", "same")

	if err := migrateThrough(database, 162); err != nil {
		t.Fatalf("apply M162: %v", err)
	}
	for index, predicate := range map[string]string{
		"idx_issues_knowledge_identity_project":  "WHERE project_id IS NOT NULL AND slug IS NOT NULL",
		"idx_issues_knowledge_identity_user":     "WHERE project_id IS NULL AND user_id IS NOT NULL AND slug IS NOT NULL",
		"idx_issues_knowledge_identity_instance": "WHERE project_id IS NULL AND user_id IS NULL AND slug IS NOT NULL",
	} {
		var definition string
		if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&definition); err != nil {
			t.Fatalf("load %s: %v", index, err)
		}
		if !strings.Contains(definition, predicate) {
			t.Fatalf("%s definition %q missing %q", index, definition, predicate)
		}
	}
	var legacyIndex int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_issues_type_slug_project'`).Scan(&legacyIndex); err != nil || legacyIndex != 0 {
		t.Fatalf("legacy knowledge identity index remains count=%d err=%v", legacyIndex, err)
	}
	for label, tc := range map[string]struct {
		query  string
		args   []any
		wantID int64
	}{
		"instance": {`SELECT id FROM issues WHERE project_id IS NULL AND user_id IS NULL AND type='memory' AND slug='shared'`, nil, instanceKeep},
		"user":     {`SELECT id FROM issues WHERE project_id IS NULL AND user_id=? AND type='memory' AND slug='personal'`, []any{userID}, userKeep},
	} {
		var gotID int64
		if err := database.QueryRow(tc.query, tc.args...).Scan(&gotID); err != nil || gotID != tc.wantID {
			t.Fatalf("%s survivor id=%d err=%v want=%d", label, gotID, err, tc.wantID)
		}
	}

	assertRejected := func(label, statement string, args ...any) {
		t.Helper()
		if _, err := database.Exec(statement, args...); err == nil {
			t.Fatalf("%s unexpectedly accepted", label)
		}
	}
	if _, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority,slug) VALUES(?,1,'memory','one','backlog','medium','project')`, projectID); err != nil {
		t.Fatalf("valid project knowledge rejected: %v", err)
	}
	assertRejected("duplicate project identity", `INSERT INTO issues(project_id,issue_number,type,title,status,priority,slug) VALUES(?,2,'memory','two','backlog','medium','project')`, projectID)
	assertRejected("duplicate user identity", `INSERT INTO issues(user_id,issue_number,type,title,status,priority,slug) VALUES(?,3,'memory','two','backlog','medium','personal')`, userID)
	assertRejected("duplicate instance identity", `INSERT INTO issues(issue_number,type,title,status,priority,slug) VALUES(3,'memory','two','backlog','medium','shared')`)
	assertRejected("mixed ownership insert", `INSERT INTO issues(project_id,user_id,issue_number,type,title,status) VALUES(?,?,3,'memory','mixed','backlog')`, projectID, userID)
	assertRejected("unsupported user type insert", `INSERT INTO issues(user_id,issue_number,type,title,status) VALUES(?,4,'runbook','bad','backlog')`, userID)
	assertRejected("mixed ownership update", `UPDATE issues SET project_id=? WHERE id=?`, projectID, userKeep)
	assertRejected("unsupported user type update", `UPDATE issues SET type='guideline' WHERE id=?`, userKeep)
}

func TestMigration162StopsBeforeMutationOnDivergentCollision(t *testing.T) {
	database := openKnowledgeScopeM161(t)
	_, userID := seedM162Owner(t, database)
	first := seedM162Memory(t, database, nil, userID, 1, "ambiguous", "first")
	second := seedM162Memory(t, database, nil, userID, 2, "ambiguous", "second")

	err := migrateThrough(database, 162)
	if err == nil {
		t.Fatal("expected divergent collision to stop M162")
	}
	message := err.Error()
	for _, want := range []string{"migration 162 precondition failed", fmt.Sprintf("scope=user:%d", userID), `type="memory"`, `slug="ambiguous"`, fmt.Sprintf("ids=[%d,%d]", first, second)} {
		if !strings.Contains(message, want) {
			t.Fatalf("collision error %q missing %q", message, want)
		}
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM issues WHERE id IN (?,?)`, first, second).Scan(&count); err != nil || count != 2 {
		t.Fatalf("precondition mutated collision count=%d err=%v", count, err)
	}
}

func TestMigration162StopsBeforeDeletingReferencedDuplicate(t *testing.T) {
	database := openKnowledgeScopeM161(t)
	_, userID := seedM162Owner(t, database)
	seedM162Memory(t, database, nil, userID, 1, "referenced", "same")
	duplicate := seedM162Memory(t, database, nil, userID, 2, "referenced", "same")
	if _, err := database.Exec(`INSERT INTO issue_history(issue_id,snapshot) VALUES(?, '{}')`, duplicate); err != nil {
		t.Fatal(err)
	}

	err := migrateThrough(database, 162)
	if err == nil || !strings.Contains(err.Error(), "issue_history.issue_id") {
		t.Fatalf("expected actionable reference stop, got %v", err)
	}
	var exists int
	if err := database.QueryRow(`SELECT EXISTS(SELECT 1 FROM issues WHERE id=?)`, duplicate).Scan(&exists); err != nil || exists != 1 {
		t.Fatalf("referenced duplicate was mutated exists=%d err=%v", exists, err)
	}
}

func TestMigration162StopsBeforeDeletingRelatedDuplicate(t *testing.T) {
	database := openKnowledgeScopeM161(t)
	projectID, userID := seedM162Owner(t, database)
	seedM162Memory(t, database, nil, userID, 1, "related", "same")
	duplicate := seedM162Memory(t, database, nil, userID, 2, "related", "same")
	target := seedM162Memory(t, database, projectID, nil, 1, "target", "target")
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'related')`, duplicate, target); err != nil {
		t.Fatal(err)
	}

	err := migrateThrough(database, 162)
	if err == nil || !strings.Contains(err.Error(), "issue_relations.source_id") {
		t.Fatalf("expected actionable relation stop, got %v", err)
	}
	var exists int
	if err := database.QueryRow(`SELECT EXISTS(SELECT 1 FROM issues WHERE id=?)`, duplicate).Scan(&exists); err != nil || exists != 1 {
		t.Fatalf("related duplicate was mutated exists=%d err=%v", exists, err)
	}
}

func TestMigration162SemanticPolicyTracksIssueColumns(t *testing.T) {
	database := openTestDB(t)
	excluded := map[string]bool{
		"id": true, "project_id": true, "user_id": true, "issue_number": true,
		"type": true, "slug": true, "created_at": true, "updated_at": true,
		"content_rev": true,
	}
	rows, err := database.Query(`SELECT name FROM pragma_table_info('issues') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		if !excluded[column] && !strings.Contains(m162KnowledgeSemanticEquality, "a."+column+" IS b."+column) {
			t.Errorf("durable issues column %q is absent from M162 semantic equality policy", column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
