// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Handler-level, with an injected non-person and no authentication middleware.
func TestPoliciesReleaseWriteHandlerPersonRequired(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	p := tenant.Principal{ID: id, TenantID: id, Kind: tenant.Agent}
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	for _, tc := range []struct{ method, path string }{{"PUT", "/api/projects/" + id + "/releases/" + id + "/plan"}, {"POST", "/api/projects/" + id + "/releases/" + id + "/membership"}} {
		r := httptest.NewRequest(tc.method, tc.path, nil).WithContext(tenant.WithPrincipal(t.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 403 || out["error"] != "person required" {
			t.Fatalf("wrong handler refusal %d %s", w.Code, w.Body.String())
		}
	}
}
