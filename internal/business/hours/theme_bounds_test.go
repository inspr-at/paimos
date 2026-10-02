// SPDX-License-Identifier: AGPL-3.0-only
package hours

import (
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"net/http/httptest"
	"testing"
)

func TestBusinessHistoryPagesBoundRowsAndReads(t *testing.T) {
	f := setup(t)
	period := f.period(f.member)
	f.sql(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO time_entries(tenant_id,period_id,principal_id,node_id,cost_unit_node_id,source,started_at,ended_at,duration_seconds,rate_amount,currency,amount,note)
   SELECT $1,$2,$3,$4,$5,'manual','2026-09-23T12:00:00Z'::timestamptz+g*interval '1 second','2026-09-23T12:00:01Z'::timestamptz+g*interval '1 second',1,0,'EUR',0,'volume'
   FROM generate_series(1,300) g`, f.admin.TenantID, period.ID, f.member.ID, f.child, f.cost)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO time_periods(tenant_id,principal_id,starts_at,ends_at)
   SELECT $1,$2,'2026-01-01Z'::timestamptz+g*interval '1 day','2026-01-02Z'::timestamptz+g*interval '1 day' FROM generate_series(1,300) g`, f.admin.TenantID, f.admin.ID)
		return err
	})
	seen := map[string]bool{}
	after := ""
	for page := 0; page < 2; page++ {
		req := httptest.NewRequest("GET", "/api/time-entries?limit=3&after_id="+after, nil)
		var got listPage[Entry]
		work := dbtest.ReadWork{}
		f.sql(func(tx pgx.Tx) error {
			out, err := f.mod.listEntries(req, dbtest.CountReads(tx, &work), f.admin)
			if err == nil {
				var ok bool
				got, ok = out.(listPage[Entry])
				if !ok {
					t.Fatal("unbounded legacy list")
				}
			}
			return err
		})
		if len(got.items) != 3 || got.next == "" || work.Rows != 4 || work.Statements != 1 {
			t.Fatalf("entry page work: %+v, len=%d", work, len(got.items))
		}
		for _, e := range got.items {
			if seen[e.ID] {
				t.Fatal("page repeated an entry")
			}
			seen[e.ID] = true
		}
		after = got.next
	}
	work := dbtest.ReadWork{}
	f.sql(func(tx pgx.Tx) error {
		out, err := f.mod.listPeriods(httptest.NewRequest("GET", "/api/time-periods?limit=3&since=2025-01-01T00:00:00Z", nil), dbtest.CountReads(tx, &work), f.admin)
		if err != nil {
			return err
		}
		got, ok := out.(listPage[Period])
		if !ok || len(got.items) != 3 || got.next == "" {
			t.Fatal("period list not paged")
		}
		return nil
	})
	if work.Statements != 1 || work.Rows != 4 {
		t.Fatalf("periods used per-row hydration: %+v", work)
	}
}
