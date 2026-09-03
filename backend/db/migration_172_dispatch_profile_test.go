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

func TestMigration172PersistsImmutableProvenanceAndGuardsWorkspaceOwnership(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m172.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := migrateThrough(database, 170); err != nil {
		t.Fatal(err)
	}
	projectResult, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M172','M172')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	for _, name := range []string{"legacy", "one", "two", "three"} {
		if _, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateThrough(database, 172); err != nil {
		t.Fatal(err)
	}
	var legacyAgent int64
	if err := database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='legacy'`, projectID).Scan(&legacyAgent); err != nil {
		t.Fatal(err)
	}
	insertBase := func(id, name, host string, extraColumns, extraValues string, values ...any) error {
		var agentID int64
		if err := database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name=?`, projectID, name).Scan(&agentID); err != nil {
			return err
		}
		query := `INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
			management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase` + extraColumns + `)
			VALUES(?,?,?,?,?,?,randomblob(32),randomblob(32),'managed','worker','none',0,1,0,0,0,'working'` + extraValues + `)`
		args := []any{id, projectID, agentID, name, "codex", host}
		args = append(args, values...)
		_, err := database.Exec(query, args...)
		return err
	}
	if err := insertBase("11111111-1111-4111-8111-111111111111", "legacy", "host-a", "", ""); err != nil {
		t.Fatal(err)
	}
	var kind, mode, account string
	if err := database.QueryRow(`SELECT workspace_kind,workspace_mode,account_label FROM harness_sessions WHERE project_agent_id=?`, legacyAgent).Scan(&kind, &mode, &account); err != nil || kind != "unknown" || mode != "unknown" || account != "unknown" {
		t.Fatalf("legacy defaults kind=%q mode=%q account=%q err=%v", kind, mode, account, err)
	}
	columns := `,workspace_path,workspace_identity,workspace_kind,workspace_mode,dispatch_profile_id,dispatch_profile_version,dispatch_model,dispatch_effort,account_label`
	values := `,?,?,?,?,?,?,?,?,?`
	identity := strings.Repeat("a", 64)
	profileArgs := []any{"/workspace/one", identity, "directory", "exclusive", "codex-sol-high", "1", "gpt-5.6-sol", "high", "chatgpt"}
	if err := insertBase("22222222-2222-4222-8222-222222222222", "one", "host-a", columns, values, profileArgs...); err != nil {
		t.Fatal(err)
	}
	if err := insertBase("33333333-3333-4333-8333-333333333333", "two", "host-a", columns, values, "/workspace/two", identity, "directory", "shared", "", "", "", "", "unknown"); err == nil || !strings.Contains(err.Error(), "ownership conflicts") {
		t.Fatalf("exclusive/shared collision error=%v", err)
	}
	if _, err := database.Exec(`UPDATE harness_sessions SET dispatch_model='different' WHERE id='22222222-2222-4222-8222-222222222222'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("profile mutation error=%v", err)
	}
	sharedIdentity := strings.Repeat("b", 64)
	for _, row := range []struct{ id, name string }{{"44444444-4444-4444-8444-444444444444", "two"}, {"55555555-5555-4555-8555-555555555555", "three"}} {
		if err := insertBase(row.id, row.name, "host-a", columns, values, "/workspace/"+row.name, sharedIdentity, "directory", "shared", "", "", "", "", "unknown"); err != nil {
			t.Fatalf("shared registration %s: %v", row.name, err)
		}
	}
}
