// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
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

// Risk: the retired ladder writer buffers or decodes a large body, or the
// deprecated read edits or misreports its 50-step bound. A complete 50-step
// derived snapshot is complete; any body, even 1 MiB, is refused with 410.
func TestEditorExactRowAndBodyBounds(t *testing.T) {
	p, h := editorFixture(t)
	displayRoutesFixture(t, p, 50)
	full := h.ladder(t, p, "review-gate")
	if full.Truncated || full.EditToken != nil || full.CanEdit || len(full.Routes) != 50 {
		t.Fatal("complete bound misreported or editable", full.Truncated, len(full.Routes))
	}
	all := []Route{}
	for _, role := range []string{"build", "build-hard", "scout", "mechanical", "review-gate"} {
		for _, row := range full.Routes {
			row.Role = role
			all = append(all, row)
		}
	}
	before := modelsState(t, p)
	retiredLadderWrite(t, h, p, "/api/models/routes", editorJSON(all), nil)
	retiredLadderWrite(t, h, p, "/api/models/routes", editorJSON(append(all, all[0])), nil)
	retiredLadderWrite(t, h, p, "/api/models/routes", strings.Repeat(" ", 1<<20)+"[]", nil)
	retiredLadderWrite(t, h, p, "/api/models/routes?role=build", "[]", map[string]string{"If-Match": routeEditToken("review-gate", full.Routes)})
	if modelsState(t, p) != before {
		t.Fatal("bound refusal mutated storage")
	}
}
