// SPDX-License-Identifier: AGPL-3.0-only
package journal

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestJournalHoldLookupIsBoundedByAdmission(t *testing.T) {
	for _, history := range []int{0, 2000} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			f := newWorld(t, generous())
			id := text(object(f.success("ledger", "admit", "POST", f.admitBody(1, 100), nil)["body"])["hold_id"])

			// Seed many immutable acknowledgements for other holds, ahead of the target.
			// The target is appended last; unrelated acknowledgements must stay unread.
			_, err := f.database.Admin.Exec(t.Context(), `INSERT INTO aithema_journal_records(tenant_id,sid,seq,client_event_id,contract,kind,original_bytes,document)
  SELECT $1,$2,g,('00000000-0000-4000-8000-'||lpad(g::text,12,'0'))::uuid,'aithema.journal.record','budget.hold',convert_to('{}','UTF8'),convert_to(jsonb_set($3::jsonb,'{data,hold_id}',to_jsonb('00000000-0000-4000-8000-000000000000'::text))::text,'UTF8') FROM generate_series(1,$4::int) g`, f.claims.TenantID, f.claims.SessionID, marshal(f.journalHold(id)), history)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.database.Admin.Exec(t.Context(), `UPDATE aithema_sessions SET seq=$3 WHERE tenant_id=$1 AND sid=$2`, f.claims.TenantID, f.claims.SessionID, history); err != nil {
				t.Fatal(err)
			}
			ack := f.journalHold(id)
			// Put the target last under both sequence and client-event index ordering;
			// a legacy scan must not pass just because a random UUID sorts first.
			ack["client_event_id"] = "ffffffff-ffff-4fff-bfff-ffffffffffff"
			f.success("journal", "records", "POST", marshal(ack), nil)
			work := dbtest.ReadWork{}
			err = db.InTenant(t.Context(), f.database.App, f.claims.TenantID, func(tx pgx.Tx) error {
				st, err := load(t.Context(), tx, f.claims.TenantID, f.claims.SessionID)
				if err != nil {
					return err
				}
				h, err := getHold(t.Context(), tx, st, id, "")
				if err != nil {
					return err
				}
				return journaledHold(t.Context(), dbtest.CountReads(tx, &work), st, h)
			})
			if err != nil {
				t.Fatal(err)
			}
			if work.Rows != 1 || work.Statements != 1 {
				t.Fatalf("hold scanned history: %+v", work)
			}
		})
	}
}
