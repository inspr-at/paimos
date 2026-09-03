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

func TestMigration171AttentionLedgerIsClosedAndImmutable(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m171.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 171); err != nil {
		t.Fatal(err)
	}
	for _, object := range []string{"agent_attention_items", "agent_attention_cursors", "agent_attention_batches", "idx_agent_attention_batches_open"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("object %s count=%d err=%v", object, count, err)
		}
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Attention','ATTN')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agent, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'amy')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?,'codex:amy',?,'harness_session_event','fixture',1,'worker_unknown','heartbeat_stale',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, agentID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_attention_items SET reason_code='stale_evidence'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("update err=%v", err)
	}
	if _, err := database.Exec(`DELETE FROM agent_attention_items`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("delete err=%v", err)
	}
	if _, err := database.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?,'codex:amy',?,'future_source','fixture-2',1,'worker_unknown','heartbeat_stale',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, agentID, projectID); err == nil {
		t.Fatal("unknown attention source was accepted")
	}
	if _, err := database.Exec(`DELETE FROM projects WHERE id=?`, projectID); err != nil {
		t.Fatalf("project cascade was blocked by immutable attention item: %v", err)
	}
	var remaining int
	if err := database.QueryRow(`SELECT COUNT(*) FROM agent_attention_items`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("attention cascade remaining=%d err=%v", remaining, err)
	}
}
