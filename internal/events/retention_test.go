// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		appendAt(a, typ, now.Add(-TelemetryRetention))
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
	if left != 4 || foreign != 2 || unrelated != 3 {
		t.Fatalf("wrong survivors: %d/%d/%d", left, foreign, unrelated)
	}
	if e := appendAt(a, "account.probed", young); e.ID <= base {
		t.Fatal("retention rewound event IDs")
	}
	requireCode := func(err error, code string) {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != code {
			t.Fatalf("expected SQLSTATE %s, got %v", code, err)
		}
	}
	_, err := pruneTelemetryBatch(t.Context(), d.App, a.TenantID, now, 3)
	requireCode(err, "22023")
	_, err = pruneTelemetryBatch(t.Context(), d.App, a.TenantID, old, 501)
	requireCode(err, "22023")
	err = db.InTenant(tenant.WithPrincipal(t.Context(), a), d.App, a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT * FROM aeon_prune_telemetry($1,$2,3)`, a.TenantID, old)
		return err
	})
	requireCode(err, "42501")
}

// Risk: retention runs only after the first day, uses a drifting per-batch
// cutoff, or ignores cancellation. Inject the clock and cadence boundary.
func TestTelemetryRetentionStartsImmediatelyAndRunsDaily(t *testing.T) {
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	start := now
	sweeps, waits := 0, 0
	runTelemetryRetention(t.Context(), func(ctx context.Context, cutoff time.Time) {
		if sweeps != waits || !cutoff.Equal(start.Add(time.Duration(sweeps)*24*time.Hour).Add(-TelemetryRetention)) {
			t.Fatalf("wrong retention schedule: sweeps=%d waits=%d cutoff=%v", sweeps, waits, cutoff)
		}
		sweeps++
	}, func(ctx context.Context, delay time.Duration) bool {
		if delay != 24*time.Hour || sweeps != waits+1 {
			t.Fatal("retention did not run before its daily wait")
		}
		waits++
		now = now.Add(delay)
		return waits < 2
	}, func() time.Time { return now })
	if sweeps != 2 || waits != 2 {
		t.Fatalf("cadence did not stop: %d/%d", sweeps, waits)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if waitTelemetry(ctx, time.Hour) {
		t.Fatal("cancelled retention kept waiting")
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
		_, err := d.Admin.Exec(t.Context(), query)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "events are append-only" {
			t.Fatalf("append-only guard failed for the wrong reason: %v", err)
		}
	}
}

// Risk: aeon_prune_telemetry leaves aeon.telemetry_prune on, so the same
// service transaction can delete further telemetry after the bounded call.
// The function also must not store that placeholder in proconfig: the
// NOSUPERUSER migration role cannot set it (SQLSTATE 42501).
func TestTelemetryRetentionPruneFlagDoesNotLeak(t *testing.T) {
	d, a, _ := fixture(t)
	old := time.Now().Add(-TelemetryRetention - time.Hour)
	var first, second Event
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		var err error
		first, err = Append(t.Context(), tx, a, Change{Type: "account.probed", After: map[string]bool{"ok": true}, At: &old})
		if err != nil {
			return err
		}
		second, err = Append(t.Context(), tx, a, Change{Type: "account.probed", After: map[string]bool{"ok": true}, At: &old})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(db.AllProjects(t.Context(), "telemetry retention"), d.App, a.TenantID, func(tx pgx.Tx) error {
		var configs []string
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(p.proconfig,'{}'::text[]) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='aeon_prune_telemetry'`).Scan(&configs); err != nil {
			return err
		}
		for _, item := range configs {
			if strings.Contains(item, "aeon.telemetry_prune") {
				t.Fatalf("function configuration stores the prune placeholder: %v", configs)
			}
		}
		var before string
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(current_setting('aeon.telemetry_prune',true),'')`).Scan(&before); err != nil {
			return err
		}
		if before == "on" {
			t.Fatal("service transaction started with the prune flag set")
		}
		rows, err := tx.Query(t.Context(), `SELECT event_type,deleted,undo_protected FROM aeon_prune_telemetry($1,$2,1)`, a.TenantID, old.Add(time.Minute))
		if err != nil {
			return err
		}
		defer rows.Close()
		var probes, beats int64
		var protected bool
		for rows.Next() {
			var typ string
			var count int64
			var rowProtected bool
			if err := rows.Scan(&typ, &count, &rowProtected); err != nil {
				return err
			}
			protected = protected || rowProtected
			switch typ {
			case "account.probed":
				probes = count
			case "harness.heartbeat":
				beats = count
			default:
				t.Fatalf("unexpected retention type %s", typ)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if protected || probes != 1 || beats != 0 {
			t.Fatalf("bounded prune = probes %d beats %d protected %v", probes, beats, protected)
		}
		var after string
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(current_setting('aeon.telemetry_prune',true),'')`).Scan(&after); err != nil {
			return err
		}
		if after != before {
			t.Fatalf("prune flag changed from %q to %q", before, after)
		}
		var gone, kept int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE id=$2), count(*) FILTER (WHERE id=$3) FROM events WHERE tenant_id=$1`, a.TenantID, first.ID, second.ID).Scan(&gone, &kept); err != nil {
			return err
		}
		if gone != 0 || kept != 1 {
			t.Fatalf("batch did not keep the later row: deleted-present %d survivor %d", gone, kept)
		}
		// RLS may hide the row, or the append-only trigger may reject it. Either
		// way the same transaction must not delete it. A leaked flag would.
		if _, err := tx.Exec(t.Context(), `SAVEPOINT retention_probe`); err != nil {
			return err
		}
		tag, delErr := tx.Exec(t.Context(), `DELETE FROM events WHERE tenant_id=$1 AND id=$2`, a.TenantID, second.ID)
		if _, err := tx.Exec(t.Context(), `ROLLBACK TO SAVEPOINT retention_probe`); err != nil {
			return err
		}
		if delErr != nil {
			var pgErr *pgconn.PgError
			if !errors.As(delErr, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "events are append-only" {
				t.Fatalf("same-transaction delete failed for the wrong reason: %v", delErr)
			}
		} else if tag.RowsAffected() != 0 {
			t.Fatal("telemetry delete succeeded after aeon_prune_telemetry returned")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND id=$2`, a.TenantID, second.ID).Scan(&kept); err != nil {
			return err
		}
		if kept != 1 {
			t.Fatal("same-transaction delete removed telemetry after the retention call")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
