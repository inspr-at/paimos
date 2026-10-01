// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestEventBriefingRange(t *testing.T) {
	d, a, b := fixture(t)
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		for i, at := range []time.Time{start.Add(-time.Second), start, end.Add(-time.Second), end} {
			_, err := Append(t.Context(), tx, a, Change{Type: "test.changed", After: map[string]int{"row": i}, At: &at})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, d, b, 1)
	m := New(d.App).(*module)
	got, err := m.read(t.Context(), a, "", 0, 1, eventRange{from: &start, to: &end})
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != 2 || got.NextAfter == nil {
		t.Fatalf("first page %+v %v", got, err)
	}
	next, err := m.read(t.Context(), a, "", *got.NextAfter, 1, eventRange{from: &start, to: &end})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != 3 || next.NextAfter != nil {
		t.Fatalf("next page %+v %v", next, err)
	}
	// Type filters never change cursor semantics or return unrelated snapshots.
	filtered, err := m.read(t.Context(), a, "", 0, 50, eventRange{from: &start, to: &end, types: []string{"node.updated"}})
	if err != nil || len(filtered.Items) != 0 {
		t.Fatalf("type filter %+v %v", filtered, err)
	}
	for _, query := range []string{"from=bad&to=bad", "from=2026-10-01T08:00:00Z", "from=2026-10-01T08:00:00Z&to=2026-09-30T08:00:00Z", "type=", "type=a,b,c,d,e,f,g,h,i"} {
		r := httptest.NewRequest(http.MethodGet, "/api/events?"+query, nil)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), a))
		w := httptest.NewRecorder()
		m.list(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid query %s: %d", query, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/events?from=2026-09-30T08:00:00Z&to=2026-10-01T08:00:00Z&type=test.changed", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), a))
	w := httptest.NewRecorder()
	m.list(w, r)
	var page page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 2 {
		t.Fatalf("HTTP range %d %s %v", w.Code, w.Body.String(), err)
	}
}
