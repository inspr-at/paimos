// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestCapacityReadingUsesProbeScope(t *testing.T) {
	id := "/api/agent-accounts/11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct{ method, path, scope string }{
		{"GET", id + "/readings", "account.probe"},
		{"POST", id + "/readings", "account.probe"},
		{"PUT", id + "/signals", "account.probe"},
		{"GET", id + "/statusline", "account.probe"},
		{"POST", id + "/quota-key", "account.probe"},
	} {
		scope, ok := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if !ok || scope != tc.scope {
			t.Fatalf("%s %s: %s %v", tc.method, tc.path, scope, ok)
		}
	}
	if scope, ok := coreAgentScope(httptest.NewRequest("PUT", id+"/statusline", nil)); ok || scope != "" {
		t.Fatal("agent statusline opt-in")
	}
}

// Risk: production paired keys cannot claim or publish catalogs, or admitting
// those two operations bypasses the scope and permanent pairing ceilings.
func TestHarnessProbeRoutesThroughPairedRuntimeMiddleware(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "Catalog daemon", "scopes": agentpairing.RuntimePermissions}))
	var computer string
	if err := db.InTenant(t.Context(), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state)
 VALUES($1,gen_random_uuid(),'115211521',$2,$2,$2,'{}','synthetic','redeemed') RETURNING id::text`, owner.TenantID, strings.Repeat("a", 64)).Scan(&computer); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,setup_state)
 VALUES($1,$2,$2,$3,$4,'catalog-daemon',$5,'connected')`, owner.TenantID, computer, key.PrincipalID, key.ID, strings.Repeat("b", 64))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	// Mount the intersecting retirement route as production does. Probe
	// publication uses a guarded fallback, while retirement stays person-only.
	mux.HandleFunc("POST /api/models/{id}/retire", func(http.ResponseWriter, *http.Request) {
		t.Fatal("paired runtime key reached person-only retirement")
	})
	reached := 0
	for _, pattern := range []string{"POST /api/models/harness-probes/claim", "POST /api/models/{collection}/{accountId}"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.PrincipalFrom(r.Context())
			if !ok || p.Kind != tenant.Agent || p.ID != key.PrincipalID || p.KeyID != key.ID {
				t.Fatal("probe route lost the authenticated runtime identity")
			}
			reached++
			w.WriteHeader(http.StatusNoContent)
		})
	}
	secured := m.Middleware(mux)
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+key.Token)
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		return w
	}
	paths := []string{"/api/models/harness-probes/claim", "/api/models/harness-probes/11111111-1111-4111-8111-111111111111"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			if w := request(http.MethodPost, path); w.Code != http.StatusNoContent {
				t.Fatalf("paired runtime route refused: HTTP %d: %s", w.Code, w.Body.String())
			}
		})
	}
	if reached != 2 {
		t.Fatal("both probe operations must reach their handlers")
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/models/harness-probes/not-a-uuid"},
		{http.MethodPost, paths[0] + "/extra"},
		{http.MethodDelete, paths[1]},
		{http.MethodPost, "/api/models/harness-probes/retire"},
		{http.MethodPost, "/api/models/other/11111111-1111-4111-8111-111111111111"},
		{http.MethodPost, paths[1] + "/retire"},
		{http.MethodPost, "/api/models/reports"},
	} {
		if w := request(route.method, route.path); w.Code != http.StatusForbidden {
			t.Fatalf("paired ceiling admitted %s %s: HTTP %d", route.method, route.path, w.Code)
		}
	}
	if w := scopesRequest(m, owner, key.ID, "PATCH", `{"remove":["models.report"]}`); w.Code != http.StatusOK {
		t.Fatal("fixture scope revocation failed")
	}
	for _, path := range paths {
		w := request(http.MethodPost, path)
		var body struct {
			ReasonCode string `json:"reason_code"`
			Scope      string `json:"scope"`
		}
		if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal("scope revocation did not deny probe operation")
		}
		if body.ReasonCode != "missing_key_scope" || body.Scope != "models.report" {
			t.Fatal("probe rejected for the wrong scope or reason")
		}
	}
	if w := scopesRequest(m, owner, key.ID, "PATCH", `{"add":["models.report"]}`); w.Code != http.StatusOK {
		t.Fatal("fixture scope restoration failed")
	}
	if err := db.InTenant(t.Context(), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_pairing_computers SET state='revoked' WHERE id=$1`, computer)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if w := request(http.MethodPost, path); w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "pairing_revoked") {
			t.Fatal("revoked pairing reached a catalog handler")
		}
	}
	if reached != 2 {
		t.Fatal("denied operations reached catalog handlers")
	}
}

func TestAccountLinkScopeAllowsOnlyPairedDeviceOperation(t *testing.T) {
	path := "/api/agent-pairing/account-link"
	if scope, ok := coreAgentScope(httptest.NewRequest("POST", path, nil)); !ok || scope != "account.probe" {
		t.Fatal("paired device operation has no probe scope")
	}
	for _, route := range []struct{ method, path string }{
		{"GET", path}, {"POST", path + "/lookup"}, {"POST", path + "/11111111-1111-4111-8111-111111111111/approve"},
		{"GET", "/api/agent-pairing/account-links"}, {"POST", "/api/agent-pairing/account-links/11111111-1111-4111-8111-111111111111/unlink"},
	} {
		if scope, ok := coreAgentScope(httptest.NewRequest(route.method, route.path, nil)); ok || scope != "" {
			t.Fatal("person ownership action exposed to an agent key")
		}
	}
}
