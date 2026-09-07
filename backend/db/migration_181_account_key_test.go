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

func TestMigration181AddsImmutableOpaqueAccountKey(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m181.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 180); err != nil {
		t.Fatal(err)
	}
	projectResult, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M181','M181')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	for _, name := range []string{"legacy", "one", "two", "three"} {
		if _, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateThrough(database, 181); err != nil {
		t.Fatal(err)
	}
	insert := func(id, name, extraColumns, extraValues string, values ...any) error {
		var agentID int64
		if err := database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name=?`, projectID, name).Scan(&agentID); err != nil {
			return err
		}
		query := `INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
			management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase` + extraColumns + `)
			VALUES(?,?,?,?,?,?,randomblob(32),randomblob(32),'managed','worker','none',0,1,0,0,0,'working'` + extraValues + `)`
		args := append([]any{id, projectID, agentID, name, "codex", "host-a"}, values...)
		_, err := database.Exec(query, args...)
		return err
	}
	if err := insert("11111111-1111-4111-8111-111111111111", "legacy", "", ""); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := database.QueryRow(`SELECT account_key FROM harness_sessions WHERE id=?`, "11111111-1111-4111-8111-111111111111").Scan(&key); err != nil || key != "" {
		t.Fatalf("legacy default account_key=%q err=%v", key, err)
	}
	identity := strings.Repeat("a", 64)
	columns := `,workspace_path,workspace_identity,workspace_kind,workspace_mode,dispatch_profile_id,dispatch_profile_version,dispatch_model,dispatch_effort,account_label,account_key`
	values := `,?,?,?,?,?,?,?,?,?,?`
	if err := insert("22222222-2222-4222-8222-222222222222", "one", columns, values, "/workspace/one", identity, "directory", "exclusive", "codex-sol-high", "1", "gpt-5.6-sol", "high", "chatgpt", "coordinator"); err != nil {
		t.Fatal(err)
	}
	if err := insert("33333333-3333-4333-8333-333333333333", "two", columns, values, "/workspace/two", strings.Repeat("b", 64), "directory", "exclusive", "codex-sol-high", "1", "gpt-5.6-sol", "high", "chatgpt", "/tmp/codex"); err == nil {
		t.Fatal("path account key was accepted")
	}
	if err := insert("44444444-4444-4444-8444-444444444444", "three", columns, values, "/workspace/three", strings.Repeat("c", 64), "directory", "exclusive", "codex-sol-high", "1", "gpt-5.6-sol", "high", "chatgpt", "chatgpt"); err == nil {
		t.Fatal("class label stored as account key")
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET account_key='other' WHERE id='22222222-2222-4222-8222-222222222222'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("account_key mutation error=%v", err)
	}
}
