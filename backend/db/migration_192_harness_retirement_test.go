// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration192AddsDurableHarnessRetirementLedger(t *testing.T) {
	database := openTestDB(t)
	if !tableExists(t, database, "harness_session_retirements") {
		t.Fatal("M192 retirement ledger missing")
	}
	for _, column := range []string{
		"harness_session_revision", "requested_activity_sequence", "runtime_id",
		"runtime_generation", "session_generation", "request_key", "request_digest",
		"requested_by_user_id", "requested_session_credential_id", "state", "reason",
		"requested_at", "claimed_at", "stopping_at", "completed_at",
	} {
		if !columnExists(t, database, "harness_session_retirements", column) {
			t.Fatalf("M192 retirement column %s missing", column)
		}
	}
	for _, trigger := range []string{
		"trg_harness_session_retirement_identity",
		"trg_harness_session_retirement_transition",
		"trg_harness_session_retirement_no_delete",
		"trg_harness_retirement_binding_admission",
		"trg_harness_retirement_delivery_admission",
		"trg_harness_retirement_consumer_admission",
		"trg_harness_retirement_control_admission",
	} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil || count != 1 {
			t.Fatalf("M192 trigger %s count=%d err=%v", trigger, count, err)
		}
	}
	for _, index := range []string{"idx_harness_session_retirements_runtime", "idx_harness_session_retirements_active"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("M192 index %s count=%d err=%v", index, count, err)
		}
	}
}

func TestMigration192RejectsPartialRetirementSchemaWithoutChangingMain188(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m192-partial.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := migrateThrough(database, 187); err != nil {
		t.Fatalf("migrate through published main M187: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE harness_session_retirements(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create partial retirement schema: %v", err)
	}
	if err := migrateThrough(database, 188); err != nil {
		t.Fatalf("published main M188 acquired retirement precondition: %v", err)
	}
	if err := migrateThrough(database, 191); err != nil {
		t.Fatalf("migrate through additive M191: %v", err)
	}

	err = migrateThrough(database, 192)
	if err == nil || !strings.Contains(err.Error(), "migration 192 precondition failed") ||
		!strings.Contains(err.Error(), "M192 schema is partially present or locally incompatible: table:harness_session_retirements") {
		t.Fatalf("partial M192 was not rejected by its ownership precondition: %v", err)
	}
	if version, versionErr := CurrentSchemaVersion(database); versionErr != nil || version != 191 {
		t.Fatalf("partial M192 changed schema version=%d err=%v, want 191", version, versionErr)
	}
}
