// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openM165Fixture(t *testing.T, through int) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m165.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, through); err != nil {
		t.Fatalf("migrate through M%d: %v", through, err)
	}
	return database, path
}

func seedM165ProjectAgent(t *testing.T, database *sql.DB, projectKey, agentKey string) (int64, int64) {
	t.Helper()
	projectResult, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES(?,?,'active')`, projectKey, projectKey)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	agentResult, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, agentKey)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agentResult.LastInsertId()
	return projectID, agentID
}

func TestMigration165CreatesOnlyUnsetSingletonAfterM164(t *testing.T) {
	database, path := openM165Fixture(t, 162)
	_, _ = seedM165ProjectAgent(t, database, "PPM", "amy")
	if err := migrateThrough(database, 165); err != nil {
		t.Fatalf("apply M165: %v", err)
	}
	var singletonID, revision int64
	var agentID, label, updater, updatedAt any
	if err := database.QueryRow(`SELECT singleton_id,project_agent_id,display_label,revision,updated_by_user_id,updated_at FROM instance_orchestrator`).
		Scan(&singletonID, &agentID, &label, &revision, &updater, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if singletonID != 1 || revision != 0 || agentID != nil || label != nil || updater != nil || updatedAt != nil {
		t.Fatalf("initial singleton=(%d,%v,%v,%d,%v,%v), want exact unset", singletonID, agentID, label, revision, updater, updatedAt)
	}
	var m164, m165 int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=164`).Scan(&m164); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=165`).Scan(&m165); err != nil {
		t.Fatal(err)
	}
	if m164 != 1 || m165 != 1 {
		t.Fatalf("schema versions M164=%d M165=%d", m164, m165)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := migrateThrough(reopened, 165); err != nil {
		t.Fatalf("reopen M165: %v", err)
	}
	if err := reopened.QueryRow(`SELECT revision FROM instance_orchestrator WHERE singleton_id=1`).Scan(&revision); err != nil || revision != 0 {
		t.Fatalf("reopened singleton revision=%d err=%v", revision, err)
	}
	var violations int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign_key_check=%d err=%v", violations, err)
	}
}

func TestMigration165EnforcesAssignmentDeletionAndAuditInvariants(t *testing.T) {
	database, _ := openM165Fixture(t, 165)
	projectID, agentID := seedM165ProjectAgent(t, database, "ORC", "amy")
	userResult, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m165-root','x','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	now := "2026-08-31T12:00:00.000Z"
	if _, err := database.Exec(`UPDATE instance_orchestrator SET project_agent_id=?,display_label='Amy',revision=1,updated_by_user_id=?,updated_at=? WHERE singleton_id=1`, agentID, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO instance_orchestrator_events(operation,actor_user_id,request_id,before_revision,after_revision,
		after_project_agent_id,after_project_id,after_key,after_display_label,created_at)
		VALUES('set',?,'m165-test',0,1,?,?,?,'Amy',?)`, userID, agentID, projectID, "amy", now); err != nil {
		t.Fatal(err)
	}
	assertRejected := func(name, query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	assertRejected("second singleton", `INSERT INTO instance_orchestrator(singleton_id) VALUES(2)`)
	assertRejected("mutated revision without timestamp", `UPDATE instance_orchestrator SET revision=2,updated_at=NULL WHERE singleton_id=1`)
	assertRejected("partial assignment", `UPDATE instance_orchestrator SET display_label=NULL WHERE singleton_id=1`)
	assertRejected("assigned agent delete", `DELETE FROM project_agents WHERE id=?`, agentID)
	assertRejected("assigned project physical delete", `DELETE FROM projects WHERE id=?`, projectID)
	assertRejected("assigned project soft delete", `UPDATE projects SET status='deleted' WHERE id=?`, projectID)
	assertRejected("audit update", `UPDATE instance_orchestrator_events SET request_id='changed' WHERE after_revision=1`)
	assertRejected("audit delete", `DELETE FROM instance_orchestrator_events WHERE after_revision=1`)
	assertRejected("nonconsecutive event", `INSERT INTO instance_orchestrator_events(operation,actor_user_id,request_id,before_revision,after_revision,
		before_project_agent_id,before_project_id,before_key,before_display_label)
		VALUES('clear',?,'bad-gap',2,3,?,?,?,'Amy')`, userID, agentID, projectID, "amy")
}

func TestMigration165BlocksOnlyLiveAssignedHarnessRename(t *testing.T) {
	database, _ := openM165Fixture(t, 165)
	projectID, agentID := seedM165ProjectAgent(t, database, "HRS", "amy")
	if _, err := database.Exec(`UPDATE instance_orchestrator SET project_agent_id=?,display_label='Amy',revision=1,
		updated_at='2026-08-31T12:00:00.000Z' WHERE singleton_id=1`, agentID); err != nil {
		t.Fatal(err)
	}
	insertHarness := func(id, phase string) {
		t.Helper()
		if _, err := database.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,
			management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
			VALUES(?,?,?,?,?,'host',?, 'managed','worker','none',1,1,0,0,0,?)`,
			id, projectID, agentID, "amy", "codex", make([]byte, 32), phase); err != nil {
			t.Fatalf("insert harness: %v", err)
		}
	}
	insertHarness("11111111-1111-4111-8111-111111111111", "working")
	if _, err := database.Exec(`UPDATE project_agents SET name='amelia' WHERE id=?`, agentID); err == nil {
		t.Fatal("assigned live harness rename was accepted")
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET phase='stopped' WHERE project_agent_id=?`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE project_agents SET name='amelia' WHERE id=?`, agentID); err != nil {
		t.Fatalf("stopped harness rename rejected: %v", err)
	}
}
