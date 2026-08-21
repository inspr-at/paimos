package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration146UpgradesPopulatedM145AndPreservesOrdinaryComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m146-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 145); err != nil {
		t.Fatalf("create exact M145 fixture: %v", err)
	}
	userID, issueID := seedM146CommentFixture(t, database)
	result, err := database.Exec(`INSERT INTO comments(issue_id,author_id,body,visibility)
		VALUES(?,?, 'ordinary before M146','internal')`, issueID, userID)
	if err != nil {
		t.Fatal(err)
	}
	commentID, _ := result.LastInsertId()

	if err := migrate(database); err != nil {
		t.Fatalf("M145 to M146: %v", err)
	}
	if !columnExists(t, database, "comments", "client_request_id") {
		t.Fatal("M146 comments.client_request_id missing")
	}
	var body string
	var requestID sql.NullString
	if err := database.QueryRow(`SELECT body,client_request_id FROM comments WHERE id=?`, commentID).
		Scan(&body, &requestID); err != nil {
		t.Fatal(err)
	}
	if body != "ordinary before M146" || requestID.Valid {
		t.Fatalf("legacy comment changed: body=%q client_request_id=%+v", body, requestID)
	}
	var indexSQL string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index'
		AND name='idx_comments_author_client_request'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexSQL, "author_id,client_request_id") || !strings.Contains(indexSQL, "client_request_id IS NOT NULL") {
		t.Fatalf("M146 exact-once index=%q", indexSQL)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("M146 restart: %v", err)
	}
}

func TestMigration146ClientRequestIdentityIsStructurallyInternalAndExactOnce(t *testing.T) {
	database := openTestDB(t)
	firstUser, issueID := seedM146CommentFixture(t, database)
	second, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('m146-second','hash','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	secondUser, _ := second.LastInsertId()

	insert := func(author any, visibility, key string) error {
		_, err := database.Exec(`INSERT INTO comments(issue_id,author_id,body,visibility,client_request_id)
			VALUES(?,?,'confirmed note',?,?)`, issueID, author, visibility, key)
		return err
	}
	if err := insert(firstUser, "internal", "voice.note:123_ABC-def"); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	if err := insert(firstUser, "internal", "voice.note:123_ABC-def"); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("same-author duplicate identity error=%v", err)
	}
	if err := insert(secondUser, "internal", "voice.note:123_ABC-def"); err != nil {
		t.Fatalf("different author should have an independent identity namespace: %v", err)
	}
	for name, invalid := range map[string]struct {
		author     any
		visibility string
		key        string
	}{
		"anonymous":          {nil, "internal", "anonymous-key"},
		"external":           {firstUser, "external", "external-key"},
		"empty":              {firstUser, "internal", ""},
		"whitespace":         {firstUser, "internal", "space key"},
		"unicode":            {firstUser, "internal", "voice-ä"},
		"control":            {firstUser, "internal", "voice\nkey"},
		"over-byte-boundary": {firstUser, "internal", strings.Repeat("a", 129)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := insert(invalid.author, invalid.visibility, invalid.key); err == nil {
				t.Fatal("invalid direct write passed M146 CHECK")
			}
		})
	}

	var keyed int
	if err := database.QueryRow(`SELECT COUNT(*) FROM comments WHERE client_request_id IS NOT NULL`).Scan(&keyed); err != nil {
		t.Fatal(err)
	}
	if keyed != 2 {
		t.Fatalf("keyed comments=%d want 2 valid per-author rows", keyed)
	}
}

func seedM146CommentFixture(t *testing.T, database *sql.DB) (int64, int64) {
	t.Helper()
	user, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('m146-author','hash','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('M146 fixture','M146','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket','M146 exact once','backlog')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	return userID, issueID
}
