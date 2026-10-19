// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: cleanup deletes user history, fresh telemetry or another tenant's rows,
// exceeds its batch bound, breaks event IDs, or runs twice destructively.
func TestTelemetryRetentionBatchedScopedAndIdempotent(t *testing.T) {
	d, a, b := fixture(t)
	now := time.Now()
	old, young := now.Add(-TelemetryRetention-time.Hour), now.Add(-time.Hour)
	appendAt := func(p tenant.Principal, typ string, at time.Time) Event {
		t.Helper()
		var e Event
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
			var err error
			e, err = Append(t.Context(), tx, p, Change{Type: typ, After: map[string]string{"state": "ready"}, At: &at})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	for i := 0; i < 7; i++ {
		typ := "account.probed"
		if i%2 == 1 {
			typ = "harness.heartbeat"
		}
		appendAt(a, typ, old)
	}
	for _, typ := range []string{"account.probed", "harness.heartbeat"} {
		appendAt(a, typ, young)
		appendAt(b, typ, old)
	}
	for _, typ := range []string{"node.updated", "harness.stopped", "account.updated"} {
		appendAt(a, typ, old)
	}
	base := logPosition(t, d, a)
	var probes, beats int64
	for _, want := range []int64{3, 3, 1, 0, 0} {
		batch, err := pruneTelemetryBatch(t.Context(), d.App, a.TenantID, now.Add(-TelemetryRetention), 3)
		if err != nil {
			t.Fatal(err)
		}
		if batch.protected || batch.probes+batch.heartbeats != want {
			t.Fatalf("batch %+v, want %d", batch, want)
		}
		probes += batch.probes
		beats += batch.heartbeats
	}
	if probes != 4 || beats != 3 {
		t.Fatalf("counts per type: %d/%d", probes, beats)
	}
	var left, foreign, unrelated int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE tenant_id=$1 AND type IN ('account.probed','harness.heartbeat')),count(*) FILTER(WHERE tenant_id=$2 AND type IN ('account.probed','harness.heartbeat')),count(*) FILTER(WHERE tenant_id=$1 AND type IN ('node.updated','harness.stopped','account.updated')) FROM events`, a.TenantID, b.TenantID).Scan(&left, &foreign, &unrelated); err != nil {
		t.Fatal(err)
	}
	if left != 2 || foreign != 2 || unrelated != 3 {
		t.Fatalf("wrong survivors: %d/%d/%d", left, foreign, unrelated)
	}
	if e := appendAt(a, "account.probed", young); e.ID <= base {
		t.Fatal("retention rewound event IDs")
	}
	if _, err := pruneTelemetryBatch(t.Context(), d.App, a.TenantID, now, 3); err == nil {
		t.Fatal("accepted an unsafe age")
	}
	if _, err := pruneTelemetryBatch(t.Context(), d.App, a.TenantID, old, 501); err == nil {
		t.Fatal("accepted oversized batch")
	}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), a), d.App, a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT * FROM aeon_prune_telemetry($1,$2,3)`, a.TenantID, old)
		return err
	})
	if err == nil {
		t.Fatal("request context acquired retention authority")
	}
}

// Risk: deleting telemetry invalidates undo history or opens generic mutation
// of this log and other append-only tables. An eligible undo reference skips
// the entire tenant, including unreferenced telemetry in the same batch.
func TestTelemetryRetentionSkipsUndoAndKeepsAppendOnlyGuard(t *testing.T) {
	d, a, _ := fixture(t)
	old := time.Now().Add(-TelemetryRetention - time.Hour)
	var target Event
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		var err error
		target, err = Append(t.Context(), tx, a, Change{Type: "account.probed", After: map[string]bool{"ok": true}, At: &old})
		if err != nil {
			return err
		}
		if _, err = Append(t.Context(), tx, a, Change{Type: "harness.heartbeat", After: map[string]bool{"ok": true}, At: &old}); err != nil {
			return err
		}
		_, err = Append(t.Context(), tx, a, Change{Type: "test.undone", After: map[string]bool{"ok": true}, UndoOf: &target.ID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		batch, err := pruneTelemetryBatch(t.Context(), d.App, a.TenantID, old.Add(time.Minute), 3)
		if err != nil || !batch.protected || batch.probes+batch.heartbeats != 0 {
			t.Fatalf("undo protection %+v %v", batch, err)
		}
	}
	var n int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type IN ('account.probed','harness.heartbeat')`, a.TenantID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("protected telemetry lost: %d %v", n, err)
	}
	for _, query := range []string{`DELETE FROM events`, `UPDATE events SET type='test.changed'`} {
		if _, err := d.Admin.Exec(context.Background(), query); err == nil {
			t.Fatal("unguarded privileged mutation accepted")
		}
	}
}
