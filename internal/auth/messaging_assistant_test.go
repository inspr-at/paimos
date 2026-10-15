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
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func assistantFixture(t *testing.T) (*Module, tenant.Principal, time.Time) {
	t.Helper()
	m, p := keyFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	m.assistantNow = func() time.Time { return now }
	// Real System notice acceptance is exercised in inbox's production-auth test.
	m.AssistantNoticeTx = func(context.Context, pgx.Tx, string, string, string, string) error { return nil }
	return m, p, now
}

func assistantHTTP(m *Module, p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	m.Mount(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func assistantAgent(t *testing.T, m *Module, owner tenant.Principal, full bool) tenant.Principal {
	t.Helper()
	rec, err := m.createAgentKey(tenant.WithPrincipal(t.Context(), owner), owner, "Coordinator", "", authz.CoordinatorKeyScopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if full {
		if err := m.inTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET full_access=true,scopes='{}' WHERE id=$1::uuid`, rec.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	parts := strings.Split(rec.Token, "_")
	p, ok, err := m.authenticateAgent(t.Context(), parts[1], parts[2])
	if err != nil || !ok {
		t.Fatal("coordinator fixture did not authenticate")
	}
	return p
}

// Risk: a narrow provisioning exception delegates general creation, exceeds
// its quota/expiry/owner ceiling, or lets general key editing bypass renewal.
func TestMessagingAssistantProvisioningBounds(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "coordinator", true: "full access"}[full], func(t *testing.T) {
			m, owner, now := assistantFixture(t)
			agent := assistantAgent(t, m, owner, full)
			expires := now.Add(30 * 24 * time.Hour)
			first, err := m.messagingAssistant(t.Context(), owner, "create", "", assistantInput{Name: "Amy", ExpiresAt: &expires})
			if err != nil {
				t.Fatal(err)
			}
			second, err := m.messagingAssistant(t.Context(), agent, "create", "", assistantInput{Name: "Helper", ExpiresAt: &expires})
			if err != nil {
				t.Fatal(err)
			}
			if first.Token == "" || second.Token == "" || second.CreatedByPrincipalID == nil || *second.CreatedByPrincipalID != owner.ID || second.FullAccess || !slices.Equal(second.Scopes, authz.MessagingAssistantScopes()) {
				t.Fatal("key/owner ceiling differs")
			}
			if _, err := m.messagingAssistant(t.Context(), agent, "create", "", assistantInput{Name: "amy", ExpiresAt: &expires}); !errors.Is(err, errAssistantName) {
				t.Fatalf("duplicate name: %v", err)
			}
			rotated, err := m.messagingAssistant(t.Context(), agent, "rotate", second.PrincipalID, assistantInput{ExpiresAt: &expires})
			if err != nil {
				t.Fatal(err)
			}
			if rotated.Token == second.Token || rotated.ID == second.ID || !slices.Equal(rotated.Scopes, authz.MessagingAssistantScopes()) {
				t.Fatal("rotation reused or widened key")
			}
			if err := m.inTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
				var roles int
				var marked, revoked bool
				var storedOwner string
				if err := tx.QueryRow(t.Context(), `SELECT coalesce(p.messaging_only,false),p.messaging_owner_id::text,(SELECT count(*) FROM role_bindings WHERE principal_id=p.id),(SELECT revoked_at IS NOT NULL FROM agent_keys WHERE id=$2::uuid) FROM principals p WHERE p.id=$1::uuid`, second.PrincipalID, second.ID).Scan(&marked, &storedOwner, &roles, &revoked); err != nil {
					return err
				}
				if !marked || storedOwner != owner.ID || roles != 0 || !revoked {
					t.Fatal("marker, no-role, ownership or old-key revocation failed")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if w := keyRequest(m, owner, map[string]any{"principal_id": first.PrincipalID, "full_access": true}); w.Code != 403 {
				t.Fatalf("general key creation bypass: %d", w.Code)
			}
			if w := keyRequest(m, owner, map[string]any{"rotate_key_id": first.ID}); w.Code != 403 {
				t.Fatalf("general rotation bypass: %d", w.Code)
			}
			for _, delta := range []*keyScopeDelta{{FullAccess: new(true)}, {ExpiresAt: json.RawMessage("null")}, {Add: []string{"nodes.read"}}} {
				if _, err := m.agentKeyScopes(t.Context(), owner, first.ID, delta); !errors.Is(err, authz.ErrForbidden) {
					t.Fatalf("scope/expiry bypass: %v", err)
				}
			}
			third, err := m.messagingAssistant(t.Context(), agent, "create", "", assistantInput{Name: "Third", ExpiresAt: &expires})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.messagingAssistant(t.Context(), owner, "create", "", assistantInput{Name: "Fourth", ExpiresAt: &expires}); !errors.Is(err, errAssistantLimit) {
				t.Fatalf("quota: %v", err)
			}
			if _, err := m.messagingAssistant(t.Context(), owner, "revoke", third.PrincipalID, assistantInput{}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.messagingAssistant(t.Context(), agent, "create", "", assistantInput{Name: "Replacement", ExpiresAt: &expires}); err != nil {
				t.Fatal(err)
			}
			// An unrelated person with both rights cannot renew/revoke this owner’s assistant.
			other := owner
			if err := m.inTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Other owner','{}') RETURNING id::text`, owner.TenantID).Scan(&other.ID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE key='admin'`, owner.TenantID, other.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for _, op := range []string{"rotate", "revoke"} {
				if _, err := m.messagingAssistant(t.Context(), other, op, first.PrincipalID, assistantInput{ExpiresAt: &expires}); !errors.Is(err, errNotFound) {
					t.Fatalf("foreign owner %s: %v", op, err)
				}
			}
		})
	}
	t.Run("expiry and fixed HTTP input", func(t *testing.T) {
		m, owner, now := assistantFixture(t)
		beforeKeys, beforeEvents := keyCounts(t, m, owner)
		for _, raw := range []string{`{"name":"Amy"}`, `{"name":"Amy","expires_at":null}`, `{"name":"Amy","expires_at":"` + now.Format(time.RFC3339) + `"}`, `{"name":"Amy","expires_at":"` + now.Add(30*24*time.Hour+time.Second).Format(time.RFC3339) + `"}`, `{"name":"Amy","scopes":["nodes.read"]}`, `{"name":"Amy","full_access":true}`, `{"name":"Amy","description":"` + strings.Repeat("x", 4096) + `"}`} {
			if w := assistantHTTP(m, owner, "POST", "/api/agents/assistants", raw); w.Code != 400 {
				t.Fatalf("invalid input status %d", w.Code)
			}
		}
		if keys, events := keyCounts(t, m, owner); keys != beforeKeys || events != beforeEvents {
			t.Fatal("invalid requests wrote keys or events")
		}
		expires := now.Add(30 * 24 * time.Hour)
		body, _ := json.Marshal(assistantInput{Name: "Amy", ExpiresAt: &expires})
		key := decodeKey(t, assistantHTTP(m, owner, "POST", "/api/agents/assistants", string(body)))
		if key.ExpiresAt == nil || !key.ExpiresAt.Equal(expires) {
			t.Fatal("30-day boundary rejected or changed")
		}
	})
	t.Run("notice failure rolls back and withholds secret", func(t *testing.T) {
		m, owner, now := assistantFixture(t)
		beforeKeys, beforeEvents := keyCounts(t, m, owner)
		failure := errors.New("notice unavailable")
		m.AssistantNoticeTx = func(ctx context.Context, tx pgx.Tx, _ string, _ string, _ string, body string) error {
			if strings.Contains(body, "aeon_") {
				t.Fatal("notice includes credential")
			}
			if _, err := events.Append(ctx, tx, owner, events.Change{Type: "test.notice"}); err != nil {
				return err
			}
			return failure
		}
		expires := now.Add(time.Hour)
		rec, err := m.messagingAssistant(t.Context(), owner, "create", "", assistantInput{Name: "Rolled back", ExpiresAt: &expires})
		if !errors.Is(err, failure) || rec.Token != "" {
			t.Fatal("failed notice returned secret or wrong failure")
		}
		if keys, events := keyCounts(t, m, owner); keys != beforeKeys || events != beforeEvents {
			t.Fatal("notice failure committed keys or events")
		}
		if err := m.inTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM principals WHERE name='Rolled back'`).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("notice failure committed principal")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// Risk: key metadata substitutes for current workspace/owner authority. Each
// missing right is removed independently; a prior successful check is retained.
func TestMessagingAssistantLiveOwnerAndCallerBounds(t *testing.T) {
	for _, missing := range []string{"agents.create", "keys.manage", "coordinator grant", "owner", "messaging marker"} {
		t.Run(missing, func(t *testing.T) {
			m, owner, now := assistantFixture(t)
			agent := assistantAgent(t, m, owner, false)
			ctx := tenant.WithPrincipal(t.Context(), agent)
			if err := authz.RequirePattern(authz.BindPool(ctx, m.pool), "POST /api/agents/assistants", authz.Scope{}); err != nil {
				t.Fatal(err)
			}
			if err := m.inTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
				if err := db.LockTenant(t.Context(), tx, owner.TenantID); err != nil {
					return err
				}
				switch missing {
				case "agents.create", "keys.manage":
					var role string
					if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'bounded_owner','Bounded owner') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1::uuid,$2::uuid,unnest($3::text[]);`, owner.TenantID, role, []string{"agents.create", "keys.manage", "harness.read", "harness.write", "harness.worker", "inbox.read", "inbox.send", "nodes.read", "work_orders.read"})
					if err != nil {
						return err
					}
					if _, err = tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE role_id=$1::uuid AND permission=$2`, role, missing); err != nil {
						return err
					}
					// Retain another Owner so demotion exercises missing rights, not the last-owner safeguard.
					var spare string
					if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Spare owner','{}') RETURNING id::text`, owner.TenantID).Scan(&spare); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE key='owner'`, owner.TenantID, spare); err != nil {
						return err
					}
					_, err = tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2::uuid WHERE principal_id=$1::uuid AND scope_type='workspace'`, owner.ID, role)
					return err
				case "coordinator grant":
					_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$1::uuid) AND permission='harness.worker'`, agent.ID)
					return err
				case "owner":
					_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=NULL,person_owner_required=false WHERE id=$1::uuid`, agent.KeyID)
					return err
				default:
					_, err := tx.Exec(t.Context(), `UPDATE principals SET messaging_only=true,messaging_owner_id=$2::uuid WHERE id=$1::uuid`, agent.ID, owner.ID)
					return err
				}
			}); err != nil {
				t.Fatal(err)
			}
			expires := now.Add(time.Hour)
			_, err := m.messagingAssistant(ctx, agent, "create", "", assistantInput{Name: "Denied", ExpiresAt: &expires})
			if !errors.Is(err, authz.ErrForbidden) && !errors.Is(err, db.ErrKeyAuthorityChanged) {
				t.Fatalf("missing %s allowed or wrong refusal: %v", missing, err)
			}
			if missing == "agents.create" || missing == "keys.manage" {
				body, _ := json.Marshal(assistantInput{Name: "Denied person", ExpiresAt: &expires})
				if out := assistantHTTP(m, owner, "POST", "/api/agents/assistants", string(body)); out.Code != 403 {
					t.Fatalf("person without %s HTTP status %d", missing, out.Code)
				}
			}
			if err := m.inTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
				var n int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM principals WHERE name='Denied'`).Scan(&n); err != nil {
					return err
				}
				if n != 0 {
					t.Fatal("denied provision persisted")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Risk: concurrent requests both observe the last free quota slot.
func TestMessagingAssistantQuotaSerializes(t *testing.T) {
	m, owner, now := assistantFixture(t)
	expires := now.Add(time.Hour)
	for _, name := range []string{"One", "Two"} {
		if _, err := m.messagingAssistant(t.Context(), owner, "create", "", assistantInput{Name: name, ExpiresAt: &expires}); err != nil {
			t.Fatal(err)
		}
	}
	observer := m.pool
	pool, barrier, guard := dbtest.BarrierPool(t, m.pool, func(sql string) bool { return strings.Contains(sql, "LIMIT 3) active") })
	m.pool = pool
	results := make(chan error, 2)
	go func() {
		_, err := m.messagingAssistant(guard, owner, "create", "", assistantInput{Name: "Three", ExpiresAt: &expires})
		results <- err
	}()
	holder := barrier.Wait(t, guard)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := m.messagingAssistant(guard, owner, "create", "", assistantInput{Name: "Four", ExpiresAt: &expires})
		results <- err
	}()
	if lock := dbtest.BlockedOrDone(t, guard, observer, holder, done); lock != "transactionid" {
		t.Fatalf("quota writer failed to serialize: %q", lock)
	}
	barrier.Release()
	var successes, limited int
	for range 2 {
		err := dbtest.Await(t, guard, results)
		if err == nil {
			successes++
		} else if errors.Is(err, errAssistantLimit) {
			limited++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || limited != 1 {
		t.Fatalf("quota results success=%d limited=%d", successes, limited)
	}
}
