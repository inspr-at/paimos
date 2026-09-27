// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

// Exercise the production outer auth allowlist, route permissions, key ceiling,
// actual price handler and tenant RLS together (direct handler tests miss it).
func TestModelPricesThroughProductionAuth(t *testing.T) {
	f := fixture(t)
	// Direct-handler fixtures use an opaque prefix; real authentication routes
	// through the tenant encoded in the key prefix.
	keyParts := strings.Split(f.key, "_")
	prefix := strings.ReplaceAll(f.agent.TenantID, "-", "") + "0123456789abcdef"
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET prefix=$1 WHERE principal_id=$2`, prefix, f.agent.ID)
		return err
	})
	f.key = "aeon_" + prefix + "_" + keyParts[2]
	expect(t, usagePrice(t, f, 1, "2.5"), 201)
	dbtest.BindRole(t, f.db, f.foreign.TenantID, f.foreign.ID, "admin")
	expect(t, f.call(f.foreign, "POST", "/api/model-prices", map[string]any{"model": "foreign-model", "version": 1,
		"input_usd_per_million": "99", "output_usd_per_million": "99", "cached_input_usd_per_million": "99"}, ""), 201)
	mod, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	server := &httpapi.Server{Mux: f.mux, Pool: f.db.App, Middleware: []func(http.Handler) http.Handler{mod.Middleware}}
	handler := server.Handler()
	for _, tc := range []struct {
		name, method, path string
		scopes             []string
		status             int
	}{
		{"agent read", "GET", "/api/model-prices", []string{"harness.read"}, 200},
		{"agent head", "HEAD", "/api/model-prices", []string{"harness.read"}, 200},
		{"missing read scope", "GET", "/api/model-prices", []string{"models.read"}, 403},
		{"worker is not reader", "HEAD", "/api/model-prices", []string{"harness.worker"}, 403},
		{"empty scope", "GET", "/api/model-prices", []string{}, 403},
		{"person only write", "POST", "/api/model-prices", []string{"harness.read", "models.manage"}, 403},
		{"unlisted child", "GET", "/api/model-prices/anything", []string{"harness.read"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=$1 WHERE principal_id=$2`, tc.scopes, f.agent.ID)
				return err
			})
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+f.key)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			expect(t, w, tc.status)
			if tc.status == 200 && tc.method == "GET" {
				items := decode(t, w)["items"].([]any)
				if len(items) != 1 || items[0].(map[string]any)["model"] != "test-model" {
					t.Fatal("tenant price isolation lost")
				}
			}
		})
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/model-prices", nil))
	expect(t, w, 401)
}
