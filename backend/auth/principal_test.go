// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/pquerna/otp/totp"
)

const testSessionCredential = "12345678-1234-4234-9234-123456789abc"

func setupPrincipalTestDB(t *testing.T) {
	t.Helper()
	authLimiter.mux.Lock()
	authLimiter.entries = map[string]rateLimitEntry{}
	authLimiter.mux.Unlock()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			_ = db.DB.Close()
			db.DB = nil
		}
	})
}

func sessionCookieWasCleared(rec *httptest.ResponseRecorder) bool {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookie && cookie.MaxAge < 0 && cookie.Expires.Before(time.Now()) {
			return true
		}
	}
	return false
}

func assertSessionCookieCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if sessionCookieWasCleared(rec) {
		return
	}
	t.Fatalf("response did not expire the session cookie: %v", rec.Result().Cookies())
}

func insertPrincipalUser(t *testing.T, username string) int64 {
	t.Helper()
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES(?, 'x', 'member', 'active')`, username)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("user id: %v", err)
	}
	return id
}

func insertPrincipalSession(t *testing.T, userID int64, credentialID string, createdAt, expiresAt time.Time) {
	t.Helper()
	_, err := db.DB.Exec(`
		INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,?,?,?)
	`, "bearer-cookie-secret", userID, expiresAt.UTC().Format("2006-01-02 15:04:05"),
		createdAt.UTC().Format("2006-01-02 15:04:05"), credentialID)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func insertPrincipalAPIKey(t *testing.T, userID int64, scopes string, expiresAt *time.Time) int64 {
	t.Helper()
	var nextID int64
	if err := db.DB.QueryRow(`SELECT COALESCE(MAX(id),0)+1 FROM api_keys`).Scan(&nextID); err != nil {
		t.Fatalf("next api key id: %v", err)
	}
	var expiry any
	if expiresAt != nil {
		expiry = expiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	result, err := db.DB.Exec(`
		INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,expires_at)
		VALUES(?, 'principal-test', ?, 'paimos_test', ?, ?)
	`, userID, fmt.Sprintf("%064x", nextID), scopes, expiry)
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("api key id: %v", err)
	}
	return id
}

func TestPrincipalIsImmutableTypedAndContextSafe(t *testing.T) {
	for _, test := range []struct {
		actor, effective int64
		impersonated     bool
	}{
		{actor: 7, effective: 7, impersonated: true},
		{actor: 7, effective: 8, impersonated: false},
	} {
		if _, err := NewSessionPrincipal(testSessionCredential, test.actor, test.effective, test.impersonated); err == nil {
			t.Fatalf("inconsistent impersonation actor=%d effective=%d impersonated=%v was accepted",
				test.actor, test.effective, test.impersonated)
		}
	}
	session, err := NewSessionPrincipal(testSessionCredential, 7, 7, false)
	if err != nil {
		t.Fatalf("NewSessionPrincipal: %v", err)
	}
	if session.Kind() != PrincipalSession || session.SafeCredentialID() != testSessionCredential ||
		session.ActorUserID() != 7 || session.UserID() != 7 || session.Impersonated() || !session.HasScope(ScopeAll) {
		t.Fatalf("unexpected session principal: %#v", session)
	}

	scopes := ScopeSet{"issues:read": {}}
	apiKey, err := NewAPIKeyPrincipal(42, 9, scopes)
	if err != nil {
		t.Fatalf("NewAPIKeyPrincipal: %v", err)
	}
	delete(scopes, "issues:read")
	if !apiKey.HasScope("issues:read") || apiKey.SafeCredentialID() != "42" {
		t.Fatal("constructor did not isolate scopes or preserve safe key identity")
	}
	copyOfScopes := apiKey.Scopes()
	delete(copyOfScopes, "issues:read")
	if !apiKey.HasScope("issues:read") {
		t.Fatal("scope accessor leaked mutable storage")
	}

	ctx := WithPrincipal(context.Background(), apiKey)
	fromContext, ok := PrincipalFromContext(ctx)
	if !ok || fromContext.APIKeyID() != 42 || !fromContext.HasScope("issues:read") {
		t.Fatalf("context principal missing: ok=%v principal=%#v", ok, fromContext)
	}
	delete(fromContext.scopes, "issues:read")
	again, ok := PrincipalFromContext(ctx)
	if !ok || !again.HasScope("issues:read") {
		t.Fatal("context extraction leaked mutable scope storage")
	}

	malformed := []Principal{
		{},
		{kind: PrincipalSession, sessionCredentialID: "raw-bearer-cookie", actorUserID: 1, userID: 1, scopes: ScopeSet{ScopeAll: {}}},
		{kind: PrincipalSession, sessionCredentialID: testSessionCredential, apiKeyID: 3, actorUserID: 1, userID: 1, scopes: ScopeSet{ScopeAll: {}}},
		{kind: PrincipalAPIKey, apiKeyID: 3, actorUserID: 1, userID: 2},
		{kind: PrincipalAPIKey, sessionCredentialID: testSessionCredential, apiKeyID: 3, actorUserID: 1, userID: 1},
	}
	for i, principal := range malformed {
		if principal.SafeCredentialID() != "" {
			t.Fatalf("malformed principal %d exposed a credential", i)
		}
		malformedContext := context.WithValue(context.Background(), principalKey, principal)
		if _, ok := PrincipalFromContext(malformedContext); ok {
			t.Fatalf("malformed principal %d entered request context", i)
		}
		base := context.Background()
		if got := WithPrincipal(base, principal); got != base {
			t.Fatalf("malformed principal %d was stored", i)
		}
	}
}

func TestReauthorizeSessionPrincipalTxEnforcesCurrentIdentityAndLifetime(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	userID := insertPrincipalUser(t, "session-principal")
	insertPrincipalSession(t, userID, testSessionCredential, now.Add(-time.Hour), now.Add(time.Hour))
	principal, err := NewSessionPrincipal(testSessionCredential, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	user, current, err := ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if err != nil || user.ID != userID || current.SafeCredentialID() != testSessionCredential {
		t.Fatalf("valid session was not reauthorized: user=%#v principal=%#v err=%v", user, current, err)
	}

	if _, err := db.DB.Exec(`UPDATE sessions SET expires_at=? WHERE credential_id=?`, now.Format("2006-01-02 15:04:05"), testSessionCredential); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("session remained valid at exact expiry: %v", err)
	}

	if _, err := db.DB.Exec(`DELETE FROM sessions WHERE credential_id=?`, testSessionCredential); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("missing session reauthorized: %v", err)
	}
	insertPrincipalSession(t, userID, testSessionCredential,
		now.Add(-sessionAbsoluteLifetime), now.Add(time.Hour))
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("session survived exact absolute lifetime: %v", err)
	}
}

func TestReauthorizeSessionPrincipalTxRejectsInvalidTimestamps(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	userID := insertPrincipalUser(t, "session-time-boundaries")
	principal, err := NewSessionPrincipal(testSessionCredential, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}

	insertRaw := func(createdAt, expiresAt string) {
		t.Helper()
		if _, err := db.DB.Exec(`DELETE FROM sessions WHERE credential_id=?`, testSessionCredential); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(`
			INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
			VALUES('bearer-cookie-secret',?,?,?,?)
		`, userID, expiresAt, createdAt, testSessionCredential); err != nil {
			t.Fatalf("insert session timestamp fixture: %v", err)
		}
	}
	reauthorize := func() error {
		t.Helper()
		tx, err := db.DB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, _, err = ReauthorizePrincipalTx(context.Background(), tx, principal, now)
		return err
	}

	for _, test := range []struct {
		name      string
		createdAt string
		expiresAt string
	}{
		{
			name:      "future created at",
			createdAt: now.Add(time.Second).Format("2006-01-02 15:04:05"),
			expiresAt: now.Add(time.Hour).Format("2006-01-02 15:04:05"),
		},
		{
			name:      "malformed created at",
			createdAt: "not-a-session-created-at",
			expiresAt: now.Add(time.Hour).Format("2006-01-02 15:04:05"),
		},
		{
			name:      "malformed expires at",
			createdAt: now.Add(-time.Hour).Format("2006-01-02 15:04:05"),
			expiresAt: "not-a-session-expiry",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			insertRaw(test.createdAt, test.expiresAt)
			if err := reauthorize(); !errors.Is(err, ErrCredentialUnavailable) {
				t.Fatalf("invalid session timestamp reauthorized: %v", err)
			}
		})
	}
}

func TestReauthorizeAPIKeyPrincipalTxUsesCurrentCredentialState(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	userID := insertPrincipalUser(t, "key-principal")
	expires := now.Add(time.Hour)
	keyID := insertPrincipalAPIKey(t, userID, "issues:read", &expires)
	principal, err := NewAPIKeyPrincipal(keyID, userID, ScopeSet{"stale:scope": {}})
	if err != nil {
		t.Fatal(err)
	}

	tx, _ := db.DB.BeginTx(context.Background(), nil)
	user, current, err := ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if err != nil || user.ID != userID || !current.HasScope("issues:read") || current.HasScope("stale:scope") {
		t.Fatalf("current scopes were not re-resolved: user=%#v principal=%#v err=%v", user, current, err)
	}

	if _, err := db.DB.Exec(`UPDATE api_keys SET expires_at=? WHERE id=?`, now.Format("2006-01-02T15:04:05.000Z"), keyID); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("API key remained valid at exact expiry: %v", err)
	}

	// A fresh key is denied immediately when disabled and when deleted.
	keyID = insertPrincipalAPIKey(t, userID, "issues:read", nil)
	principal, _ = NewAPIKeyPrincipal(keyID, userID, ScopeSet{ScopeAll: {}})
	if _, err := db.DB.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("disabled API key reauthorized: %v", err)
	}
	if _, err := db.DB.Exec(`DELETE FROM api_keys WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("deleted API key reauthorized: %v", err)
	}
}

type failAfterReader struct {
	remaining int
}

func (reader *failAfterReader) Read(p []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := len(p)
	if n > reader.remaining {
		n = reader.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = byte(i + 1)
	}
	reader.remaining -= n
	return n, nil
}

func TestCreateSessionFailsClosedWhenCredentialRandomnessFails(t *testing.T) {
	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "rng-failure")
	previous := sessionRandomReader
	sessionRandomReader = &failAfterReader{remaining: 16}
	t.Cleanup(func() { sessionRandomReader = previous })

	now := time.Now().UTC()
	sessionID, err := createSession(context.Background(), userID, now, now.Add(time.Hour), false, false)
	if err == nil || sessionID != "" {
		t.Fatalf("credential RNG failure returned session=%q err=%v", sessionID, err)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=?`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("credential RNG failure persisted %d session rows", count)
	}
}

func TestCreateSessionMintsIndependentCanonicalCredential(t *testing.T) {
	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "session-mint")
	now := time.Now().UTC()
	sessionID, err := createSession(context.Background(), userID, now, now.Add(time.Hour), false, false)
	if err != nil {
		t.Fatal(err)
	}
	var credentialID string
	if err := db.DB.QueryRow(`SELECT credential_id FROM sessions WHERE id=?`, sessionID).Scan(&credentialID); err != nil {
		t.Fatal(err)
	}
	if sessionID == credentialID || !validSessionCredentialID(credentialID) {
		t.Fatalf("unsafe session identities bearer=%q credential=%q", sessionID, credentialID)
	}
}

func TestCreateSessionRequiresExactActiveUser(t *testing.T) {
	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "session-mint-status")
	if _, err := db.DB.Exec(`UPDATE users SET status='suspended' WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sessionID, err := createSession(context.Background(), userID, now, now.Add(time.Hour), false, false)
	if err == nil || sessionID != "" {
		t.Fatalf("non-active user minted session=%q err=%v", sessionID, err)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=?`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("non-active user minted %d session rows", count)
	}
}

func TestPasswordAndTOTPAdmissionsRequireExactActiveUser(t *testing.T) {
	t.Run("password", func(t *testing.T) {
		setupPrincipalTestDB(t)
		userID := insertPrincipalUser(t, "suspended-password-login")
		hash, err := HashPassword("correct-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(`UPDATE users SET password=?,status='suspended' WHERE id=?`, hash, userID); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
			strings.NewReader(`{"username":"suspended-password-login","password":"correct-password"}`))
		rec := httptest.NewRecorder()
		LoginHandler(rec, req)
		if rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
			t.Fatalf("non-active password login status=%d cookies=%v", rec.Code, rec.Result().Cookies())
		}
		var count int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=?`, userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("non-active password login minted %d sessions", count)
		}
	})

	t.Run("totp completion", func(t *testing.T) {
		for _, status := range []string{"suspended", "inactive"} {
			t.Run(status, func(t *testing.T) {
				setupPrincipalTestDB(t)
				userID := insertPrincipalUser(t, status+"-totp-login")
				const secret = "JBSWY3DPEHPK3PXP"
				if _, err := db.DB.Exec(`UPDATE users SET status=?,totp_secret=?,totp_enabled=1 WHERE id=?`, status, secret, userID); err != nil {
					t.Fatal(err)
				}
				pendingToken := status + "-user-pending-token"
				if _, err := db.DB.Exec(`INSERT INTO totp_pending(token,user_id,expires_at) VALUES(?,?,?)`,
					pendingToken, userID, time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")); err != nil {
					t.Fatal(err)
				}
				code, err := totp.GenerateCode(secret, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				body := fmt.Sprintf(`{"totp_token":%q,"code":%q}`, pendingToken, code)
				req := httptest.NewRequest(http.MethodPost, "/api/auth/totp/verify", strings.NewReader(body))
				rec := httptest.NewRecorder()
				TOTPVerify(rec, req)
				if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
					t.Fatalf("non-active TOTP completion status=%d cookies=%v", rec.Code, rec.Result().Cookies())
				}
				var count int
				if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=?`, userID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("non-active TOTP completion minted %d sessions", count)
				}
			})
		}
	})
}

func TestEverySessionMintPathUsesCredentialFactory(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate auth source")
	}
	directory := filepath.Dir(currentFile)
	for file, marker := range map[string]string{
		"auth.go":          `createSession(r.Context(), loginUser.ID, now, expiresAt, false, false)`,
		"totp.go":          `createSession(r.Context(), userID, now, expiresAt, false, false)`,
		"oidc.go":          `createSession(r.Context(), user.ID, now, expiresAt, false, true)`,
		"dev_login_dev.go": `createSession(r.Context(), loginUser.ID, now, expiresAt, true, false)`,
	} {
		body, err := os.ReadFile(filepath.Join(directory, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), marker) {
			t.Fatalf("%s bypasses the safe credential session factory", file)
		}
		if file != "auth.go" && strings.Contains(string(body), "INSERT INTO sessions") {
			t.Fatalf("%s directly writes session rows", file)
		}
	}
}

func TestReauthorizeSessionPrincipalRejectsIdentityAndStatusRotation(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	actorID := insertPrincipalUser(t, "session-actor")
	targetID := insertPrincipalUser(t, "session-target")
	if _, err := db.DB.Exec(`UPDATE users SET role='admin',is_super_admin=1 WHERE id=?`, actorID); err != nil {
		t.Fatal(err)
	}
	insertPrincipalSession(t, actorID, testSessionCredential, now.Add(-time.Hour), now.Add(time.Hour))
	original, _ := NewSessionPrincipal(testSessionCredential, actorID, actorID, false)

	if _, err := db.DB.Exec(`UPDATE sessions SET actor_user_id=?,acting_as_user_id=? WHERE credential_id=?`,
		actorID, targetID, testSessionCredential); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.DB.BeginTx(context.Background(), nil)
	_, _, err := ReauthorizePrincipalTx(context.Background(), tx, original, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrPrincipalChanged) {
		t.Fatalf("effective-user rotation was not detected: %v", err)
	}

	impersonated, _ := NewSessionPrincipal(testSessionCredential, actorID, targetID, true)
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, impersonated, now)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("valid impersonated identity was not reauthorized: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET status='inactive' WHERE id=?`, targetID); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, impersonated, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("disabled effective user remained authorized: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET status='active' WHERE id=?`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET role='member',role_key='member',is_super_admin=0 WHERE id=?`, actorID); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, impersonated, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("demoted impersonating actor remained authorized: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET role='admin',role_key='super_admin',is_super_admin=1 WHERE id=?`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET status='deleted' WHERE id=?`, targetID); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, impersonated, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("deleted effective user remained authorized: %v", err)
	}
}

func TestReauthorizeAPIKeyPrincipalRefreshesScopesAndOwner(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	ownerID := insertPrincipalUser(t, "key-owner")
	otherID := insertPrincipalUser(t, "key-other")
	keyID := insertPrincipalAPIKey(t, ownerID, "issues:read,issues:write", nil)
	expected, _ := NewAPIKeyPrincipal(keyID, ownerID, ScopeSet{"issues:read": {}, "issues:write": {}})
	if _, err := db.DB.Exec(`UPDATE api_keys SET scopes='issues:read' WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.DB.BeginTx(context.Background(), nil)
	_, current, err := ReauthorizePrincipalTx(context.Background(), tx, expected, now)
	_ = tx.Rollback()
	if err != nil || !current.HasScope("issues:read") || current.HasScope("issues:write") {
		t.Fatalf("scope loss was not observed in transaction: current=%#v err=%v", current, err)
	}
	if _, err := db.DB.Exec(`UPDATE api_keys SET scopes='' WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, current, err = ReauthorizePrincipalTx(context.Background(), tx, expected, now)
	_ = tx.Rollback()
	if err != nil || current.HasScope("issues:read") || current.HasScope("issues:write") || current.HasScope(ScopeAll) {
		t.Fatalf("complete scope loss was not observed in transaction: current=%#v err=%v", current, err)
	}

	// Simulate a legacy-corrupt row to prove Tx reauthorization checks the
	// current owner, independently of M147's identity trigger.
	if _, err := db.DB.Exec(`DROP TRIGGER trg_api_keys_identity_update_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE api_keys SET user_id=? WHERE id=?`, otherID, keyID); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.DB.BeginTx(context.Background(), nil)
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, expected, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrPrincipalChanged) {
		t.Fatalf("API-key owner rotation was not detected: %v", err)
	}
}

func TestSessionCredentialCanNeverAliasBearer(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	userID := insertPrincipalUser(t, "credential-alias")
	insertPrincipalSession(t, userID, testSessionCredential, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := db.DB.Exec(`DROP TRIGGER trg_sessions_identity_update_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE sessions SET id=credential_id WHERE credential_id=?`, testSessionCredential); err != nil {
		t.Fatal(err)
	}
	principal, _ := NewSessionPrincipal(testSessionCredential, userID, userID, false)
	tx, _ := db.DB.BeginTx(context.Background(), nil)
	_, _, err := ReauthorizePrincipalTx(context.Background(), tx, principal, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("credential/bearer alias reauthorized: %v", err)
	}
	if _, err := loadSession(testSessionCredential); err == nil {
		t.Fatal("middleware session loader accepted credential/bearer alias")
	}
}

func TestResolveAPIKeyPrincipalUsesExactCurrentExpiryAndStatus(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	userID := insertPrincipalUser(t, "middleware-key-owner")
	rawKey := "paimos_test_current-key-secret"
	digest := sha256.Sum256([]byte(rawKey))
	result, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,expires_at)
		VALUES(?,'middleware-current',?,'paimos_test','issues:read',?)`, userID, hex.EncodeToString(digest[:]),
		now.Add(time.Millisecond).Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	if _, principal, err := resolveAPIKeyPrincipalAt(rawKey, now); err != nil || principal.APIKeyID() != keyID {
		t.Fatalf("pre-expiry key was rejected: principal=%#v err=%v", principal, err)
	}
	if _, _, err := resolveAPIKeyPrincipalAt(rawKey, now.Add(time.Millisecond)); err == nil {
		t.Fatal("key remained valid at exact expiry")
	}
	if _, err := db.DB.Exec(`UPDATE api_keys SET expires_at=NULL,disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveAPIKeyPrincipalAt(rawKey, now); err == nil {
		t.Fatal("disabled key resolved in middleware")
	}
}

func TestResolveAPIKeyUsageStampNeverInheritsSQLiteBusyTimeout(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	userID := insertPrincipalUser(t, "nonblocking-usage-stamp")
	rawKey := "paimos_test_nonblocking_usage_stamp"
	digest := sha256.Sum256([]byte(rawKey))
	result, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'nonblocking-usage',?,'paimos_test','issues:read')`, userID, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	writer, err := db.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer writer.ExecContext(context.Background(), `ROLLBACK`)

	started := time.Now()
	user, principal, err := resolveAPIKeyPrincipalAt(rawKey, now)
	elapsed := time.Since(started)
	if err != nil || user.ID != userID || principal.APIKeyID() != keyID {
		t.Fatalf("authentication failed while optional usage stamp was busy: user=%#v principal=%#v err=%v", user, principal, err)
	}
	if elapsed > time.Second {
		t.Fatalf("authentication inherited SQLite's busy timeout: %s", elapsed)
	}
	policyConn, err := db.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer policyConn.Close()
	var busyTimeout int
	if err := policyConn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != db.DefaultBusyTimeoutMS {
		t.Fatalf("usage-stamp connection returned with busy_timeout=%d want %d", busyTimeout, db.DefaultBusyTimeoutMS)
	}
}

func TestResolveAPIKeyRecentUsageStaysReadOnlyWhileSQLiteWriterIsBusy(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	userID := insertPrincipalUser(t, "read-only-usage-stamp")
	rawKey := "paimos_test_read_only_usage_stamp"
	digest := sha256.Sum256([]byte(rawKey))
	lastUsed := now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	result, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,last_used_at)
		VALUES(?,'read-only-usage',?,'paimos_test','issues:read',?)`, userID, hex.EncodeToString(digest[:]), lastUsed)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	writer, err := db.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer writer.ExecContext(context.Background(), `ROLLBACK`)

	started := time.Now()
	user, principal, err := resolveAPIKeyPrincipalAt(rawKey, now)
	elapsed := time.Since(started)
	if err != nil || user.ID != userID || principal.APIKeyID() != keyID {
		t.Fatalf("authentication failed while writer was busy: user=%#v principal=%#v err=%v", user, principal, err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("recent API-key authentication attempted a writer lock: %s", elapsed)
	}
	var stamped string
	if err := db.DB.QueryRow(`SELECT last_used_at FROM api_keys WHERE id=?`, keyID).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if stamped != lastUsed {
		t.Fatalf("recent usage stamp changed: got %q want %q", stamped, lastUsed)
	}
}

func TestAPIKeyOwnerStatusFailsClosedInTxAndMiddleware(t *testing.T) {
	for _, status := range []string{"inactive", "deleted", "suspended"} {
		t.Run(status, func(t *testing.T) {
			setupPrincipalTestDB(t)
			now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
			userID := insertPrincipalUser(t, "key-owner-"+status)
			rawKey := "paimos_test_owner_" + status
			digest := sha256.Sum256([]byte(rawKey))
			result, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
				VALUES(?,'owner-status',?,'paimos_owner','issues:read')`, userID, hex.EncodeToString(digest[:]))
			if err != nil {
				t.Fatal(err)
			}
			keyID, _ := result.LastInsertId()
			principal, _ := NewAPIKeyPrincipal(keyID, userID, ScopeSet{"issues:read": {}})
			if _, err := db.DB.Exec(`UPDATE users SET status=? WHERE id=?`, status, userID); err != nil {
				t.Fatal(err)
			}
			tx, _ := db.DB.BeginTx(context.Background(), nil)
			_, _, txErr := ReauthorizePrincipalTx(context.Background(), tx, principal, now)
			_ = tx.Rollback()
			if !errors.Is(txErr, ErrCredentialUnavailable) {
				t.Fatalf("%s owner reauthorized in Tx: %v", status, txErr)
			}
			if _, _, err := resolveAPIKeyPrincipalAt(rawKey, now); err == nil {
				t.Fatalf("%s owner resolved in middleware", status)
			}
		})
	}
}

func TestSessionFailureLogsNeverExposeBearer(t *testing.T) {
	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "safe-session-log")
	rawBearer := "RAW-BEARER-sk-live-session-log-canary"
	credentialID := "92929292-9292-4292-8292-929292929292"
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,'2026-12-01 00:00:00','2026-08-01 00:00:00',?)`, rawBearer, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET status='inactive' WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER test_block_session_cleanup BEFORE DELETE ON sessions
		WHEN OLD.credential_id='` + credentialID + `' BEGIN SELECT RAISE(ABORT,'blocked cleanup'); END`); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: rawBearer})
	rec := httptest.NewRecorder()
	Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("disabled session reached handler")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled session status=%d", rec.Code)
	}
	if strings.Contains(logs.String(), rawBearer) || !strings.Contains(logs.String(), credentialID) {
		t.Fatalf("unsafe session cleanup log: %q", logs.String())
	}
}

func TestMiddlewareRejectsInvalidSessionTimestamps(t *testing.T) {
	for _, test := range []struct {
		name      string
		createdAt string
		expiresAt func() string
	}{
		{
			name:      "malformed created at",
			createdAt: "not-a-session-created-at",
			expiresAt: func() string { return time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05") },
		},
		{
			name:      "malformed lexically live expiry",
			createdAt: time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05"),
			expiresAt: func() string { return "zzzz-not-a-session-expiry" },
		},
		{
			name:      "same-day expired RFC3339",
			createdAt: time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05"),
			expiresAt: func() string { return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339) },
		},
		{
			name:      "future created at",
			createdAt: time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05"),
			expiresAt: func() string { return time.Now().UTC().Add(2 * time.Hour).Format("2006-01-02 15:04:05") },
		},
		{
			name:      "exact absolute lifetime",
			createdAt: time.Now().UTC().Add(-sessionAbsoluteLifetime).Format("2006-01-02 15:04:05"),
			expiresAt: func() string { return time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05") },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			setupPrincipalTestDB(t)
			userID := insertPrincipalUser(t, "malformed-session-time")
			rawBearer := "raw-malformed-session-bearer"
			expiresAt := test.expiresAt()
			if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
				VALUES(?,?,?,?,?)`, rawBearer, userID, expiresAt, test.createdAt,
				"83838383-8383-4383-8383-838383838383"); err != nil {
				t.Fatal(err)
			}

			reached := false
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: rawBearer})
			rec := httptest.NewRecorder()
			Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			})).ServeHTTP(rec, req)
			if reached || rec.Code != http.StatusUnauthorized {
				t.Fatalf("malformed session reached handler=%v status=%d", reached, rec.Code)
			}
			assertSessionCookieCleared(t, rec)
			var retained int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, rawBearer).Scan(&retained); err != nil {
				t.Fatal(err)
			}
			if retained != 0 {
				t.Fatalf("rejected session with expiry %q was retained or renewed", expiresAt)
			}
		})
	}
}

func TestInvalidSessionTimeFailureLogNeverExposesBearer(t *testing.T) {
	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "invalid-time-safe-log")
	rawBearer := "RAW-BEARER-sk-live-invalid-time-canary"
	credentialID := "86868686-8686-4686-8686-868686868686"
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,'zzzz-not-a-session-expiry','2026-08-01 00:00:00',?)`, rawBearer, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER test_block_invalid_time_cleanup BEFORE DELETE ON sessions
		WHEN OLD.credential_id='` + credentialID + `' BEGIN SELECT RAISE(ABORT,'blocked invalid-time cleanup'); END`); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: rawBearer})
	rec := httptest.NewRecorder()
	Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid-time session reached handler")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid-time session status=%d", rec.Code)
	}
	assertSessionCookieCleared(t, rec)
	if strings.Contains(logs.String(), rawBearer) || !strings.Contains(logs.String(), credentialID) {
		t.Fatalf("unsafe invalid-time cleanup log: %q", logs.String())
	}
}

func TestMiddlewareCookieCleanupDistinguishesInvalidSessionFromStoreFailure(t *testing.T) {
	t.Run("missing session clears cookie", func(t *testing.T) {
		setupPrincipalTestDB(t)
		db.DB.SetMaxOpenConns(1)
		if _, err := db.DB.Exec(`PRAGMA query_only=ON`); err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		previousWriter := log.Writer()
		previousFlags := log.Flags()
		log.SetOutput(&logs)
		log.SetFlags(0)
		t.Cleanup(func() {
			log.SetOutput(previousWriter)
			log.SetFlags(previousFlags)
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "missing-session-bearer"})
		rec := httptest.NewRecorder()
		Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("missing session reached handler")
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("missing session status=%d", rec.Code)
		}
		assertSessionCookieCleared(t, rec)
		if strings.Contains(logs.String(), "delete rejected session") {
			t.Fatalf("missing cookie attempted a database cleanup write: %q", logs.String())
		}
	})

	t.Run("store failure preserves cookie", func(t *testing.T) {
		setupPrincipalTestDB(t)
		if err := db.DB.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "retryable-session-bearer"})
		rec := httptest.NewRecorder()
		Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("store-failure session reached handler")
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("store failure status=%d", rec.Code)
		}
		if sessionCookieWasCleared(rec) {
			t.Fatalf("transient store failure expired the client credential: %v", rec.Result().Cookies())
		}
	})
}

func TestMiddlewareAcceptsLiveRFC3339Session(t *testing.T) {
	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "live-rfc3339-session")
	now := time.Now().UTC()
	rawBearer := "live-rfc3339-session-bearer"
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,?,?,?)`, rawBearer, userID, now.Add(time.Minute).Format(time.RFC3339),
		now.Add(-time.Hour).Format(time.RFC3339), "84848484-8484-4484-8484-848484848484"); err != nil {
		t.Fatal(err)
	}

	reached := false
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: rawBearer})
	rec := httptest.NewRecorder()
	Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	})).ServeHTTP(rec, req)
	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("live RFC3339 session reached handler=%v status=%d", reached, rec.Code)
	}
}

func TestLoadSessionAtRejectsExactAbsoluteLifetime(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	userID := insertPrincipalUser(t, "exact-cap-session")
	rawBearer := "exact-cap-session-bearer"
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES(?,?,?,?,?)`, rawBearer, userID,
		now.Add(time.Hour).Format("2006-01-02 15:04:05"),
		now.Add(-sessionAbsoluteLifetime).Format("2006-01-02 15:04:05"),
		"87878787-8787-4787-8787-878787878787"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSessionAt(rawBearer, now); !errors.Is(err, errInvalidSession) {
		t.Fatalf("session survived exact absolute lifetime: %v", err)
	}
	var retained int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, rawBearer).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 0 {
		t.Fatal("exact-cap session row was retained")
	}
}

func TestMiddlewareRequiresExactActiveSessionUsers(t *testing.T) {
	for _, suspendedParty := range []string{"actor", "effective"} {
		t.Run(suspendedParty, func(t *testing.T) {
			setupPrincipalTestDB(t)
			now := time.Now().UTC()
			actorID := insertPrincipalUser(t, "middleware-status-actor")
			targetID := insertPrincipalUser(t, "middleware-status-target")
			if _, err := db.DB.Exec(`UPDATE users SET role='admin',role_key='super_admin',is_super_admin=1 WHERE id=?`, actorID); err != nil {
				t.Fatal(err)
			}
			rawBearer := "middleware-status-session-bearer"
			credentialID := "85858585-8585-4585-8585-858585858585"
			if _, err := db.DB.Exec(`INSERT INTO sessions(
				 id,user_id,expires_at,created_at,credential_id,actor_user_id,acting_as_user_id
				) VALUES(?,?,?,?,?,?,?)`, rawBearer, actorID,
				now.Add(time.Hour).Format("2006-01-02 15:04:05"),
				now.Add(-time.Hour).Format("2006-01-02 15:04:05"),
				credentialID, actorID, targetID); err != nil {
				t.Fatal(err)
			}
			suspendedID := actorID
			if suspendedParty == "effective" {
				suspendedID = targetID
			}
			if _, err := db.DB.Exec(`UPDATE users SET status='suspended' WHERE id=?`, suspendedID); err != nil {
				t.Fatal(err)
			}

			reached := false
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: rawBearer})
			rec := httptest.NewRecorder()
			Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			})).ServeHTTP(rec, req)
			if reached || rec.Code != http.StatusUnauthorized {
				t.Fatalf("suspended %s reached handler=%v status=%d", suspendedParty, reached, rec.Code)
			}
		})
	}
}
