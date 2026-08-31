// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestStructuredKnowledgeValidatorFlagsEssayChatAndProjectLocalDuplicates(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE issues(
		id INTEGER PRIMARY KEY,project_id INTEGER,type TEXT,slug TEXT,title TEXT,description TEXT,deleted_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO issues VALUES(1,42,'memory','same','Release invariant','Keep the exact release invariant stable and reviewable',NULL)`,
		`INSERT INTO issues VALUES(2,43,'memory','foreign','Release invariant','Keep the exact release invariant stable and reviewable',NULL)`,
		`INSERT INTO issues VALUES(3,42,'ticket',NULL,'Not knowledge','Keep the exact release invariant stable and reviewable',NULL)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	body := "Amy: Keep the exact release invariant stable and reviewable\nUser: agreed"
	validation, err := structuredKnowledgeValidationForTx(context.Background(), tx, 42, 0,
		"Release invariant", body, 32)
	if err != nil {
		t.Fatal(err)
	}
	wantFlags := map[string]bool{"essay": true, "chat_note_prose": true, "likely_duplicate": true}
	for _, flag := range validation.Flags {
		delete(wantFlags, flag)
	}
	if len(wantFlags) != 0 {
		t.Fatalf("missing validator flags: %v from %+v", wantFlags, validation)
	}
	if len(validation.LikelyDuplicateIDs) != 1 || validation.LikelyDuplicateIDs[0] != 1 {
		t.Fatalf("duplicate IDs=%v, want same-project knowledge only", validation.LikelyDuplicateIDs)
	}
}

func TestStructuredKnowledgeValidatorDoesNotGuessFromShortGenericText(t *testing.T) {
	if score := tokenJaccard(knowledgeTokens("same short note"), knowledgeTokens("same short note")); score != 0 {
		t.Fatalf("short generic token score=%v, want zero", score)
	}
	for _, body := range []string{"Amy: one speaker label", "A concise operational fact."} {
		if looksLikeChatNote(body) {
			t.Fatalf("concise body misclassified as chat: %q", body)
		}
	}
}
