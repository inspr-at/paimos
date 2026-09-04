package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestM174AddsClosedRevisionedWorkShapeWithoutReclassifyingHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m173-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 173); err != nil {
		t.Fatalf("create exact M173 fixture: %v", err)
	}
	deliveryID, attemptID, _, _ := seedDeliverySchemaGraph(t, database)
	var deliveryPolicyBefore string
	if err := database.QueryRow(`SELECT group_concat(stage_key||':'||applicability||':'||weight,'|') FROM
		(SELECT stage_key,applicability,weight FROM delivery_attempt_stage_policy WHERE delivery_id=? AND attempt_id=? ORDER BY sort_order)`,
		deliveryID, attemptID).Scan(&deliveryPolicyBefore); err != nil {
		t.Fatal(err)
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Task shapes','SHP')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agent, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	ticket, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority) VALUES(?,1,'ticket','Legacy assignment','in-progress','medium')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	ticketID, _ := ticket.LastInsertId()
	const sessionID = "11111111-1111-4111-8111-111111111111"
	if _, err := database.Exec(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
		management_mode,role,ticket_id,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,
		workspace_path,workspace_identity,workspace_kind,workspace_mode,dispatch_profile_id,dispatch_profile_version,
		dispatch_model,dispatch_effort,account_label)
		VALUES(?,?,?,?,?, ?,zeroblob(32),zeroblob(32),'managed','worker',?,'none',0,1,0,0,0,'starting',
		'/workspace/pai907',?,'directory','exclusive','codex-sol-high','1','gpt-5.6-sol','high','chatgpt')`,
		sessionID, projectID, agentID, "worker", "codex", "host", ticketID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO harness_session_events(
		harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_sequence,
		before_ticket_id,after_ticket_id,assignment_present,created_at)
		VALUES(?,1,'register','starting','unknown','unreported',0,NULL,?,1,'2026-09-03T20:00:00.000Z')`, sessionID, ticketID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE sqlite_sequence SET seq=40 WHERE name='harness_session_events'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 174); err != nil {
		t.Fatalf("migrate exact M173 through M174: %v", err)
	}
	var deliveryPolicyAfter string
	if err := database.QueryRow(`SELECT group_concat(stage_key||':'||applicability||':'||weight,'|') FROM
		(SELECT stage_key,applicability,weight FROM delivery_attempt_stage_policy WHERE delivery_id=? AND attempt_id=? ORDER BY sort_order)`,
		deliveryID, attemptID).Scan(&deliveryPolicyAfter); err != nil || deliveryPolicyAfter != deliveryPolicyBefore {
		t.Fatalf("historical delivery policy changed: before=%q after=%q err=%v", deliveryPolicyBefore, deliveryPolicyAfter, err)
	}
	var storedShape, beforeShape, afterShape sql.NullString
	var createdAt string
	if err := database.QueryRow(`SELECT work_shape FROM harness_sessions WHERE id=?`, sessionID).Scan(&storedShape); err != nil || storedShape.Valid {
		t.Fatalf("legacy binding was classified: shape=%+v err=%v", storedShape, err)
	}
	var workspacePath, profileModel, profileEffort, accountLabel string
	if err := database.QueryRow(`SELECT workspace_path,dispatch_model,dispatch_effort,account_label FROM harness_sessions WHERE id=?`, sessionID).
		Scan(&workspacePath, &profileModel, &profileEffort, &accountLabel); err != nil || workspacePath != "/workspace/pai907" ||
		profileModel != "gpt-5.6-sol" || profileEffort != "high" || accountLabel != "chatgpt" {
		t.Fatalf("M172 provenance changed: path=%q model=%q effort=%q account=%q err=%v", workspacePath, profileModel, profileEffort, accountLabel, err)
	}
	var eventHighWater int64
	if err := database.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='harness_session_events'`).Scan(&eventHighWater); err != nil || eventHighWater != 40 {
		t.Fatalf("event high-water changed: seq=%d err=%v", eventHighWater, err)
	}
	if err := database.QueryRow(`SELECT before_work_shape,after_work_shape,created_at FROM harness_session_events WHERE harness_session_id=? AND event_sequence=1`, sessionID).
		Scan(&beforeShape, &afterShape, &createdAt); err != nil || beforeShape.Valid || afterShape.Valid || createdAt != "2026-09-03T20:00:00.000Z" {
		t.Fatalf("historical event changed: before=%+v after=%+v created=%q err=%v", beforeShape, afterShape, createdAt, err)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET work_shape='research',revision=revision+1 WHERE id=?`, sessionID); err == nil {
		t.Fatal("open-ended work shape was accepted")
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET work_shape='scout' WHERE id=?`, sessionID); err == nil || !strings.Contains(err.Error(), "requires revision") {
		t.Fatalf("shape update without revision err=%v", err)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET work_shape='scout',revision=revision+1 WHERE id=?`, sessionID); err != nil {
		t.Fatal(err)
	}
	var classificationEventID int64
	if err := database.QueryRow(`SELECT id,before_work_shape,after_work_shape FROM harness_session_events WHERE harness_session_id=? AND event_sequence=2`, sessionID).
		Scan(&classificationEventID, &beforeShape, &afterShape); err != nil || classificationEventID <= eventHighWater || beforeShape.Valid || !afterShape.Valid || afterShape.String != "scout" {
		t.Fatalf("classification event missing: before=%+v after=%+v err=%v", beforeShape, afterShape, err)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET work_shape='ship',revision=revision+1 WHERE id=?`, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT before_work_shape,after_work_shape FROM harness_session_events WHERE harness_session_id=? AND event_sequence=3`, sessionID).
		Scan(&beforeShape, &afterShape); err != nil || beforeShape.String != "scout" || afterShape.String != "ship" {
		t.Fatalf("promotion event missing: before=%+v after=%+v err=%v", beforeShape, afterShape, err)
	}
	if _, err := database.Exec(`UPDATE harness_session_events SET after_work_shape='scout' WHERE harness_session_id=? AND event_sequence=3`, sessionID); err == nil {
		t.Fatal("immutable assignment event was updated")
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET ticket_id=NULL,revision=revision+1 WHERE id=?`, sessionID); err == nil {
		t.Fatal("classified shape survived a detached ticket")
	}
}
