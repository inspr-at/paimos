// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
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

func TestEditorExactRowAndBodyBounds(t *testing.T) {
	p, h := editorFixture(t)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), `DELETE FROM model_role_routes`); return err }); err != nil {
		t.Fatal(err)
	}
	displayRoutesFixture(t, p, 50)
	ladder := h.ladder(t, p, "review-gate")
	all := []Route{}
	for _, role := range []string{"build", "build-hard", "scout", "mechanical", "review-gate"} {
		for _, row := range ladder.Routes {
			row.Role = role
			all = append(all, row)
		}
	}
	if len(all) != 250 {
		t.Fatal("incorrect bound fixture")
	}
	editorDecode[[]Route](t, h.call(t, p, "PUT", "/api/models/routes", editorJSON(all), nil))
	before := catalogSetupState(t, p)
	extra := append(append([]Route{}, all...), all[0])
	extra[len(extra)-1].Priority = 51
	editorError(t, h.call(t, p, "PUT", "/api/models/routes", editorJSON(extra), nil), 413, "too_many_routes")
	full := h.ladder(t, p, "build")
	if full.Truncated || full.EditToken == nil || !full.CanEdit || len(full.Routes) != 50 {
		t.Fatal("complete bound not editable")
	}
	w := h.call(t, p, "PUT", "/api/models/routes", strings.Repeat(" ", 1<<20)+"[]", nil)
	editorError(t, w, 413, "request body too large")
	if catalogSetupState(t, p) != before {
		t.Fatal("bound refusal mutated storage")
	}
	// Existing oversized data cannot be represented by a partial audit snapshot,
	// even when the addressed role itself is complete and the new input is small.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		profile, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: "overflow", Version: "1", Harness: "claude", Family: "anthropic", Model: "fixture-overflow", Effort: "xhigh", Tier: "frontier"})
		if err != nil {
			return err
		}
		return insertRoute(t.Context(), tx, p.TenantID, Route{Role: "scout", Priority: 51, ProfileID: profile.ID, State: "available"})
	}); err != nil {
		t.Fatal(err)
	}
	before = catalogSetupState(t, p)
	editorError(t, h.call(t, p, "PUT", "/api/models/routes?role=build", "[]", map[string]string{"If-Match": *full.EditToken}), 413, "audit_snapshot_too_large")
	if catalogSetupState(t, p) != before {
		t.Fatal("oversized audit silently truncated")
	}
}
