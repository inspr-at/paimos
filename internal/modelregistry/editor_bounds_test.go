// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEditorExpiryBoundaryNewAndRetainedHolds(t *testing.T) {
	expiry := time.Date(2030, 1, 1, 0, 0, 0, 987654000, time.UTC)
	hold := Route{Role: "build", Priority: 1, ProfileID: "00000000-0000-0000-0000-000000000001", State: "conserved", Reason: "quota", ValidUntil: &expiry}
	for _, offset := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
		t.Run(offset.String(), func(t *testing.T) {
			now := expiry.Add(offset)
			_, err := normalizeStoredRoutes([]Route{hold}, nil, now, false)
			if offset < 0 && err != nil || offset >= 0 && err == nil {
				t.Fatal("wrong strictly future boundary", err)
			}
			reordered := hold
			reordered.Priority = 2
			if _, err := normalizeStoredRoutes([]Route{reordered}, []Route{hold}, now, false); err != nil {
				t.Fatal("unchanged expired hold cannot be reordered", err)
			}
			clear, err := normalizeStoredRoutes([]Route{hold}, nil, now, true)
			if err != nil {
				t.Fatal(err)
			}
			if offset < 0 {
				if clear[0].State != hold.State || !timePtrEqual(clear[0].ValidUntil, hold.ValidUntil) {
					t.Fatal("future compensation altered or extended hold")
				}
			} else if clear[0].State != "available" || clear[0].Reason != "" || clear[0].ValidUntil != nil {
				t.Fatal("expired compensation revived hold")
			}
		})
	}
}

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
	var ids []string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT profile_id::text FROM model_role_routes WHERE role='review-gate' ORDER BY priority`)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
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
