// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"crypto/sha256"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

func insertGDPRUser(t *testing.T, username string) int64 {
	t.Helper()
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES(?,'x','member','active')`, username)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestGDPRControlExportUsesSafeCredentialsAndEraseRevokesImpersonation(t *testing.T) {
	ts := newTestServer(t)
	actorID := insertGDPRUser(t, "gdpr-control-actor")
	targetID := insertGDPRUser(t, "gdpr-control-target")
	unrelatedID := insertGDPRUser(t, "gdpr-control-unrelated")
	if _, err := db.DB.Exec(`UPDATE users SET role='admin',is_super_admin=1 WHERE id=?`, actorID); err != nil {
		t.Fatal(err)
	}
	rawBearer := "RAW-BEARER-sk-live-control-export-canary"
	rawCSRF := "RAW-CSRF-provider-control-export-canary"
	rawOperationKey := "PRIVATE-IDEMPOTENCY-" + strings.Repeat("K", 108) // exact 128-byte canary
	credentialID := "89898989-8989-4989-8989-898989898989"
	if _, err := db.DB.Exec(`INSERT INTO sessions(
		id,user_id,actor_user_id,acting_as_user_id,expires_at,created_at,csrf_token,credential_id)
		VALUES(?,?,?,?,'2026-12-01 00:00:00','2026-08-01 00:00:00',?,?)`,
		rawBearer, actorID, actorID, targetID, rawCSRF, credentialID); err != nil {
		t.Fatal(err)
	}
	keyDigest := sha256.Sum256([]byte(rawOperationKey))
	requestDigest := sha256.Sum256([]byte("gdpr-control-request"))
	resultDigest := sha256.Sum256([]byte("gdpr-control-result"))
	if _, err := db.DB.Exec(`INSERT INTO control_operation_keys(
		actor_user_id,user_id,principal_kind,actor_session_credential_id,operation_kind,
		operation_key_digest,request_digest,result_digest,command_id)
		VALUES(?,?,'session',?,'command.create',?,?,?,'90909090-9090-4090-8090-909090909090')`,
		actorID, actorID, credentialID, keyDigest[:], requestDigest[:], resultDigest[:]); err != nil {
		t.Fatal(err)
	}

	export := func(userID int64) string {
		t.Helper()
		resp := ts.get(t, "/api/users/"+itoa(userID)+"/gdpr-export", ts.adminCookie)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("export user %d: status=%d body=%s", userID, resp.StatusCode, body)
		}
		return string(body)
	}

	actorExport := export(actorID)
	targetExport := export(targetID)
	unrelatedExport := export(unrelatedID)
	for label, body := range map[string]string{"actor": actorExport, "target": targetExport, "unrelated": unrelatedExport} {
		if strings.Contains(body, rawBearer) || strings.Contains(body, rawCSRF) || strings.Contains(body, rawOperationKey) {
			t.Fatalf("%s export leaked bearer/provider/idempotency secret", label)
		}
	}
	if !strings.Contains(actorExport, credentialID) || !strings.Contains(targetExport, credentialID) {
		t.Fatal("actor/impersonated-target exports omitted the safe session credential")
	}
	if strings.Contains(unrelatedExport, credentialID) {
		t.Fatal("unrelated subject inherited another principal's session")
	}
	if !strings.Contains(actorExport, "control_operation_keys") || !strings.Contains(actorExport, "command.create") {
		t.Fatal("control actor history was omitted from GDPR export")
	}
	active := ts.get(t, "/api/auth/me", "session="+rawBearer)
	active.Body.Close()
	if active.StatusCode != http.StatusOK {
		t.Fatalf("setup impersonated credential=%d, want 200", active.StatusCode)
	}

	resp := ts.post(t, "/api/users/"+itoa(targetID)+"/gdpr-erase", ts.adminCookie, map[string]string{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("erase impersonated target: %d", resp.StatusCode)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE credential_id=?`, credentialID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("erasing impersonated target retained the actor credential")
	}
	denied := ts.get(t, "/api/auth/me", "session="+rawBearer)
	denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("erased target's old impersonated cookie=%d, want 401", denied.StatusCode)
	}
}

func TestGDPREraseRollsBackWhenCredentialRevocationFails(t *testing.T) {
	ts := newTestServer(t)
	targetID := insertGDPRUser(t, "gdpr-rollback-target")
	credentialID := "91919191-9191-4191-8191-919191919191"
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('gdpr-rollback-bearer',?,'2026-12-01 00:00:00','2026-08-01 00:00:00',?)`, targetID, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER test_block_gdpr_session_delete BEFORE DELETE ON sessions
		WHEN OLD.credential_id='` + credentialID + `' BEGIN SELECT RAISE(ABORT,'blocked credential revoke'); END`); err != nil {
		t.Fatal(err)
	}

	resp := ts.post(t, "/api/users/"+itoa(targetID)+"/gdpr-erase", ts.adminCookie, map[string]string{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("erase with blocked credential revoke=%d, want 500", resp.StatusCode)
	}
	var status, username string
	if err := db.DB.QueryRow(`SELECT status,username FROM users WHERE id=?`, targetID).Scan(&status, &username); err != nil {
		t.Fatal(err)
	}
	if status != "active" || username != "gdpr-rollback-target" {
		t.Fatalf("failed erase partially anonymized principal: status=%q username=%q", status, username)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE credential_id=?`, credentialID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("failed erase did not retain credential transactionally")
	}
}

func TestGDPREraseRollsBackWhenAPIKeyRevocationFails(t *testing.T) {
	ts := newTestServer(t)
	targetID := insertGDPRUser(t, "gdpr-key-rollback-target")
	result, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'gdpr-key',?,'paimos_gdpr','*')`, targetID, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	if _, err := db.DB.Exec(`CREATE TRIGGER test_block_gdpr_key_delete BEFORE DELETE ON api_keys
		WHEN OLD.id=` + itoa(keyID) + ` BEGIN SELECT RAISE(ABORT,'blocked key revoke'); END`); err != nil {
		t.Fatal(err)
	}

	resp := ts.post(t, "/api/users/"+itoa(targetID)+"/gdpr-erase", ts.adminCookie, map[string]string{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("erase with blocked key revoke=%d, want 500", resp.StatusCode)
	}
	var status, username string
	if err := db.DB.QueryRow(`SELECT status,username FROM users WHERE id=?`, targetID).Scan(&status, &username); err != nil {
		t.Fatal(err)
	}
	if status != "active" || username != "gdpr-key-rollback-target" {
		t.Fatalf("failed key erase partially anonymized principal: status=%q username=%q", status, username)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE id=?`, keyID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("failed erase did not retain API key transactionally")
	}
}
