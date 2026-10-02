// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestKeyScopeExpiryValidation(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, raw := range []string{`"bad"`, `"2026-02-30T12:00:00Z"`, `"2026-10-02T11:59:59Z"`, `"2026-10-02T12:00:00Z"`, `""`, `0`, `true`, `{}`, `[]`} {
		if _, err := keyScopeExpiry(json.RawMessage(raw), now); !errors.Is(err, errKeyExpiry) {
			t.Fatalf("invalid expiry accepted: %s", raw)
		}
	}
	if expires, err := keyScopeExpiry(json.RawMessage(`null`), now); err != nil || expires != nil {
		t.Fatal("explicit null is not Never")
	}
	if expires, err := keyScopeExpiry(json.RawMessage(`"2026-10-02T12:00:01Z"`), now); err != nil || expires == nil || !expires.Equal(now.Add(time.Second)) {
		t.Fatal("future timestamp rejected")
	}
	for _, raw := range []string{``, `null`} {
		delta := &keyScopeDelta{Add: []string{}, ExpiresAt: json.RawMessage(raw)}
		if raw == "" {
			delta.ExpiresAt = nil
		}
		normalized, err := normalizeKeyScopeDelta(delta)
		if err != nil || (normalized.ExpiresAt == nil) != (raw == "") {
			t.Fatal("normalization lost omission vs Never")
		}
	}
	if _, err := normalizeKeyScopeDelta(&keyScopeDelta{ExpiresAt: json.RawMessage(strings.Repeat(" ", 129) + "null")}); !errors.Is(err, errKeyExpiry) {
		t.Fatal("unbounded internal expiry accepted")
	}
}

func TestKeyScopeFullAccessAndExpiryKeepBearer(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "full-access-worker", "scopes": []string{"nodes.read"}}))
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='admin') WHERE principal_id=$1::uuid AND scope_type='workspace'`, key.PrincipalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	admin, _ := authz.BuiltinPermissions("admin")
	if !slices.Contains(admin, "recurrences.manage") {
		t.Fatal("fixture requires the person Admin recurrence permission")
	}
	full := []string{}
	for _, perm := range authz.Registry {
		// Built-in agent roles require an explicit custom-role grant for
		// recurrence automation (authz.readGrants), unlike person Admin.
		if perm.Key == "recurrences.manage" {
			continue
		}
		if perm.AgentGrantable && slices.Contains(admin, perm.Key) {
			full = append(full, perm.Key)
		}
	}
	view, err := m.agentKeyScopes(tenant.WithPrincipal(ctx, owner), owner, key.ID, nil)
	if err != nil || !slices.Equal(view.Grantable, full) {
		t.Fatal("Full access differs from the live built-in agent Admin ceiling")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	secured := m.Middleware(mux)
	request := func() int {
		r := httptest.NewRequest(http.MethodGet, "/api/events", nil)
		r.Header.Set("Authorization", "Bearer "+key.Token)
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		return w.Code
	}
	if request() != http.StatusForbidden {
		t.Fatal("narrow key already has history access")
	}
	beforeKeys, beforeEvents := keyCounts(t, m, owner)
	// Full access must not smuggle this custom-role-only scope through an
	// expiry edit. The rejected combined write must leave the key intact.
	deniedBody, _ := json.Marshal(map[string]any{"add": []string{"recurrences.manage"}, "expires_at": time.Now().UTC().Add(24 * time.Hour)})
	if w := scopesRequest(m, owner, key.ID, http.MethodPatch, string(deniedBody)); w.Code != http.StatusForbidden {
		t.Fatalf("built-in agent recurrence grant status %d, want 403", w.Code)
	}
	view, err = m.agentKeyScopes(tenant.WithPrincipal(ctx, owner), owner, key.ID, nil)
	if err != nil || view.Key.ID != key.ID || view.Key.Prefix != key.Prefix || view.Key.ExpiresAt != nil || view.Key.RevokedAt != nil || !slices.Equal(view.Key.Scopes, key.Scopes) {
		t.Fatal("rejected recurrence grant changed the key")
	}
	if keys, events := keyCounts(t, m, owner); keys != beforeKeys || events != beforeEvents {
		t.Fatal("rejected recurrence grant wrote a key or audit event")
	}
	for i, days := range []int{30, 90, 365, 0} {
		var expiry *time.Time
		if days > 0 {
			at := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Truncate(time.Second)
			expiry = &at
		}
		// Reapply Full access with every expiry choice, including Never.
		edit := map[string]any{"add": full, "expires_at": expiry}
		body, _ := json.Marshal(edit)
		w := scopesRequest(m, owner, key.ID, http.MethodPatch, string(body))
		if w.Code != http.StatusOK {
			t.Fatalf("expiry edit status %d", w.Code)
		}
		if strings.Contains(w.Body.String(), key.Token) || strings.Contains(w.Body.String(), "token") || strings.Contains(w.Body.String(), "hash") {
			t.Fatal("edit exposed credential material")
		}
		var got agentKeyJSON
		if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.ID != key.ID || got.Prefix != key.Prefix || got.RevokedAt != nil || (got.ExpiresAt == nil) != (expiry == nil) || expiry != nil && !got.ExpiresAt.Equal(*expiry) {
			t.Fatal("edit changed key identity or lost expiry")
		}
		want := slices.Clone(full)
		slices.Sort(want)
		actual := slices.Clone(got.Scopes)
		slices.Sort(actual)
		if !slices.Equal(actual, want) {
			t.Fatal("Full access differs from the built-in agent Admin ceiling intersected with agent-grantable")
		}
		if request() != http.StatusNoContent {
			t.Fatal("original bearer did not work immediately after edit")
		}
		keys, events := keyCounts(t, m, owner)
		if keys != beforeKeys || events != beforeEvents+i+1 {
			t.Fatal("edit rotated or missed its single audit")
		}
		// Both exact expiry retries and omission are no-ops, without rotation.
		if w := scopesRequest(m, owner, key.ID, http.MethodPatch, string(body)); w.Code != http.StatusOK {
			t.Fatalf("expiry retry status %d", w.Code)
		}
		w = scopesRequest(m, owner, key.ID, http.MethodPatch, `{"add":[]}`)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || (got.ExpiresAt == nil) != (expiry == nil) || expiry != nil && !got.ExpiresAt.Equal(*expiry) {
			t.Fatal("omitted expiry did not preserve the current value")
		}
		_, retryEvents := keyCounts(t, m, owner)
		if retryEvents != events {
			t.Fatal("no-op expiry retry or omission wrote an event")
		}
	}
	_, events := keyCounts(t, m, owner)
	if w := scopesRequest(m, owner, key.ID, http.MethodPatch, `{"expires_at":null}`); w.Code != http.StatusOK {
		t.Fatalf("no-op status %d", w.Code)
	}
	_, afterEvents := keyCounts(t, m, owner)
	if afterEvents != events {
		t.Fatal("expiry no-op wrote an event")
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var complete bool
		err := tx.QueryRow(ctx, `SELECT count(*)=4 AND bool_and(actor_principal_id=$1::uuid AND before->>'key_id'=$2 AND after->>'key_id'=$2 AND before->'expires_at' IS DISTINCT FROM after->'expires_at' AND NOT (after ? 'hash') AND NOT (after ? 'token')) FROM events WHERE type='agent_key.scopes_changed'`, owner.ID, key.ID).Scan(&complete)
		if err == nil && !complete {
			t.Error("expiry audit lacks actor or before/after")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyScopeExpiryPermissionsAndAtomicFailure(t *testing.T) {
	m, owner := keyFixture(t)
	expiry := time.Now().UTC().Add(90 * 24 * time.Hour).Truncate(time.Second)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "expiry-worker", "scopes": []string{"nodes.read", "nodes.write"}, "expires_at": expiry}))
	keys, events := keyCounts(t, m, owner)
	for _, body := range []string{`{"expires_at":"bad"}`, `{"expires_at":"2020-01-01T00:00:00Z"}`, `{"expires_at":true}`, `{"expires_at":42}`, `{"expires_at":{}}`, `{"expires_at":null} {}`} {
		if w := scopesRequest(m, owner, key.ID, http.MethodPatch, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid expiry status %d", w.Code)
		}
	}
	if w := scopesRequest(m, owner, key.ID, http.MethodPatch, `{"add":["harness.worker"],"expires_at":null}`); w.Code != http.StatusForbidden {
		t.Fatalf("role ceiling bypass: %d", w.Code)
	}
	editor := keyScopeEditor(t, m, owner, "keys.manage", "nodes.read")
	if w := scopesRequest(m, editor, key.ID, http.MethodPatch, `{"expires_at":null}`); w.Code != http.StatusForbidden {
		t.Fatalf("expiry-only bypassed editor ceiling: %d", w.Code)
	}
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET created_by_principal_id=$2::uuid WHERE id=$1::uuid`, key.ID, editor.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, owner, key.ID, http.MethodPatch, `{"expires_at":null}`); w.Code != http.StatusForbidden {
		t.Fatalf("expiry-only bypassed original creator ceiling: %d", w.Code)
	}
	agent := tenant.Principal{ID: key.PrincipalID, TenantID: owner.TenantID, Kind: tenant.Agent, Scopes: []string{"keys.manage"}}
	if w := scopesRequest(m, agent, key.ID, http.MethodPatch, `{"expires_at":null}`); w.Code != http.StatusForbidden {
		t.Fatal("agent edited expiry")
	}
	view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
	if err != nil || view.Key.ExpiresAt == nil || !view.Key.ExpiresAt.Equal(expiry) || !slices.Equal(view.Key.Scopes, key.Scopes) {
		t.Fatal("failed edit partially changed key")
	}
	afterKeys, afterEvents := keyCounts(t, m, owner)
	if afterKeys != keys || afterEvents != events {
		t.Fatal("failed expiry edit changed key/event count")
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET expires_at=now()-interval '1 second' WHERE id=$1::uuid`, key.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, owner, key.ID, http.MethodPatch, `{"expires_at":null}`); w.Code != http.StatusConflict {
		t.Fatal("expired key revived")
	}
	if err := m.revokeAgentKey(tenant.WithPrincipal(t.Context(), owner), owner, key.ID); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, owner, key.ID, http.MethodPatch, `{"expires_at":null}`); w.Code != http.StatusConflict {
		t.Fatal("revoked key revived")
	}
}

func TestKeyScopeExpiryRechecksManagementInWrite(t *testing.T) {
	m, owner := keyFixture(t)
	expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "stale-expiry-editor", "scopes": []string{"nodes.read"}, "expires_at": expires}))
	editor := keyScopeEditor(t, m, owner, "keys.manage", "nodes.read")
	// Revoke after the handler's read check, exactly at the write boundary.
	m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(pgx.Tx) error) error {
		if err := db.InTenant(dbtest.Seed(ctx), pool, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE permission='keys.manage' AND role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$1::uuid)`, editor.ID)
			return err
		}); err != nil {
			return err
		}
		return db.InTenant(ctx, pool, tenantID, fn)
	}
	if w := scopesRequest(m, editor, key.ID, http.MethodPatch, `{"expires_at":null}`); w.Code != http.StatusForbidden {
		t.Fatalf("stale management permission edited expiry: %d", w.Code)
	}
	m.inTenant = db.InTenant
	view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
	if err != nil || view.Key.ExpiresAt == nil || !view.Key.ExpiresAt.Equal(expires) {
		t.Fatal("denied stale edit changed expiry")
	}
}
