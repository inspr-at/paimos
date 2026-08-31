// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package db

import (
	"database/sql"
	"testing"
)

func TestMigration164PreservesPopulatedLedgerAndEnforcesHumanShapes(t *testing.T) {
	database, path := openM165Fixture(t, 163)
	projectID, senderID := seedM165ProjectAgent(t, database, "M164", "sender")
	_, receiverID := seedM165ProjectAgentInProject(t, database, projectID, "receiver")
	userResult, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m164-human','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	targetID := "m164-target"
	if _, err := database.Exec(`INSERT INTO agent_message_targets(
		id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES(?, 'test', ?, 'codex:receiver', 'codex', 'codex_thread', zeroblob(29), 'simple', 'primary', 1, 1)`, targetID, projectID); err != nil {
		t.Fatal(err)
	}
	legacyResult, err := database.Exec(`INSERT INTO agent_messages(
		from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,role,parts_json,
		from_address,to_address,thread_id,delivery_primary_target_id)
		VALUES(?,?,'legacy body',1,'2026-08-31T10:00:00Z','legacy-m164','M164','agent',
		 json_array(json_object('kind','text','text','legacy body')),'codex:sender','codex:receiver','legacy-thread',?)`,
		senderID, receiverID, targetID)
	if err != nil {
		t.Fatal(err)
	}
	legacyRowID, _ := legacyResult.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_message_deliveries(
		delivery_id,message_row_id,instance,primary_target_id,requested_level,state)
		VALUES('legacy-delivery',?,'test',?,'simple','pending')`, legacyRowID, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_message_idempotency(
		instance,project_id,sender_agent_id,key_digest,request_digest,message_row_id)
		VALUES('test',?,?,zeroblob(32),zeroblob(32),?)`, projectID, senderID, legacyRowID); err != nil {
		t.Fatal(err)
	}
	agentSession := "17e5d8f7-0b11-4bee-a8a4-a11406de865a"
	paimosSession := "27e5d8f7-0b11-4bee-a8a4-a11406de865a"
	if _, err := database.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'project_agent',?,'Agent conversation',?,?),(?,?,'paimos',NULL,'Paimos conversation',?,?)`,
		agentSession, projectID, receiverID, userID, userID, paimosSession, projectID, userID, userID); err != nil {
		t.Fatal(err)
	}

	if err := migrateThrough(database, 164); err != nil {
		t.Fatalf("apply M164: %v", err)
	}
	var body, messageID, deliveryID string
	if err := database.QueryRow(`SELECT body,message_id FROM agent_messages WHERE id=?`, legacyRowID).Scan(&body, &messageID); err != nil {
		t.Fatal(err)
	}
	if body != "legacy body" || messageID != "legacy-m164" {
		t.Fatalf("legacy message changed body=%q message_id=%q", body, messageID)
	}
	if err := database.QueryRow(`SELECT delivery_id FROM agent_message_deliveries WHERE message_row_id=?`, legacyRowID).Scan(&deliveryID); err != nil || deliveryID != "legacy-delivery" {
		t.Fatalf("legacy delivery=%q err=%v", deliveryID, err)
	}
	var idempotencyRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM agent_message_idempotency WHERE message_row_id=?`, legacyRowID).Scan(&idempotencyRows); err != nil || idempotencyRows != 1 {
		t.Fatalf("legacy idempotency rows=%d err=%v", idempotencyRows, err)
	}

	humanAgentResult, err := database.Exec(`INSERT INTO agent_messages(
		from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,role,parts_json,
		from_address,to_address,thread_id,delivery_primary_target_id,from_user_id,product_session_id)
		VALUES(NULL,?,'human to agent',1,'2026-08-31T10:01:00.000Z',?,'M164','human',
		 json_array(json_object('kind','text','text','human to agent')),?,?,?, ?,?,?)`, receiverID,
		"37e5d8f7-0b11-4bee-a8a4-a11406de865a", "user:1", "codex:receiver", agentSession,
		targetID, userID, agentSession)
	if err != nil {
		t.Fatalf("valid human-to-agent row: %v", err)
	}
	humanAgentRowID, _ := humanAgentResult.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_message_deliveries(
		delivery_id,message_row_id,instance,primary_target_id,requested_level,state)
		VALUES('47e5d8f7-0b11-4bee-a8a4-a11406de865a',?,'test',?,'simple','pending')`, humanAgentRowID, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO session_utterance_receipts(
		instance,project_id,user_id,utterance_id,request_digest,message_row_id,product_session_id,
		product_session_revision,delivery_id,created_at)
		VALUES('test',?,?,'utt_0123456789abcdef0123456789abcdeZ',zeroblob(32),?,?,1,
		 '47e5d8f7-0b11-4bee-a8a4-a11406de865a','2026-08-31T10:01:00.000Z')`, projectID, userID, humanAgentRowID, agentSession); err == nil {
		t.Fatal("receipt accepted a non-lowercase-hex utterance id")
	}
	if _, err := database.Exec(`INSERT INTO session_utterance_receipts(
		instance,project_id,user_id,utterance_id,request_digest,message_row_id,product_session_id,
		product_session_revision,delivery_id,created_at)
		VALUES('test',?,?,'utt_0123456789abcdef0123456789abcdef',zeroblob(32),?,?,1,
		 '47e5d8f7-0b11-4bee-a8a4-a11406de865a','2026-08-31T10:01:00.000Z')`, projectID, userID, humanAgentRowID, agentSession); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_message_targets(
		id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('m164-other-target','test',?,'codex:other','codex','codex_thread',zeroblob(29),'simple','primary',1,1)`, projectID); err != nil {
		t.Fatal(err)
	}
	mismatchedResult, err := database.Exec(`INSERT INTO agent_messages(
		from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,role,parts_json,
		from_address,to_address,thread_id,delivery_primary_target_id,from_user_id,product_session_id)
		VALUES(NULL,?,'mismatched delivery target',1,'2026-08-31T10:01:01.000Z',?,'M164','human',
		 json_array(json_object('kind','text','text','mismatched delivery target')),?,?,?, ?,?,?)`, receiverID,
		"47e5d8f7-0b11-4bee-a8a4-a11406de865b", "user:1", "codex:receiver", agentSession,
		targetID, userID, agentSession)
	if err != nil {
		t.Fatalf("human message for delivery-target mismatch: %v", err)
	}
	mismatchedRowID, _ := mismatchedResult.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_message_deliveries(
		delivery_id,message_row_id,instance,primary_target_id,requested_level,state)
		VALUES('47e5d8f7-0b11-4bee-a8a4-a11406de865b',?,'test','m164-other-target','simple','pending')`, mismatchedRowID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO session_utterance_receipts(
		instance,project_id,user_id,utterance_id,request_digest,message_row_id,product_session_id,
		product_session_revision,delivery_id,created_at)
		VALUES('test',?,?,'utt_1123456789abcdef0123456789abcdef',zeroblob(32),?,?,1,
		 '47e5d8f7-0b11-4bee-a8a4-a11406de865b','2026-08-31T10:01:01.000Z')`, projectID, userID, mismatchedRowID, agentSession); err == nil {
		t.Fatal("receipt accepted a delivery target that disagreed with the message target snapshot")
	}
	if _, err := database.Exec(`INSERT INTO agent_messages(
		from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,role,parts_json,
		from_address,to_address,thread_id,from_user_id,product_session_id)
		VALUES(NULL,NULL,'human to Paimos',1,'2026-08-31T10:02:00.000Z',?,'M164','human',
		 json_array(json_object('kind','text','text','human to Paimos')),'user:1','paimos',?,?,?)`,
		"57e5d8f7-0b11-4bee-a8a4-a11406de865a", paimosSession, userID, paimosSession); err != nil {
		t.Fatalf("valid human-to-Paimos row: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO agent_messages(
		from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,role,parts_json,
		from_address,to_address,thread_id,delivery_primary_target_id,from_user_id,product_session_id)
		VALUES(NULL,?,'mismatch',1,'2026-08-31T10:03:00.000Z',?,'M164','human','[]',
		 'user:1','codex:receiver',?,?,?,?)`, receiverID,
		"67e5d8f7-0b11-4bee-a8a4-a11406de865a", paimosSession, targetID, userID, paimosSession); err == nil {
		t.Fatal("human message accepted a destination that disagreed with its product session")
	}
	if _, err := database.Exec(`INSERT INTO agent_messages(
		from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,role,parts_json,
		from_address,to_address,thread_id,delivery_primary_target_id,from_user_id,product_session_id)
		VALUES(NULL,?,'spoofed target',1,'2026-08-31T10:04:00.000Z',?,'M164','human','[]',
		 'user:1','other:receiver',?,?,?,?)`, receiverID,
		"77e5d8f7-0b11-4bee-a8a4-a11406de865a", agentSession, targetID, userID, agentSession); err == nil {
		t.Fatal("human message accepted an address that disagreed with its delivery target")
	}
	var violations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign_key_check=%d err=%v", violations, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := migrateThrough(reopened, 166); err != nil {
		t.Fatalf("reopen through current schema: %v", err)
	}
}

func seedM165ProjectAgentInProject(t *testing.T, database *sql.DB, projectID int64, agentKey string) (int64, int64) {
	t.Helper()
	result, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, agentKey)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := result.LastInsertId()
	return projectID, agentID
}
