// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration168RetiresUnboundGenerationsAndEnforcesLeaseDigest(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m168.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 167); err != nil {
		t.Fatal(err)
	}
	user, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m168','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M168','M168')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agent, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	sessionID := "11111111-1111-4111-8111-111111111111"
	if _, err := database.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
	 VALUES(?,?,?,?,?,?,zeroblob(32),'managed','worker','none',0,1,0,1,1,'working')`, sessionID, projectID, agentID, "worker", "codex", "legacy-host"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO harness_session_controls(id,harness_session_id,sequence,kind,requested_by_user_id) VALUES('22222222-2222-4222-8222-222222222222',?,1,'interrupt',?)`, sessionID, userID); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 168); err != nil {
		t.Fatal(err)
	}
	var phase, state, reason string
	if err := database.QueryRow(`SELECT phase FROM harness_sessions WHERE id=?`, sessionID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT state,reason FROM harness_session_controls WHERE harness_session_id=?`, sessionID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if phase != "stopped" || state != "rejected" || reason != "ownership_lost" {
		t.Fatalf("phase=%s state=%s reason=%s", phase, state, reason)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET worker_lease_digest=zeroblob(32) WHERE id=?`, sessionID); err == nil {
		t.Fatal("legacy row acquired a lease after migration")
	}
	if _, err := database.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop)
	 VALUES('33333333-3333-4333-8333-333333333333',?,?,?,?,?,zeroblob(32),'managed','worker','none',0,1,0,0,0)`, projectID, agentID, "worker", "codex", "new-host"); err == nil {
		t.Fatal("new unbound generation inserted")
	}
}
