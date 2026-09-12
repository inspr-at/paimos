// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration195AddsClosedMachineNotifierBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m195.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 194); err != nil {
		t.Fatalf("create exact M194 fixture: %v", err)
	}
	user, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('legacy-key','disabled','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	legacy, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'legacy','legacy-hash','legacy','*')`, userID)
	if err != nil {
		t.Fatal(err)
	}
	legacyID, _ := legacy.LastInsertId()

	if err := migrateThrough(database, 195); err != nil {
		t.Fatalf("M194 to M195: %v", err)
	}
	var kind string
	if err := database.QueryRow(`SELECT credential_kind FROM api_keys WHERE id=?`, legacyID).Scan(&kind); err != nil || kind != "general" {
		t.Fatalf("legacy credential kind=%q err=%v", kind, err)
	}
	if _, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,credential_kind)
		VALUES(?,'invalid','invalid-hash','invalid','*','future_kind')`, userID); err == nil {
		t.Fatal("unknown credential kind bypassed database constraint")
	}
	if _, err := database.Exec(`UPDATE api_keys SET credential_kind='machine_notifier' WHERE id=?`, legacyID); err == nil {
		t.Fatal("general credential changed kind")
	}

	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Notifier','MNT')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	sender, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'hostd59')`, projectID)
	receiver, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'amy')`, projectID)
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_message_allowlist(sender_agent_id,receiver_agent_id) VALUES(?,?)`, senderID, receiverID); err != nil {
		t.Fatal(err)
	}
	targetID := "4f73e08c-f98d-4dfd-a86c-6a9393f05db4"
	if _, err := database.Exec(`INSERT INTO agent_message_targets
		(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,target_secret_cipher,maximum_level,role,enabled,version)
		VALUES(?,'ppm',?,'grok_bot:amy','grok_bot_routine','https_webhook',zeroblob(48),zeroblob(48),'simple','primary',1,1)`, targetID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO machine_notifier_bindings
		(api_key_id,instance,project_id,sender_agent_id,receiver_agent_id,address,target_id,target_version)
		VALUES(?,'ppm',?,?,?,'grok_bot:amy',?,1)`, legacyID, projectID, senderID, receiverID, targetID); err == nil {
		t.Fatal("general credential acquired a machine notifier binding")
	}
	notifier, err := database.Exec(`INSERT INTO api_keys
		(user_id,name,key_hash,key_prefix,scopes,expires_at,credential_kind)
		VALUES(?,'notifier','notifier-hash','notifier','',strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 day'),'machine_notifier')`, userID)
	if err != nil {
		t.Fatal(err)
	}
	notifierID, _ := notifier.LastInsertId()
	if _, err := database.Exec(`INSERT INTO machine_notifier_bindings
		(api_key_id,instance,project_id,sender_agent_id,receiver_agent_id,address,target_id,target_version)
		VALUES(?,'ppm',?,?,?,'grok_bot:amy',?,1)`, notifierID, projectID, senderID, receiverID, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE machine_notifier_bindings SET target_version=2 WHERE api_key_id=?`, notifierID); err == nil {
		t.Fatal("machine notifier binding was mutable")
	}
	var triggerSQL string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_machine_notifier_no_target_recovery'`).Scan(&triggerSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(triggerSQL, "machine_notifier_api_key_id IS NOT NULL") {
		t.Fatalf("unexpected target-recovery guard: %q", triggerSQL)
	}
	var violations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign-key violations=%d err=%v", violations, err)
	}
}

func TestMigration195RefusesPartialLocalSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m195-partial.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 194); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE machine_notifier_bindings(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 195); err == nil || !strings.Contains(err.Error(), "M195 schema is partially present") {
		t.Fatalf("partial schema error=%v", err)
	}
}
