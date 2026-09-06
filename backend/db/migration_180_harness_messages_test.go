// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import "testing"

func TestMigration180CreatesExactHumanWorkerReceiptsAtomically(t *testing.T) {
	database, _ := openM165Fixture(t, 179)
	if err := migrateThrough(database, 180); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 180); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
	for _, table := range []string{"harness_conversation_bindings", "harness_message_receipts"} {
		if !tableExists(t, database, table) {
			t.Fatalf("missing table %s", table)
		}
	}
	for _, trigger := range []string{
		"trg_harness_conversation_bindings_shape",
		"trg_harness_conversation_bindings_no_update",
		"trg_harness_message_receipts_shape",
		"trg_harness_message_receipts_no_update",
	} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil || count != 1 {
			t.Fatalf("trigger %s count=%d err=%v", trigger, count, err)
		}
	}
	var applied, violations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=180`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("M180 count=%d err=%v", applied, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
}
