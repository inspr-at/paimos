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

func TestMigration183AcceptsPiContextClassAndRejectsItAsAccountKey(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m183.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 182); err != nil {
		t.Fatal(err)
	}
	projectResult, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M183','M183')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	if _, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 183); err != nil {
		t.Fatal(err)
	}
	var agentID int64
	if err := database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='worker'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("a", 64)
	insert := func(id, label, key string) error {
		_, err := database.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
			management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,
			workspace_path,workspace_identity,workspace_kind,workspace_mode,dispatch_profile_id,dispatch_profile_version,dispatch_model,dispatch_effort,account_label,account_key)
			VALUES(?,?,?,?,?,?,randomblob(32),randomblob(32),'managed','worker','none',0,1,0,0,0,'working',?,?,?,?,?,?,?,?,?,?)`,
			id, projectID, agentID, "worker", "pi", "host-a", "/workspace/pi", identity, "directory", "exclusive",
			"pi-anthropic-sonnet-high", "1", "anthropic:claude-sonnet-4-20250514", "high", label, key)
		return err
	}
	if err := insert("11111111-1111-4111-8111-111111111111", "pi_context", "operator-pi"); err != nil {
		t.Fatalf("pi_context class rejected: %v", err)
	}
	if err := insert("22222222-2222-4222-8222-222222222222", "pi_context", "pi_context"); err == nil {
		t.Fatal("reserved pi_context class stored as account_key")
	}
}
