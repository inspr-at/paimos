// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
)

func insertMachineNotifierAuthKey(t *testing.T, userID int64, rawKey, credentialKind, scopes string, expiresAt *time.Time) int64 {
	t.Helper()
	var expiry any
	if expiresAt != nil {
		expiry = expiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	digest := sha256.Sum256([]byte(rawKey))
	result, err := db.DB.Exec(`
		INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,expires_at,credential_kind)
		VALUES(?,'machine-notifier-auth-test',?,'paimos_test',?,?,?)
	`, userID, hex.EncodeToString(digest[:]), scopes, expiry, credentialKind)
	if err != nil {
		t.Fatalf("insert machine notifier API key: %v", err)
	}
	keyID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("machine notifier API key id: %v", err)
	}
	return keyID
}

func insertUncheckedMachineNotifierAuthKey(t *testing.T, userID int64, rawKey, credentialKind string) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("open fixture connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatalf("disable fixture constraints: %v", err)
	}
	digest := sha256.Sum256([]byte(rawKey))
	_, insertErr := conn.ExecContext(ctx, `
		INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,credential_kind)
		VALUES(?,'invalid-machine-notifier-auth-test',?,'paimos_test','',?)
	`, userID, hex.EncodeToString(digest[:]), credentialKind)
	_, restoreErr := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=OFF`)
	if restoreErr != nil {
		t.Fatalf("restore fixture constraints: %v", restoreErr)
	}
	if insertErr != nil {
		t.Fatalf("insert invalid credential kind fixture: %v", insertErr)
	}
}

func TestMachineNotifierMiddlewareAllowsOnlyNotifierEndpoints(t *testing.T) {
	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "machine-notifier-routes")
	rawKey := "paimos_test_machine_notifier_routes"
	keyID := insertMachineNotifierAuthKey(t, userID, rawKey, "machine_notifier", "", nil)

	tests := []struct {
		name    string
		method  string
		path    string
		allowed bool
	}{
		{name: "post message", method: http.MethodPost, path: "/api/machine-notifier/messages", allowed: true},
		{name: "get own receipt", method: http.MethodGet, path: "/api/machine-notifier/messages/message-123/receipt", allowed: true},
		{name: "get collection", method: http.MethodGet, path: "/api/machine-notifier/messages"},
		{name: "put message", method: http.MethodPut, path: "/api/machine-notifier/messages"},
		{name: "patch message", method: http.MethodPatch, path: "/api/machine-notifier/messages"},
		{name: "delete message", method: http.MethodDelete, path: "/api/machine-notifier/messages"},
		{name: "head message", method: http.MethodHead, path: "/api/machine-notifier/messages"},
		{name: "options message", method: http.MethodOptions, path: "/api/machine-notifier/messages"},
		{name: "connect message", method: http.MethodConnect, path: "/api/machine-notifier/messages"},
		{name: "trace message", method: http.MethodTrace, path: "/api/machine-notifier/messages"},
		{name: "post receipt", method: http.MethodPost, path: "/api/machine-notifier/messages/message-123/receipt"},
		{name: "put receipt", method: http.MethodPut, path: "/api/machine-notifier/messages/message-123/receipt"},
		{name: "patch receipt", method: http.MethodPatch, path: "/api/machine-notifier/messages/message-123/receipt"},
		{name: "delete receipt", method: http.MethodDelete, path: "/api/machine-notifier/messages/message-123/receipt"},
		{name: "head receipt", method: http.MethodHead, path: "/api/machine-notifier/messages/message-123/receipt"},
		{name: "options receipt", method: http.MethodOptions, path: "/api/machine-notifier/messages/message-123/receipt"},
		{name: "connect receipt", method: http.MethodConnect, path: "/api/machine-notifier/messages/message-123/receipt"},
		{name: "trace receipt", method: http.MethodTrace, path: "/api/machine-notifier/messages/message-123/receipt"},
		{name: "missing receipt suffix", method: http.MethodGet, path: "/api/machine-notifier/messages/message-123"},
		{name: "missing message id", method: http.MethodGet, path: "/api/machine-notifier/messages//receipt"},
		{name: "receipt descendant", method: http.MethodGet, path: "/api/machine-notifier/messages/message-123/receipt/details"},
		{name: "lookalike prefix", method: http.MethodGet, path: "/api/machine-notifier/messages-other/message-123/receipt"},
		{name: "general API route", method: http.MethodGet, path: "/api/projects"},
		{name: "session API route", method: http.MethodGet, path: "/api/auth/me"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reached := false
			handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				principal, ok := GetPrincipal(r)
				if !ok || principal.Kind() != PrincipalMachineNotifier || principal.APIKeyID() != keyID ||
					principal.UserID() != userID || len(principal.Scopes()) != 0 {
					t.Fatalf("unexpected notifier principal: ok=%v principal=%#v", ok, principal)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(test.method, test.path, nil)
			req.Header.Set("Authorization", "Bearer "+rawKey)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if test.allowed {
				if rec.Code != http.StatusNoContent || !reached {
					t.Fatalf("allowed route status=%d reached=%v", rec.Code, reached)
				}
				return
			}
			if rec.Code != http.StatusForbidden || reached {
				t.Fatalf("denied route status=%d reached=%v", rec.Code, reached)
			}
		})
	}
}

func TestMachineNotifierCredentialKindsFailClosed(t *testing.T) {
	for _, credentialKind := range []string{"", "machine-notifier", "Machine_Notifier", "machine_notifier ", "future_kind"} {
		if _, ok := principalKindForCredential(credentialKind); ok {
			t.Fatalf("credential kind %q was recognized", credentialKind)
		}
		if _, err := principalForAPIKey(credentialKind, 1, 1, ScopeSet{ScopeAll: {}}); err == nil {
			t.Fatalf("credential kind %q produced a principal", credentialKind)
		}
	}

	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "invalid-machine-notifier-kind")
	for i, credentialKind := range []string{"machine_notifier ", "future_kind"} {
		rawKey := "paimos_test_invalid_notifier_" + string(rune('a'+i))
		insertUncheckedMachineNotifierAuthKey(t, userID, rawKey, credentialKind)
		if _, _, err := resolveAPIKeyPrincipalAt(rawKey, time.Now().UTC()); err == nil {
			t.Fatalf("stored credential kind %q authenticated", credentialKind)
		}

		reached := false
		req := httptest.NewRequest(http.MethodPost, "/api/machine-notifier/messages", nil)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			reached = true
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || reached {
			t.Fatalf("stored credential kind %q status=%d reached=%v", credentialKind, rec.Code, reached)
		}
	}
}

func TestMachineNotifierRestrictionDoesNotChangeGeneralOrSessionAuth(t *testing.T) {
	setupPrincipalTestDB(t)
	userID := insertPrincipalUser(t, "ordinary-auth")
	rawKey := "paimos_test_general_routes"
	keyID := insertMachineNotifierAuthKey(t, userID, rawKey, "general", "*", nil)

	assertPasses := func(t *testing.T, req *http.Request, wantKind PrincipalKind, wantKeyID int64) {
		t.Helper()
		reached := false
		rec := httptest.NewRecorder()
		Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			principal, ok := GetPrincipal(r)
			if !ok || principal.Kind() != wantKind || principal.APIKeyID() != wantKeyID || principal.UserID() != userID {
				t.Fatalf("unexpected ordinary principal: ok=%v principal=%#v", ok, principal)
			}
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent || !reached {
			t.Fatalf("ordinary auth status=%d reached=%v", rec.Code, reached)
		}
	}

	keyRequest := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	keyRequest.Header.Set("Authorization", "Bearer "+rawKey)
	assertPasses(t, keyRequest, PrincipalAPIKey, keyID)

	now := time.Now().UTC()
	insertPrincipalSession(t, userID, testSessionCredential, now.Add(-time.Hour), now.Add(time.Hour))
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	sessionRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: "bearer-cookie-secret"})
	assertPasses(t, sessionRequest, PrincipalSession, 0)
}

func TestMachineNotifierExpiryAndDisablementFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, userID, keyID int64, now time.Time)
	}{
		{
			name: "exact expiry",
			mutate: func(t *testing.T, _ int64, keyID int64, now time.Time) {
				if _, err := db.DB.Exec(`UPDATE api_keys SET expires_at=? WHERE id=?`, now.Format("2006-01-02T15:04:05.000Z"), keyID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "disabled credential",
			mutate: func(t *testing.T, _ int64, keyID int64, now time.Time) {
				if _, err := db.DB.Exec(`UPDATE api_keys SET disabled_at=? WHERE id=?`, now.Format("2006-01-02T15:04:05.000Z"), keyID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "disabled owner",
			mutate: func(t *testing.T, userID, _ int64, _ time.Time) {
				if _, err := db.DB.Exec(`UPDATE users SET status='inactive' WHERE id=?`, userID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			setupPrincipalTestDB(t)
			now := time.Now().UTC()
			userID := insertPrincipalUser(t, "unavailable-notifier")
			rawKey := "paimos_test_unavailable_notifier"
			keyID := insertMachineNotifierAuthKey(t, userID, rawKey, "machine_notifier", "", nil)
			expected, err := principalForAPIKey("machine_notifier", keyID, userID, ScopeSet{})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, userID, keyID, now)

			if _, _, err := resolveAPIKeyPrincipalAt(rawKey, now); err == nil {
				t.Fatal("unavailable notifier resolved")
			}
			tx, err := db.DB.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = ReauthorizePrincipalTx(context.Background(), tx, expected, now)
			_ = tx.Rollback()
			if !errors.Is(err, ErrCredentialUnavailable) {
				t.Fatalf("unavailable notifier reauthorized: %v", err)
			}
		})
	}
}

func TestMachineNotifierReauthorizationPreservesExactIdentity(t *testing.T) {
	setupPrincipalTestDB(t)
	now := time.Now().UTC()
	userID := insertPrincipalUser(t, "notifier-revalidation")
	rawKey := "paimos_test_notifier_revalidation"
	keyID := insertMachineNotifierAuthKey(t, userID, rawKey, "machine_notifier", "", nil)
	expected, err := principalForAPIKey("machine_notifier", keyID, userID, ScopeSet{"stale:scope": {}})
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	user, current, err := ReauthorizePrincipalTx(context.Background(), tx, expected, now)
	_ = tx.Rollback()
	if err != nil || user.ID != userID || current.Kind() != PrincipalMachineNotifier ||
		len(current.Scopes()) != 0 || current.HasScope("stale:scope") {
		t.Fatalf("notifier did not revalidate current identity: user=%#v principal=%#v err=%v", user, current, err)
	}

	if _, err := db.DB.Exec(`UPDATE api_keys SET credential_kind='general' WHERE id=?`, keyID); err == nil {
		t.Fatal("database allowed machine notifier credential kind to change")
	}

	// Simulate a legacy-corrupt row to prove transaction reauthorization
	// independently checks the immutable credential kind.
	if _, err := db.DB.Exec(`DROP TRIGGER trg_api_keys_credential_kind_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE api_keys SET credential_kind='general' WHERE id=?`, keyID); err != nil {
		t.Fatal(err)
	}
	tx, err = db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ReauthorizePrincipalTx(context.Background(), tx, expected, now)
	_ = tx.Rollback()
	if !errors.Is(err, ErrPrincipalChanged) {
		t.Fatalf("credential-kind change was not detected: %v", err)
	}
}
