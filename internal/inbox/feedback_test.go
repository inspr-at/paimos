// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

func feedbackCall(t *testing.T, m *module, p tenant.Principal) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	m.Mount(mux)
	r := httptest.NewRequest("GET", "/api/inbox/feedback-recipient", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// AEON-312: feedback goes to a workspace owner other than the caller, and an
// only owner is told there is no one else (404), never pointed at themselves.
func TestFeedbackRecipient(t *testing.T) {
	w := newWorld(t)
	m := newModule(w.db.App)

	if got := feedbackCall(t, m, w.sender); got.Code != http.StatusNotFound {
		t.Fatalf("no owner yet: status %d: %s", got.Code, got.Body.String())
	}

	owner := insertPrincipal(t, w.db, w.sender.TenantID, tenant.Person, "Owner", []string{"super_admin"})
	got := feedbackCall(t, m, w.sender)
	if got.Code != http.StatusOK {
		t.Fatalf("status %d: %s", got.Code, got.Body.String())
	}
	var out FeedbackRecipient
	if err := json.Unmarshal(got.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.PrincipalID != owner.ID || out.Name != "Owner" {
		t.Fatalf("recipient %+v, want %s", out, owner.ID)
	}

	// The only owner has no one else to write to.
	if got := feedbackCall(t, m, owner); got.Code != http.StatusNotFound {
		t.Fatalf("only owner: status %d: %s", got.Code, got.Body.String())
	}
	// A second owner reaches the first, and the first the second.
	second := insertPrincipal(t, w.db, w.sender.TenantID, tenant.Person, "Second owner", []string{"super_admin"})
	for _, pair := range []struct {
		caller tenant.Principal
		want   string
	}{{owner, second.ID}, {second, owner.ID}} {
		caller, want := pair.caller, pair.want
		got := feedbackCall(t, m, caller)
		if err := json.Unmarshal(got.Body.Bytes(), &out); err != nil || got.Code != http.StatusOK || out.PrincipalID != want {
			t.Fatalf("owner %s: status %d %+v, want %s", caller.Name, got.Code, out, want)
		}
	}

	// Owners of another workspace are never answered.
	if got := feedbackCall(t, m, w.outsider); got.Code != http.StatusNotFound {
		t.Fatalf("outsider: status %d: %s", got.Code, got.Body.String())
	}
}
