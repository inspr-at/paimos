// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/modelregistry"
)

func TestWorkstationModelReadSnapshots(t *testing.T) {
	f := newWorkstationFixture(t)
	modelregistry.New(f.m.pool).Mount(f.mux)
	f.enable(t)
	for _, path := range []string{"/api/models/resolve?role=build&mode=placement"} {
		t.Run(path, func(t *testing.T) {
			w := f.call(f.key.Token, http.MethodGet, path, "", "")
			if w.Code != http.StatusOK {
				t.Fatalf("authorized workstation snapshot: HTTP %d: %s", w.Code, w.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["preference"] == nil || body["trace"] == nil {
				t.Fatal("placement did not return its preference decision and trace")
			}
			var preference struct {
				PersonID string `json:"person_id"`
			}
			if err := json.Unmarshal(body["preference"], &preference); err != nil || preference.PersonID != f.owner.ID {
				t.Fatal("placement lost the key creator's canonical person")
			}
		})
	}
	// Preference GET retains its existing middleware ceiling, even for a
	// designated workstation. Do not broaden the approved person-only surface.
	if w := f.call(f.key.Token, http.MethodGet, "/api/model-preferences", "", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "agent key scope required") {
		t.Fatalf("workstation preference ceiling: HTTP %d", w.Code)
	}
	if w := f.mark(f.owner, f.key.ID, f.computer, false); w.Code != http.StatusOK {
		t.Fatalf("remove workstation designation: HTTP %d", w.Code)
	}
	if w := f.call(f.key.Token, http.MethodGet, "/api/models/resolve?role=build&mode=placement", "", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "outside this computer's runtime authority") {
		t.Fatalf("unmarked paired key bypassed runtime ceiling: HTTP %d", w.Code)
	}
	ordinary := decodeKey(t, keyRequest(f.m, f.owner, map[string]any{"name": "Synthetic ordinary reader", "scopes": []string{"models.read"}}))
	if w := f.call(ordinary.Token, http.MethodGet, "/api/models/resolve?role=build&mode=placement", "", ""); w.Code != http.StatusOK {
		t.Fatalf("ordinary scoped placement snapshot: HTTP %d: %s", w.Code, w.Body.String())
	}
}
