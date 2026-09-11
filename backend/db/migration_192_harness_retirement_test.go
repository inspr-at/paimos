// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import "testing"

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
