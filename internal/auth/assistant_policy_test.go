// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: a compromised full-access assistant key bypasses policy through ordinary
// node APIs, or list RLS leaks projects outside the policy allowlist.
func TestAssistantPolicyDirectAPICeilingAndRLS(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "Policy ceiling fixture", "full_access": true}))
	var allowed, hidden string
	policy := assistants.DefaultPolicy()
	policy.AllowedScopes = []string{"nodes.read", "inbox.read"}
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	seed(func(tx pgx.Tx) error {
		// Full access is a key ceiling, not a role grant. Give the generated
		// role explicit content authority before testing the policy ceiling.
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT b.tenant_id,b.role_id,p.permission FROM role_bindings b CROSS JOIN unnest($3::text[]) AS p(permission) WHERE b.tenant_id=$1 AND b.principal_id=$2 AND b.scope_type='workspace' ON CONFLICT DO NOTHING`, owner.TenantID, key.PrincipalID, []string{"nodes.read", "nodes.write", "inbox.read"}); err != nil {
			return err
		}
		for _, id := range []*string{&allowed, &hidden} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Policy visibility fixture' FROM node_kinds WHERE slug='project' RETURNING id::text`, owner.TenantID).Scan(id); err != nil {
				return err
			}
		}
		policy.AllowedProjects = []string{allowed}
		raw, _ := json.Marshal(policy)
		if _, err := tx.Exec(t.Context(), `UPDATE principals SET agent_class='assistant' WHERE id=$1`, key.PrincipalID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO assistant_profiles(tenant_id,principal_id,owner_person_id,vendor,policy) VALUES($1,$2,$3,'grok',$4)`, owner.TenantID, key.PrincipalID, owner.ID, raw)
		return err
	})
	entered := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/nodes", func(w http.ResponseWriter, r *http.Request) { entered = true; w.WriteHeader(204) })
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, r *http.Request) {
		var ids []string
		err := db.InTenant(r.Context(), m.pool, owner.TenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(r.Context(), `SELECT id::text FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id`, []string{allowed, hidden})
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(ids)
	})
	call := func(method string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/nodes", nil)
		r.Header.Set("Authorization", "Bearer "+key.Token)
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		m.Middleware(mux).ServeHTTP(w, r)
		return w
	}
	w := call("GET")
	var visible []string
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &visible) != nil || !slices.Equal(visible, []string{allowed}) {
		t.Fatalf("policy RLS visible=%v status=%d", visible, w.Code)
	}
	w = call("POST")
	var denial struct {
		Reason string `json:"reason_code"`
	}
	if w.Code != 403 || json.Unmarshal(w.Body.Bytes(), &denial) != nil || denial.Reason != "assistant_scope_denied" || entered {
		t.Fatalf("full-key bypass status=%d reason=%s entered=%v", w.Code, denial.Reason, entered)
	}
	seed(func(tx pgx.Tx) error {
		var receipts, events int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM assistant_policy_receipts WHERE principal_id=$1 AND operation='permission' AND NOT allowed`, key.PrincipalID).Scan(&receipts); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='assistant.policy_evaluated' AND after->>'principal_id'=$1 AND after->>'allowed'='false'`, key.PrincipalID).Scan(&events); err != nil {
			return err
		}
		if receipts != 1 || events != 1 {
			t.Fatalf("denial receipts=%d events=%d", receipts, events)
		}
		return nil
	})
	policy.AllowedScopes = append(policy.AllowedScopes, "nodes.write")
	seed(func(tx pgx.Tx) error {
		raw, _ := json.Marshal(policy)
		_, err := tx.Exec(t.Context(), `UPDATE assistant_profiles SET policy=$2,revision=revision+1 WHERE principal_id=$1`, key.PrincipalID, raw)
		return err
	})
	if w := call("POST"); w.Code != 204 || !entered {
		t.Fatalf("allowed within role+policy: %d", w.Code)
	}
	// Policy is still only a ceiling: removing ordinary grants closes the route.
	entered = false
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, key.PrincipalID)
		return err
	})
	if w := call("POST"); w.Code != 403 || entered {
		t.Fatalf("policy became role grant: %d", w.Code)
	}
}
