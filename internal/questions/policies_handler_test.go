// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Handler-level: an injected person does not neutralize a bearer header.
// No authentication middleware is involved in this test.
func TestPoliciesQuestionHandlerPersonWithAuthorizationRefused(t *testing.T) {
	p := tenant.Principal{ID: "00000000-0000-4000-8000-000000000001", TenantID: "00000000-0000-4000-8000-000000000002", Kind: tenant.Person}
	r := httptest.NewRequest("POST", "/api/questions/00000000-0000-4000-8000-000000000003/decision", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	r.Header.Set("Authorization", "Bearer fixture-header")
	w := httptest.NewRecorder()
	(&Module{}).handleDecide(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 403 || out["code"] != "person_required" {
		t.Fatalf("wrong handler refusal %d %s", w.Code, w.Body.String())
	}
}
