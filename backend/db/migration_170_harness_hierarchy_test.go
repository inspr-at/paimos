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

func TestMigration170PreservesActivityHistoryAndGuardsBindingHistory(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m170.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 169); err != nil {
		t.Fatal(err)
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M170','M170')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	for _, name := range []string{"parent", "child"} {
		if _, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
			t.Fatal(err)
		}
	}
	var parentAgentID, childAgentID int64
	if err := database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='parent'`, projectID).Scan(&parentAgentID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='child'`, projectID).Scan(&childAgentID); err != nil {
		t.Fatal(err)
	}
	ticket, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,903,'ticket','Hierarchy','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	ticketID, _ := ticket.LastInsertId()
	parentID := "11111111-1111-4111-8111-111111111111"
	childID := "22222222-2222-4222-8222-222222222222"
	for _, row := range []struct {
		id, agent string
		agentID   int64
	}{
		{parentID, "parent", parentAgentID},
		{childID, "child", childAgentID},
	} {
		if _, err := database.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
			management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
			VALUES(?,?,?,?,?,?,zeroblob(32),zeroblob(32),'managed','worker','none',0,1,0,0,0,'working')`, row.id, projectID, row.agentID, row.agent, "codex", "m170-"+row.agent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'register','working','unknown','unreported','',0)`, childID); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 170); err != nil {
		t.Fatal(err)
	}
	var operation string
	var oldParent sql.NullString
	if err := database.QueryRow(`SELECT operation,before_parent_harness_session_id FROM harness_session_events WHERE harness_session_id=?`, childID).Scan(&operation, &oldParent); err != nil {
		t.Fatal(err)
	}
	if operation != "register" || oldParent.Valid {
		t.Fatalf("M169 history changed: operation=%s before_parent=%+v", operation, oldParent)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET parent_harness_session_id=?,ticket_id=? WHERE id=?`, parentID, ticketID, childID); err == nil || !strings.Contains(err.Error(), "requires revision") {
		t.Fatalf("binding without CAS error=%v", err)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET parent_harness_session_id=?,ticket_id=?,revision=revision+1 WHERE id=?`, parentID, ticketID, childID); err != nil {
		t.Fatal(err)
	}
	var afterParent string
	var afterTicket int64
	if err := database.QueryRow(`SELECT operation,after_parent_harness_session_id,after_ticket_id FROM harness_session_events
		WHERE harness_session_id=? AND event_sequence=2`, childID).Scan(&operation, &afterParent, &afterTicket); err != nil {
		t.Fatal(err)
	}
	if operation != "binding_changed" || afterParent != parentID || afterTicket != ticketID {
		t.Fatalf("binding history mismatch: operation=%s parent=%s ticket=%d", operation, afterParent, afterTicket)
	}
	if _, err := database.Exec(`DELETE FROM harness_session_events WHERE harness_session_id=? AND event_sequence=2`, childID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("binding event delete error=%v", err)
	}
}
