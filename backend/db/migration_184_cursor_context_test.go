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

func TestMigration184AcceptsCursorContextAndUpgradesLiveFence(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		database := openMigrationDB(t, "m184-fresh.db")
		defer database.Close()
		if err := migrateThrough(database, 184); err != nil {
			t.Fatal(err)
		}
		assertCursorTriggerMembership(t, database, true)
		if err := insertCursorContextSession(t, database, "11111111-1111-4111-8111-111111111111", "operator-cursor"); err != nil {
			t.Fatalf("cursor_context class rejected: %v", err)
		}
		if err := insertCursorContextSession(t, database, "22222222-2222-4222-8222-222222222222", "cursor_context"); err == nil {
			t.Fatal("reserved cursor_context class stored as account_key")
		}
	})
	t.Run("v183-upgrade", func(t *testing.T) {
		database := openMigrationDB(t, "m184-upgrade.db")
		defer database.Close()
		if err := migrateThrough(database, 183); err != nil {
			t.Fatal(err)
		}
		assertCursorTriggerMembership(t, database, false)
		if err := insertCursorContextSession(t, database, "11111111-1111-4111-8111-111111111111", "operator-cursor"); err == nil {
			t.Fatal("v183 accepted cursor_context before M184")
		}
		if err := migrateThrough(database, 184); err != nil {
			t.Fatal(err)
		}
		assertCursorTriggerMembership(t, database, true)
		if err := insertCursorContextSession(t, database, "33333333-3333-4333-8333-333333333333", "operator-cursor"); err != nil {
			t.Fatalf("upgraded cursor_context class rejected: %v", err)
		}
		if err := insertCursorContextSession(t, database, "44444444-4444-4444-8444-444444444444", "cursor_context"); err == nil {
			t.Fatal("reserved cursor_context class stored as account_key after upgrade")
		}
	})
}

func openMigrationDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name)+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAIMOS_TEST_MODE", "1")
	return database
}

func assertCursorTriggerMembership(t *testing.T, database *sql.DB, upgraded bool) {
	t.Helper()
	var provenance, fence string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_harness_sessions_provenance_shape_insert'`).Scan(&provenance); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='consumer_delivery_fence'`).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	if upgraded {
		if !strings.Contains(provenance, "cursor_context") || !strings.Contains(fence, "agentd_cursor") {
			t.Fatalf("upgraded triggers missing Cursor membership provenance=%q fence=%q", provenance, fence)
		}
		return
	}
	if strings.Contains(provenance, "cursor_context") || strings.Contains(fence, "agentd_cursor") {
		t.Fatalf("v183 triggers already contained Cursor membership provenance=%q fence=%q", provenance, fence)
	}
}

func insertCursorContextSession(t *testing.T, database *sql.DB, id, key string) error {
	t.Helper()
	var projectID, agentID int64
	if err := database.QueryRow(`SELECT id FROM projects WHERE key='M184'`).Scan(&projectID); err != nil {
		projectResult, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M184','M184')`)
		if err != nil {
			t.Fatal(err)
		}
		projectID, _ = projectResult.LastInsertId()
		if _, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='worker'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("b", 64)
	_, err := database.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
		management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,
		workspace_path,workspace_identity,workspace_kind,workspace_mode,dispatch_profile_id,dispatch_profile_version,dispatch_model,dispatch_effort,account_label,account_key)
		VALUES(?,?,?,?,?,?,randomblob(32),randomblob(32),'managed','worker','none',0,1,0,0,0,'working',?,?,?,?,?,?,?,?,?,?)`,
		id, projectID, agentID, "worker", "cursor", "host-a", "/workspace/cursor", identity, "directory", "exclusive",
		"cursor-composer", "1", "composer-2.5", "default", "cursor_context", key)
	return err
}
