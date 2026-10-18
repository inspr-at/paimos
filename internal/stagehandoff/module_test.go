// SPDX-License-Identifier: AGPL-3.0-only
package stagehandoff

import (
	"github.com/inspr-at/paimos/internal/tenant"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetiredHandoffsNeverTouchStorageOrAdmitLaunch(t *testing.T) {
	mux := http.NewServeMux()
	New(nil, nil).Mount(mux)
	for _, tc := range []struct{ method, path string }{{"POST", "/api/stage-handoffs"}, {"GET", "/api/stage-handoffs/h"}, {"POST", "/api/stage-handoffs/h/evidence"}, {"POST", "/api/stage-handoffs/h/result"}, {"POST", "/api/stage-handoffs/h/launch/admit"}, {"POST", "/api/stage-handoffs/h/launch/consume"}, {"POST", "/api/stage-handoffs/h/classic-batch-alias"}, {"POST", "/api/projects/p/baseline-batches/batches/1/built-receipt"}, {"GET", "/api/projects/p/releases/r/candidate-artifact"}, {"PUT", "/api/projects/p/releases/r/candidate-artifact"}} {
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		request = request.WithContext(tenant.WithPrincipal(request.Context(), tenant.Principal{ID: "agent", TenantID: "tenant", Kind: tenant.Agent}))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != 410 || !strings.Contains(response.Body.String(), "The INSPR Flow is retired.") {
			t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
		}
	}
}
