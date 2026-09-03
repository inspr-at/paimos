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

func TestMigration169BackfillsTerminalTruthAndRejectsInconsistentActivity(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m169.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 168); err != nil {
		t.Fatal(err)
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M169','M169')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agent, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	activeID := "11111111-1111-4111-8111-111111111111"
	stoppedID := "22222222-2222-4222-8222-222222222222"
	for _, row := range []struct {
		id, phase string
	}{
		{activeID, "working"},
		{stoppedID, "stopped"},
	} {
		if _, err := database.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
		 VALUES(?,?,?,?,?,?||?,zeroblob(32),zeroblob(32),'managed','worker','none',0,1,0,0,0,?)`, row.id, projectID, agentID, "worker", "codex", "m169-", row.phase, row.phase); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateThrough(database, 169); err != nil {
		t.Fatal(err)
	}
	var state, reason, closed string
	if err := database.QueryRow(`SELECT activity_state,activity_reason,closed_reason FROM harness_sessions WHERE id=?`, stoppedID).Scan(&state, &reason, &closed); err != nil {
		t.Fatal(err)
	}
	if state != "dead" || reason != "ownership_lost" || closed != "ownership_lost" {
		t.Fatalf("terminal backfill state=%s reason=%s closed=%s", state, reason, closed)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET activity_state='idle',activity_reason='turn_completed',activity_event_kind='turn_completed' WHERE id=?`, stoppedID); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("stopped-as-idle error=%v", err)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET activity_state='busy',activity_reason='adapter_activity',activity_event_kind='session_started' WHERE id=?`, activeID); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("session-started-as-busy error=%v", err)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET activity_state='idle',activity_reason='turn_completed',activity_event_kind='turn_completed',activity_sequence=1,activity_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, activeID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'heartbeat','working','idle','turn_completed','turn_completed',1)`, activeID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE harness_session_events SET operation='yield' WHERE harness_session_id=?`, activeID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable event update error=%v", err)
	}
}
