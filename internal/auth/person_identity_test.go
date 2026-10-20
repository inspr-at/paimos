// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: authentication silently substitutes issuer provenance for an explicit
// person, or key response projections discard the new binding.
func TestKeyPersonBindingIsReadWithoutIssuerFallback(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "Identity key", "scopes": []string{"nodes.read"}}))
	prefix, secret, _ := parseBearer("Bearer " + key.Token)
	p, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok || p.KeyPersonID != "" || key.PersonID != nil {
		t.Fatal("unbound key inferred a person")
	}
	var person string
	err = db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Explicit person') RETURNING id::text`, owner.TenantID).Scan(&person); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET person_id=$2 WHERE id=$1`, key.ID, person)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	p, ok, err = m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok || p.KeyPersonID != person || p.KeyCreatorID != owner.ID {
		t.Fatal("explicit person was lost or conflated with issuer")
	}
	keys, err := m.listAgentKeys(t.Context(), owner.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range keys {
		if rec.ID == key.ID {
			raw, err := json.Marshal(keyJSON(rec))
			if err != nil {
				t.Fatal(err)
			}
			var response map[string]any
			if err = json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if response["person_id"] != person {
				t.Fatal("key response omitted explicit person")
			}
			return
		}
	}
	t.Fatal("key not listed")
}
