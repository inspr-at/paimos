// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func scopesRequest(m *Module, p tenant.Principal, id, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/agent-keys/"+id+"/scopes", strings.NewReader(body))
	r.SetPathValue("id", id)
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	w := httptest.NewRecorder()
	m.handleAgentKeyScopes(w, r)
	return w
}

func TestKeyScopeChangesImmediateAuditedAndBounded(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "scope-worker", "scopes": []string{"nodes.read", "harness.worker", "events.read"}}))
	ctx := dbtest.Seed(t.Context())
	call := func(body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := scopesRequest(m, owner, key.ID, "PATCH", body)
		if w.Code != want {
			t.Fatalf("scope edit status = %d, want %d", w.Code, want)
		}
		if strings.Contains(w.Body.String(), "token") || strings.Contains(w.Body.String(), "hash") || strings.Contains(w.Body.String(), key.Token) {
			t.Fatal("scope edit exposed credential material")
		}
		return w
	}
	check := func(want bool) {
		t.Helper()
		prefix, secret, _ := parseBearer("Bearer " + key.Token)
		p, ok, err := m.authenticateAgent(ctx, prefix, secret)
		if err != nil || !ok {
			t.Fatal("original key no longer authenticates")
		}
		err = authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, p), m.pool), "harness.worker", authz.Scope{})
		if (err == nil) != want || err != nil && !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("immediate permission = %v, want %v", err, want)
		}
	}
	beforeKeys, beforeEvents := keyCounts(t, m, owner)
	call(`{"remove":["harness.worker"]}`, 200)
	check(false)
	call(`{"add":["harness:worker","harness.worker"]}`, 200)
	check(true)
	keys, events := keyCounts(t, m, owner)
	if keys != beforeKeys || events != beforeEvents+2 {
		t.Fatal("scope edit rotated a key or missed its audit")
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var complete bool
		returnErr := tx.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(actor_principal_id=$1::uuid AND before->>'key_id'=$2 AND after->>'key_id'=$2 AND before->'scopes' <> after->'scopes' AND NOT (after ? 'token') AND NOT (after ? 'hash')) FROM events WHERE type='agent_key.scopes_changed'`, owner.ID, key.ID).Scan(&complete)
		if returnErr == nil && !complete {
			t.Error("scope audit lacks actor, key, or before/after")
		}
		return returnErr
	}); err != nil {
		t.Fatal(err)
	}
	call(`{"add":["harness.worker"]}`, 200) // No-op creates no audit.
	call(`{"add":["nodes.write"]}`, 403)    // Dedicated roles are ceilings, too.
	call(`{"add":["keys.manage"]}`, 400)
	call(`{"add":["unknown.scope"]}`, 400)
	call(`{"add":["nodes.read"],"remove":["nodes:read"]}`, 400)
	call(`{}`, 400)
	call(`{"add":null}`, 400)
	call(`{"scopes":["nodes.read"]}`, 400)
	call(`{"add":[]} {}`, 400)
	_, afterEvents := keyCounts(t, m, owner)
	if afterEvents != events {
		t.Fatal("denied or no-op edit created an event")
	}
	w := scopesRequest(m, owner, key.ID, "GET", "")
	if w.Code != 200 {
		t.Fatalf("scope metadata = %d", w.Code)
	}
	var view keyScopeView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil || !slices.Contains(view.Grantable, "harness.worker") || slices.Contains(view.Grantable, "nodes.write") {
		t.Fatal("incorrect scope ceiling")
	}
	call(`{"remove":["nodes.read","harness.worker","events.read"]}`, 200)
	check(false)
}

func TestKeyScopeEditorAndOriginalCreatorIntersection(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "scope-worker", "scopes": []string{"nodes.read", "nodes.write"}}))
	ctx := dbtest.Seed(t.Context())
	var editor tenant.Principal
	editor.Kind, editor.TenantID = tenant.Person, owner.TenantID
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Key editor') RETURNING id::text`, owner.TenantID).Scan(&editor.ID); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'key_editor','Key editor') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1::uuid,$2::uuid,unnest(ARRAY['keys.manage','nodes.read'])`, owner.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, editor.ID, role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, editor, key.ID, "PATCH", `{"remove":["nodes.write"]}`); w.Code != 200 {
		t.Fatalf("restriction status %d", w.Code)
	}
	if w := scopesRequest(m, editor, key.ID, "PATCH", `{"add":["nodes.write"]}`); w.Code != 403 {
		t.Fatalf("editor escalated: %d", w.Code)
	}
	// Keep an owner editor, but cap the key's original creator at the narrower role.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET created_by_principal_id=$2 WHERE id=$1`, key.ID, editor.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, owner, key.ID, "PATCH", `{"add":["nodes.write"]}`); w.Code != 403 {
		t.Fatalf("original creator ceiling bypass: %d", w.Code)
	}
	// Permission revocation is checked again inside the mutation transaction.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE permission='keys.manage' AND role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$1)`, editor.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), editor), editor, key.ID, &keyScopeDelta{Remove: []string{"nodes.read"}}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("stale editor allowed: %v", err)
	}
}

func TestKeyScopeIsolationInactiveAndAgentDenial(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "scope-worker", "scopes": []string{"nodes.read"}}))
	agent := tenant.Principal{ID: key.PrincipalID, TenantID: owner.TenantID, Kind: tenant.Agent, Scopes: []string{"keys.manage"}}
	for _, method := range []string{"GET", "PATCH"} {
		for _, id := range []string{key.ID, "00000000-0000-4000-8000-000000000000", "invalid"} {
			w := scopesRequest(m, agent, id, method, `{"add":["nodes.write"]}`)
			if w.Code != 403 || strings.Contains(w.Body.String(), "scope") || strings.Contains(w.Body.String(), key.ID) {
				t.Fatal("agent got key information")
			}
			if w := scopesRequest(m, tenant.Principal{}, id, method, `{}`); w.Code != 401 {
				t.Fatalf("anonymous status %d", w.Code)
			}
		}
	}
	foreign := tenant.Principal{Kind: tenant.Person}
	seed := dbtest.Seed(t.Context())
	if err := db.InTenant(seed, m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(seed, `INSERT INTO tenants(slug,name) VALUES('scope-foreign','Foreign') RETURNING id::text`).Scan(&foreign.TenantID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(seed, m.pool, foreign.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(seed, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Foreign owner',ARRAY['super_admin']) RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(seed, tx, foreign.TenantID, foreign.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, foreign, key.ID, "PATCH", `{"add":["nodes.read"]}`); w.Code != 404 {
		t.Fatalf("foreign key status %d", w.Code)
	}
	if w := scopesRequest(m, owner, "00000000-0000-4000-8000-000000000000", "PATCH", `{"add":["nodes.read"]}`); w.Code != 404 {
		t.Fatalf("missing key status %d", w.Code)
	}
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET expires_at=now()-interval '1 second' WHERE id=$1`, key.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, owner, key.ID, "PATCH", `{"remove":["nodes.read"]}`); w.Code != 409 {
		t.Fatalf("expired key status %d", w.Code)
	}
	if err := m.revokeAgentKey(tenant.WithPrincipal(t.Context(), owner), owner, key.ID); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, owner, key.ID, "GET", ``); w.Code != 409 {
		t.Fatalf("revoked key status %d", w.Code)
	}
}

func TestKeyScopeConcurrentDeltasPreserveBothChanges(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "scope-worker", "scopes": []string{"nodes.read", "harness.worker", "events.read"}}))
	if w := scopesRequest(m, owner, key.ID, "PATCH", `{"remove":["harness.worker","events.read"]}`); w.Code != 200 {
		t.Fatalf("setup %d", w.Code)
	}
	var wg sync.WaitGroup
	for _, scope := range []string{"harness.worker", "events.read"} {
		wg.Add(1)
		go func(scope string) {
			defer wg.Done()
			if w := scopesRequest(m, owner, key.ID, "PATCH", `{"add":["`+scope+`"]}`); w.Code != 200 {
				t.Errorf("concurrent edit %d", w.Code)
			}
		}(scope)
	}
	wg.Wait()
	view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
	if err != nil || len(view.Key.Scopes) != 3 {
		t.Fatal("concurrent delta lost a scope")
	}
}

func TestKeyScopesThroughBearerMiddleware(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "scope-worker", "scopes": []string{"events.read"}}))
	mux := http.NewServeMux()
	m.Mount(mux)
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	secured := m.Middleware(mux)
	request := func(method, path string, signed bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"add":["nodes.read"]}`))
		if signed {
			r.Header.Set("Authorization", "Bearer "+key.Token)
		}
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/events", true); w.Code != 204 {
		t.Fatalf("initial auth %d", w.Code)
	}
	if w := scopesRequest(m, owner, key.ID, "PATCH", `{"remove":["events.read"]}`); w.Code != 200 {
		t.Fatalf("scope edit %d", w.Code)
	}
	w := request("GET", "/api/events", true)
	var body map[string]any
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != 403 || body["reason_code"] != "missing_key_scope" || body["scope"] != "events.read" {
		t.Fatal("middleware lost scope denial diagnostics")
	}
	if body["scope_label"] != "See history" || body["scope_code"] == nil || !strings.Contains(body["error"].(string), "events.read (See history)") {
		t.Fatal("authenticated scope error lost its label or proposal")
	}
	if w := request("GET", "/api/events", false); w.Code != 401 || strings.Contains(w.Body.String(), "scope") {
		t.Fatal("anonymous caller received a scope diagnostic")
	}
	for _, id := range []string{key.ID, "00000000-0000-4000-8000-000000000000"} {
		w := request("PATCH", "/api/agent-keys/"+id+"/scopes", true)
		if w.Code != 403 || strings.Contains(w.Body.String(), "reason_code") || strings.Contains(w.Body.String(), id) {
			t.Fatal("agent reached scope editor or disclosed target information")
		}
		if w := request("GET", "/api/agent-keys/"+id+"/scopes", false); w.Code != 401 || strings.Contains(w.Body.String(), "reason_code") {
			t.Fatal("anonymous caller received key diagnostics")
		}
	}
	if w := scopesRequest(m, owner, key.ID, "PATCH", `{"add":["events.read"]}`); w.Code != 200 {
		t.Fatalf("scope add %d", w.Code)
	}
	if w := request("GET", "/api/events", true); w.Code != 204 {
		t.Fatalf("same bearer did not recover immediately: %d", w.Code)
	}
}

func TestProjectAgentDenialsDoNotDiscloseNodeTargets(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "project-worker", "scopes": []string{"nodes.read"}}))
	ctx := dbtest.Seed(t.Context())
	var projectID, nodeID string
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,title,state,kind_id)
			SELECT $1::uuid,'PRJ-1','Project','active',id FROM node_kinds WHERE slug='project'
			RETURNING id::text`, owner.TenantID).Scan(&projectID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,title,parent_id,kind_id)
			SELECT $1::uuid,'PRJ-2','Ticket',$2::uuid,id FROM node_kinds WHERE slug='ticket'
			RETURNING id::text`, owner.TenantID, projectID).Scan(&nodeID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_key_aliases(tenant_id,key,node_id) VALUES($1::uuid,'OLD-1',$2::uuid)`, owner.TenantID, nodeID); err != nil {
			return err
		}
		// nodes.read is now granted only through this project's role.
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET scope_type='project',scope_id=$2::uuid WHERE principal_id=$1::uuid`, key.PrincipalID, projectID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, pattern := range []string{"GET /api/node-keys/{key}", "GET /api/nodes/{nodeId}"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if authz.RouteScope(r.Context()).ProjectID != projectID {
				t.Error("successful authorization lost the resolved project")
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	secured := m.Middleware(mux)
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+key.Token)
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		return w
	}
	targets := []struct{ existing, missing string }{
		{"/api/node-keys/PRJ-2", "/api/node-keys/PRJ-999"},
		{"/api/node-keys/OLD-1", "/api/node-keys/OLD-999"},
		{"/api/nodes/" + nodeID, "/api/nodes/00000000-0000-4000-8000-000000000000"},
	}
	for _, target := range targets {
		if w := request(target.existing); w.Code != http.StatusNoContent {
			t.Fatalf("project role and key scope did not authorize %s: %d", target.existing, w.Code)
		}
	}
	if w := scopesRequest(m, owner, key.ID, http.MethodPatch, `{"remove":["nodes.read"]}`); w.Code != http.StatusOK {
		t.Fatalf("remove key scope: %d", w.Code)
	}
	for _, target := range targets {
		t.Run(target.existing, func(t *testing.T) {
			existing, missing := request(target.existing), request(target.missing)
			if existing.Code != http.StatusForbidden || missing.Code != http.StatusForbidden {
				t.Fatalf("denied target statuses: existing=%d missing=%d", existing.Code, missing.Code)
			}
			if !bytes.Equal(existing.Body.Bytes(), missing.Body.Bytes()) {
				t.Fatalf("target existence changed denial: existing=%s missing=%s", existing.Body.String(), missing.Body.String())
			}
			var body map[string]any
			if json.Unmarshal(existing.Body.Bytes(), &body) != nil || body["reason_code"] != "missing_role_permission" || body["scope"] != nil {
				t.Fatal("denial did not retain the pre-resolution diagnostic")
			}
			if existing.Header().Get("Cache-Control") != "no-store" || missing.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("denial is cacheable")
			}
		})
	}
}
