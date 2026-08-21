// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

// PAI-742: /auth/totp/status reports sso_session so the SPA's local-2FA
// nag stays quiet for OIDC-minted sessions. Exercises the full chain:
// session row → Middleware context flag → handler response.

func ssoNagTestSetup(t *testing.T) (userID int64) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
	res, err := db.DB.Exec(
		`INSERT INTO users(username, password, role, status) VALUES('sso-user','','member','active')`)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userID, _ = res.LastInsertId()
	return userID
}

func plantSession(t *testing.T, id string, userID int64, viaOIDC int) {
	t.Helper()
	credentialID := "11111111-1111-4111-8111-111111111111"
	if viaOIDC != 0 {
		credentialID = "11111111-1111-4111-8111-111111111112"
	}
	if _, err := db.DB.Exec(
		`INSERT INTO sessions(id, user_id, expires_at, created_at, csrf_token, via_oidc, credential_id)
		 VALUES(?, ?, datetime('now', '+1 hour'), datetime('now'), 'tok', ?, ?)`,
		id, userID, viaOIDC, credentialID); err != nil {
		t.Fatalf("plant session: %v", err)
	}
}

func totpStatusVia(t *testing.T, sessionID string) map[string]bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/totp/status", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: sessionID})
	rec := httptest.NewRecorder()
	auth.Middleware(http.HandlerFunc(auth.TOTPStatus)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("totp status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestTOTPStatus_SSOSessionFlag(t *testing.T) {
	userID := ssoNagTestSetup(t)
	plantSession(t, "sess-password-1234567890", userID, 0)
	plantSession(t, "sess-oidc-1234567890abcd", userID, 1)

	pw := totpStatusVia(t, "sess-password-1234567890")
	if pw["sso_session"] {
		t.Fatal("password session reported sso_session=true — the nag would wrongly disappear")
	}
	if pw["enabled"] {
		t.Fatal("fixture user has no TOTP; enabled must be false")
	}

	sso := totpStatusVia(t, "sess-oidc-1234567890abcd")
	if !sso["sso_session"] {
		t.Fatal("OIDC session reported sso_session=false — the nag would keep showing (PAI-742 regression)")
	}
}
