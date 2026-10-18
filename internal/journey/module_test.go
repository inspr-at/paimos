// SPDX-License-Identifier: AGPL-3.0-only
package journey

import (
	"github.com/inspr-at/paimos/internal/tenant"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetiredJourneyNeverTouchesStorage(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	for _, tc := range []struct{ method, path string }{{"GET", "/api/journey/next-actions"}, {"GET", "/api/projects/p/journey"}, {"PUT", "/api/projects/p/journey/profile"}, {"POST", "/api/projects/p/journey/actions"}} {
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		request = request.WithContext(tenant.WithPrincipal(request.Context(), tenant.Principal{ID: "agent", TenantID: "tenant", Kind: tenant.Agent}))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != 410 || response.Body.String() != "{\"error\":\"The INSPR Flow is retired.\"}\n" {
			t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
		}
	}
}
