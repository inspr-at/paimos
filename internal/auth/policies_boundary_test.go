// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/jackc/pgx/v5"
)

// These are bearer-through-middleware pins. Handler-only tests are deliberately
// separate: the scope ceiling stops these requests before a person check runs.
func TestPoliciesBearerMiddlewareRefusalsAndLadderRead(t *testing.T) {
	m, owner := keyFixture(t)
	var scopes []string
	for _, permission := range authz.Registry {
		if permission.AgentGrantable {
			scopes = append(scopes, permission.Key)
		}
	}
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "policies-broadest-key", "scopes": scopes}))
	for _, permission := range []string{"rules.publish", "questions.decide", "roles.manage", "approvals.decide"} {
		entry, ok := authz.Lookup(permission)
		if !ok || entry.AgentGrantable {
			t.Fatalf("human governance scope became grantable: %s", permission)
		}
		if _, err := NormalizeScopes([]string{permission}); err == nil {
			t.Fatalf("key grant accepted %s", permission)
		}
	}
	mux := http.NewServeMux()
	modelregistry.New(m.pool).Mount(mux)
	authz.New(m.pool).Mount(mux)
	questions.New(m.pool).Mount(mux)
	releases.New(m.pool).Mount(mux)
	rules.New(m.pool).Mount(mux)
	doctrine.New(m.pool, doctrine.Options{}).Mount(mux)
	entered := false
	secured := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered = true; mux.ServeHTTP(w, r) }))
	counts := func() (int, int) {
		t.Helper()
		var events, proposals int
		if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM events),(SELECT count(*) FROM doctrine_proposals)`).Scan(&events, &proposals)
		}); err != nil {
			t.Fatal(err)
		}
		return events, proposals
	}
	beforeEvents, beforeProposals := counts()
	const id = "00000000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"ladder reads are allowed", "GET", "/api/models/routes?role=review-gate", "", 200},
		{"permissions person session only", "GET", "/api/authz/permissions", "", 403},
		{"question decision middleware", "POST", "/api/questions/" + id + "/decision", `{"option_id":"a"}`, 403},
		{"rules publish middleware", "POST", "/api/rules/sets/" + id + "/publish", `{"expected_revision":1,"version":"261002210000.0.0"}`, 403},
		{"rules restore middleware", "POST", "/api/rules/sets/" + id + "/restore", `{}`, 403},
		{"release plan middleware", "PUT", "/api/projects/" + id + "/releases/" + id + "/plan", `{"expected_revision":1,"ordered_ticket_ids":[]}`, 403},
		{"release membership middleware", "POST", "/api/projects/" + id + "/releases/" + id + "/membership", `{"revision":1,"ids":[]}`, 403},
		{"unlocked doctrine middleware", "POST", "/api/rules/doctrine/inbox", `{"rule_key":"unlocked","source":"ordinary proposal"}`, 403},
		{"locked doctrine middleware", "POST", "/api/rules/doctrine/inbox", `{"rule_key":"locked","source":"locked proposal"}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered = false
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+key.Token)
			_, r.Pattern = mux.Handler(r)
			w := httptest.NewRecorder()
			secured.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("unexpected status %d", w.Code)
			}
			var answer map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
				t.Fatal("invalid response")
			}
			if tc.status == 403 {
				if entered || answer["error"] != "agent key scope required" {
					t.Fatal("refusal moved away from middleware scope ceiling")
				}
			} else if !entered || answer["setup"] != false || answer["truncated"] != false {
				t.Fatal("agent did not read the unseeded bounded ladder")
			}
			events, proposals := counts()
			if events != beforeEvents || proposals != beforeProposals {
				t.Fatal("refused action or ladder read mutated data")
			}
		})
	}
}
