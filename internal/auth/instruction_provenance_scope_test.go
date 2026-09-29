// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// The query is a real middleware decision: an agent key is rejected before
// the handler unless coreAgentScope names the route and the key holds
// harness.read. A person session is not subject to that agent ceiling.
func TestInstructionProvenanceQueryScopeAtMiddleware(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "provenance-scope", "Provenance scope")
	m := newMod(t, Config{})
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	path := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/instruction-provenance"
	call := func(method string, header http.Header) int {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		setPolicyPattern(req)
		for key, values := range header {
			req.Header[key] = values
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res.Code
	}

	reader, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "provenance-reader", "", []string{"harness.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerOnly, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "provenance-worker", "", []string{"harness.worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodGet, http.Header{"Authorization": {"Bearer " + reader.Token}}); got != http.StatusNoContent {
		t.Fatalf("agent with harness.read: %d", got)
	}
	if got := call(http.MethodHead, http.Header{"Authorization": {"Bearer " + reader.Token}}); got != http.StatusNoContent {
		t.Fatalf("HEAD with harness.read: %d", got)
	}
	if got := call(http.MethodPost, http.Header{"Authorization": {"Bearer " + reader.Token}}); got != http.StatusForbidden {
		t.Fatalf("read key gained a write: %d", got)
	}
	if got := call(http.MethodGet, http.Header{"Authorization": {"Bearer " + workerOnly.Token}}); got != http.StatusForbidden {
		t.Fatalf("agent without harness.read: %d", got)
	}

	personID, identityID := signinPerson(t, tenantID, "provenance-person", "Provenance person", "provenance@example.com", "provenance@example.com", "admin")
	token, err := m.startSession(t.Context(), identityID, tenantID, personID)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodGet, http.Header{"Cookie": {"aeon_session=" + token}}); got != http.StatusNoContent {
		t.Fatalf("person session: %d", got)
	}
}
