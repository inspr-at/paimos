// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// editorRetired asserts the retired ladder writer's contract: 410 Gone with a
// pointer to the Models successor, whatever the body, role or precondition.
func editorRetired(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusGone || w.Header().Get("Deprecation") != "true" || !strings.Contains(w.Header().Get("Link"), "</api/model-preferences/orders>") || !strings.Contains(w.Body.String(), "moved") {
		t.Fatalf("legacy ladder writer not retired: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

// Risk: the retired ladder writer still decodes, bounds or applies an
// exact-bound, over-bound or oversized body, or a role write over retained
// ladder history, instead of refusing every one unchanged.
func TestEditorExactRowAndBodyBounds(t *testing.T) {
	p, h := editorFixture(t)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), `DELETE FROM model_role_routes`); return err }); err != nil {
		t.Fatal(err)
	}
	displayRoutesFixture(t, p, 50)
	full := h.ladder(t, p, "review-gate")
	if full.Truncated || full.EditToken != nil || full.CanEdit || len(full.Routes) != 50 {
		t.Fatal("complete bound misreported or editable", full.Truncated, len(full.Routes))
	}
	ids := make([]string, len(full.Routes))
	for i, row := range full.Routes {
		ids[i] = row.ProfileID
	}
	all := []Route{}
	for _, role := range []string{"build", "build-hard", "scout", "mechanical", "review-gate"} {
		for i, id := range ids {
			all = append(all, Route{Role: role, Priority: i + 1, ProfileID: id, State: "available"})
		}
	}
	if len(all) != 250 {
		t.Fatal("incorrect bound fixture")
	}
	if read := h.ladder(t, p, "review-gate"); read.CanEdit || read.EditToken != nil {
		t.Fatal("compatibility read still grants editing")
	}
	before := catalogSetupState(t, p)
	events := eventCount(t, p, evRoutes)
	extra := append(append([]Route{}, all...), Route{Role: "build", Priority: 51, ProfileID: ids[0], State: "available"})
	for _, body := range []string{editorJSON(all), editorJSON(extra), strings.Repeat(" ", 1<<20) + "[]"} {
		editorRetired(t, h.call(t, p, "PUT", "/api/models/routes", body, nil))
	}
	editorRetired(t, h.call(t, p, "PUT", "/api/models/routes?role=build", "[]", map[string]string{"If-Match": `"` + strings.Repeat("0", 64) + `"`}))
	if catalogSetupState(t, p) != before || eventCount(t, p, evRoutes) != events {
		t.Fatal("retired writer mutated storage or wrote an event")
	}
}
