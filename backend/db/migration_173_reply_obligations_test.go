// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration173AddsClosedReplyAndResolutionLedgers(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m173.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 171); err != nil {
		t.Fatal(err)
	}
	project, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('M173','M173')`)
	projectID, _ := project.LastInsertId()
	_, _ = database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender'),(?,'receiver')`, projectID, projectID)
	var senderID, receiverID int64
	_ = database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='sender'`, projectID).Scan(&senderID)
	_ = database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='receiver'`, projectID).Scan(&receiverID)
	message, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,is_action_request,delivered,
		held_reason,delivered_at,message_id,context_id,role,parts_json,metadata_json,from_address,to_address,thread_id)
		VALUES(?,?,'legacy',0,1,'',strftime('%Y-%m-%dT%H:%M:%fZ','now'),'11111111-1111-4111-8111-111111111173',
		'M173','agent','[]','{}','paimos:sender','codex:receiver','11111111-1111-4111-8111-111111111173')`, senderID, receiverID)
	if err != nil {
		t.Fatal(err)
	}
	messageRowID, _ := message.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?,'codex:receiver',?,'held_agent_message','legacy-source',0,'held_action','action_request_held',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, receiverID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_attention_batches(batch_id,receiver_project_id,receiver_project_agent_id,address,
		from_cursor,to_cursor,item_count,state) VALUES('22222222-2222-4222-8222-222222222173',?,?, 'codex:receiver',0,1,1,'pending')`,
		projectID, receiverID); err != nil {
		t.Fatal(err)
	}
	transientProject, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M173 transient','M173-TRANSIENT')`)
	if err != nil {
		t.Fatal(err)
	}
	transientProjectID, _ := transientProject.LastInsertId()
	transientAgent, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'transient')`, transientProjectID)
	if err != nil {
		t.Fatal(err)
	}
	transientAgentID, _ := transientAgent.LastInsertId()
	for _, sourceID := range []string{"expired-1", "expired-2", "expired-3", "expired-4", "expired-5", "expired-6", "expired-7", "expired-8"} {
		if _, err := database.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
			source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
			VALUES(?,?,'codex:transient',?,'held_agent_message',?,0,'held_action','action_request_held',
			strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, transientProjectID, transientAgentID, transientProjectID, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	var priorSequence, survivingMaxID int64
	if err := database.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='agent_attention_items'`).Scan(&priorSequence); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM projects WHERE id=?`, transientProjectID); err != nil {
		t.Fatalf("cascade historical attention rows: %v", err)
	}
	if err := database.QueryRow(`SELECT COALESCE(MAX(id),0) FROM agent_attention_items`).Scan(&survivingMaxID); err != nil {
		t.Fatal(err)
	}
	if priorSequence <= survivingMaxID {
		t.Fatalf("test setup lacks deleted high-water: sequence=%d surviving max=%d", priorSequence, survivingMaxID)
	}
	if err := migrateThrough(database, 173); err != nil {
		t.Fatal(err)
	}
	var expects, attentionRows int
	if err := database.QueryRow(`SELECT expects_reply FROM agent_messages WHERE id=?`, messageRowID).Scan(&expects); err != nil || expects != 0 {
		t.Fatalf("legacy expects_reply=%d err=%v", expects, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM agent_attention_items WHERE source_id='legacy-source'`).Scan(&attentionRows); err != nil || attentionRows != 1 {
		t.Fatalf("M171 attention rows=%d err=%v", attentionRows, err)
	}
	var batchState string
	if err := database.QueryRow(`SELECT state FROM agent_attention_batches WHERE batch_id='22222222-2222-4222-8222-222222222173'`).Scan(&batchState); err != nil || batchState != "pending" {
		t.Fatalf("M171 attention batch state=%q err=%v", batchState, err)
	}
	if _, err := database.Exec(`UPDATE agent_attention_batches SET state='superseded',superseded_at='2030-01-01T00:00:00.000Z'
		WHERE batch_id='22222222-2222-4222-8222-222222222173'`); err != nil {
		t.Fatalf("superseded terminal state rejected: %v", err)
	}
	newAttention, err := database.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?,'codex:receiver',?,'reply_obligation','11111111-1111-4111-8111-111111111173',1,'reply_overdue','reply_expected',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, receiverID, projectID)
	if err != nil {
		t.Fatalf("new attention vocabulary rejected: %v", err)
	}
	newAttentionID, err := newAttention.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if newAttentionID <= priorSequence {
		t.Fatalf("rebuilt attention sequence regressed: new id=%d prior high-water=%d", newAttentionID, priorSequence)
	}
	if _, err := database.Exec(`UPDATE agent_attention_items SET reason_code='reply_expected' WHERE source_id='legacy-source'`); err == nil {
		t.Fatal("rebuilt attention item lost immutability")
	}
	if _, err := database.Exec(`INSERT INTO agent_reply_obligations(message_row_id,project_id,sender_agent_id,next_attention_at)
		VALUES(?,?,?,'2030-01-01T00:00:00.000Z')`, messageRowID, projectID, senderID); err == nil {
		t.Fatal("legacy message without expects_reply accepted an obligation")
	}
	expected, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,is_action_request,delivered,
		held_reason,delivered_at,message_id,context_id,role,parts_json,metadata_json,from_address,to_address,thread_id,expects_reply)
		VALUES(?,?,'expected',0,1,'',strftime('%Y-%m-%dT%H:%M:%fZ','now'),'33333333-3333-4333-8333-333333333173',
		'M173','agent','[]','{}','paimos:sender','codex:receiver','33333333-3333-4333-8333-333333333173',1)`, senderID, receiverID)
	if err != nil {
		t.Fatal(err)
	}
	expectedRowID, _ := expected.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_reply_obligations(message_row_id,project_id,sender_agent_id,next_attention_at)
		VALUES(?,?,?,'2030-01-01T00:00:00.000Z')`, expectedRowID, projectID, senderID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_reply_obligations SET next_attention_at=NULL WHERE message_row_id=?`, expectedRowID); err == nil {
		t.Fatal("open obligation became quiet before the terminal resurface")
	}
	if _, err := database.Exec(`UPDATE agent_reply_obligations SET resurface_count=7 WHERE message_row_id=?`, expectedRowID); err == nil {
		t.Fatal("reply obligation exceeded the finite resurface bound")
	}
	var version int
	if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil || version != 173 {
		t.Fatalf("version=%d err=%v", version, err)
	}
}
