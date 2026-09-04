// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/secretvault"
)

func TestMessageTargetAuthorityReauthorizesDemotionAndRevocationInsideMutation(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	secretvault.ResetForTest()
	t.Cleanup(secretvault.ResetForTest)
	openChangesTestDB(t)

	userResult, err := db.DB.Exec(`INSERT INTO users(username,password,role,status,role_key,is_super_admin)
		VALUES('transaction-admin','disabled','admin','active','super_admin',1)`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	projectID := seedChangesProject(t, "TAT")
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'amy'),(?,'worker')`, projectID, projectID); err != nil {
		t.Fatal(err)
	}
	var amyID int64
	if err := db.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&amyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE instance_orchestrator SET project_agent_id=?,display_label='Amy',revision=1,
		updated_by_user_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE singleton_id=1`, amyID, userID); err != nil {
		t.Fatal(err)
	}
	service := agentmessage.NewService(db.DB)
	if _, err := service.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: uuid.NewString(), MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}

	credentialID, sessionID := uuid.NewString(), uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,datetime('now','+1 hour'),datetime('now'),?)`, sessionID, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	principal, err := auth.NewSessionPrincipal(credentialID, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/1/message-targets", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), principal))

	if _, err := db.DB.Exec(`UPDATE users SET role_key='admin',is_super_admin=0 WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	_, err = service.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: uuid.NewString(), MaximumLevel: "simple", Role: "primary",
		Authority: messageTargetAuthority(request),
	})
	var codedErr *agentmessage.CodedError
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_attention_target_forbidden" {
		t.Fatalf("demoted target error=%v", err)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_targets WHERE project_id=? AND address='codex:amy'`, projectID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("protected target versions=%d err=%v", count, err)
	}

	if _, err := db.DB.Exec(`DELETE FROM sessions WHERE credential_id=?`, credentialID); err != nil {
		t.Fatal(err)
	}
	_, err = service.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: uuid.NewString(), MaximumLevel: "simple", Role: "primary",
		Authority: messageTargetAuthority(request),
	})
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_message_unauthorized" {
		t.Fatalf("revoked target error=%v", err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_targets WHERE project_id=? AND address='codex:worker'`, projectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked target versions=%d err=%v", count, err)
	}
}

func TestMessageTargetAuthorityRejectsDemotedMember(t *testing.T) {
	// The callback is intentionally tested against a transaction: middleware's
	// earlier user snapshot cannot preserve administrator authority.
	openChangesTestDB(t)
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status,role_key,is_super_admin)
		VALUES('demoted-member','disabled','member','active','member',0)`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	credentialID, sessionID := uuid.NewString(), uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,datetime('now','+1 hour'),datetime('now'),?)`, sessionID, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	principal, err := auth.NewSessionPrincipal(credentialID, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
	tx, err := db.DB.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = messageTargetAuthority(request)(context.Background(), tx)
	var codedErr *agentmessage.CodedError
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_message_forbidden" {
		t.Fatalf("demoted authority error=%v", err)
	}
}

func TestHarnessRegistrationAuthorityPreservesAndRechecksProjectEditor(t *testing.T) {
	openChangesTestDB(t)
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status,role_key,is_super_admin)
		VALUES('current-editor','disabled','external','active','external',0)`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	projectID := seedChangesProject(t, "HRA")
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, userID, projectID); err != nil {
		t.Fatal(err)
	}
	credentialID, sessionID := uuid.NewString(), uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,datetime('now','+1 hour'),datetime('now'),?)`, sessionID, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	principal, err := auth.NewSessionPrincipal(credentialID, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
	check := harnessRegistrationAuthority(request, projectID)
	tx, err := db.DB.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	super, err := check(context.Background(), tx)
	if err != nil || super {
		t.Fatalf("current editor super=%t err=%v", super, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='viewer' WHERE user_id=? AND project_id=?`, userID, projectID); err != nil {
		t.Fatal(err)
	}
	tx, err = db.DB.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = check(context.Background(), tx)
	var codedErr *agentmessage.CodedError
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_message_forbidden" {
		t.Fatalf("revoked editor error=%v", err)
	}
}
