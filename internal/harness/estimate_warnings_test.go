// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// R3/R4/R10: optional projection contention must not lose an owned heartbeat,
// bypass a revoked grant, refresh old process evidence, or manufacture Done.
func TestHeartbeatIsolationCommitAndAuthority(t *testing.T) {
	f := fixture(t)
	lease := "isolated-heartbeat-lease-0000000001"
	session := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "isolated-heartbeat-ref-0000001", lease)
	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/heartbeat"
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET progress_pct=100,
			eta_live_at=$2,eta_reported_at=$2,process_observed_at=$2 WHERE id=$1`, session, old)
		return err
	})
	enabled := false
	f.mux = http.NewServeMux()
	harness.New(f.db.App).(*harness.Module).WithHeartbeatIsolation(func(_ context.Context, _ pgx.Tx, project string) (bool, error) {
		return enabled && project == f.project, nil
	}).Mount(f.mux)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	blocker, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var blockerPID int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `LOCK TABLE eta_settings IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	waitBlocked := func() {
		t.Helper()
		for {
			var waiting bool
			if err := f.db.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database() AND wait_event_type='Lock'
				AND query LIKE '%SELECT interval_minutes FROM eta_settings%'
				AND $1::int=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				return
			}
			runtime.Gosched()
		}
	}
	// With the gate off, the existing optional SELECT remains in the write.
	legacyCtx, cancelLegacy := context.WithCancel(ctx)
	defer cancelLegacy()
	legacy := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"phase":"working","activity_sequence":1}`))
		r = r.WithContext(tenant.WithPrincipal(legacyCtx, f.agent))
		r.Header.Set("Authorization", "Bearer "+f.key)
		r.Header.Set("X-Aeon-Worker-Lease", lease)
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		legacy <- w
	}()
	waitBlocked()
	assertStored := func(want int64, accepted bool) {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var seq int64
			var beat *time.Time
			var report, observed time.Time
			var progress int
			if err := tx.QueryRow(ctx, `SELECT activity_sequence,heartbeat_at,eta_reported_at,
				process_observed_at,progress_pct FROM harness_sessions WHERE id=$1`, session).
				Scan(&seq, &beat, &report, &observed, &progress); err != nil {
				return err
			}
			if seq != want || (beat != nil) != accepted || !report.Equal(old) || !observed.Equal(old) || progress != 100 {
				t.Fatalf("stored receipt seq=%d beat=%v report=%v process=%v progress=%d", seq, beat, report, observed, progress)
			}
			return nil
		})
	}
	assertStored(0, false)
	cancelLegacy()
	select {
	case w := <-legacy:
		if w.Code == 200 {
			t.Fatal("disabled mode unexpectedly accepted its blocked projection")
		}
	case <-ctx.Done():
		t.Fatal("legacy request did not leave its query barrier")
	}
	assertStored(0, false)

	// An unrelated read now reaches the same projection barrier without taking
	// mutation fences. It stays blocked through commit and both authority checks.
	projectionCtx, cancelProjection := context.WithCancel(ctx)
	defer cancelProjection()
	projection := make(chan error, 1)
	go func() {
		projection <- db.InTenant(tenant.WithPrincipal(projectionCtx, f.person), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
			var minutes int
			return tx.QueryRow(projectionCtx, `SELECT coalesce((SELECT interval_minutes FROM eta_settings),10)`).Scan(&minutes)
		})
	}()
	waitBlocked()
	enabled = true
	beat := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		beat <- f.call(f.agent, "POST", path, map[string]any{"phase": "working", "activity_sequence": 1}, lease)
	}()
	var w *httptest.ResponseRecorder
	select {
	case w = <-beat:
	case <-ctx.Done():
		t.Fatal("owned heartbeat waited for the unrelated projection")
	}
	expect(t, w, 200)
	body := decode(t, w)
	warningCodes(t, body, "projection_unavailable")
	if body["finished"] != false || body["progress_pct"] != nil || body["eta_stale"] != true || body["process_observed_at"] != old.Format(time.RFC3339) {
		t.Fatalf("unknown projection refreshed evidence or claimed completion: %s", w.Body)
	}
	assertStored(1, true)
	select {
	case err := <-projection:
		t.Fatalf("projection barrier already left: %v", err)
	default:
	}
	w = f.call(f.agent, "POST", path, map[string]any{"phase": "working", "activity_sequence": 2}, "wrong-isolated-worker-lease-0000001")
	expect(t, w, 403)
	if !strings.Contains(w.Body.String(), "harness worker proof rejected") {
		t.Fatalf("wrong lease failed for the wrong reason: %s", w.Body)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		if err := db.LockWorkTreeTx(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	w = f.call(f.agent, "POST", path, map[string]any{"phase": "working", "activity_sequence": 2}, lease)
	expect(t, w, 403)
	if !strings.Contains(w.Body.String(), "forbidden") {
		t.Fatalf("revoked grant failed for the wrong reason: %s", w.Body)
	}
	assertStored(1, true)
	cancelProjection()
	select {
	case err := <-projection:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("projection failed for the wrong reason: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("projection did not cancel")
	}
	assertStored(1, true)
}

func warningCodes(t *testing.T, body map[string]any, want ...string) {
	t.Helper()
	items, ok := body["warnings"].([]any)
	if !ok {
		t.Fatalf("missing warning array: %#v", body["warnings"])
	}
	got := []string{}
	for _, item := range items {
		warning := item.(map[string]any)
		if warning["hint"] == "" {
			t.Fatal("empty hint")
		}
		got = append(got, warning["code"].(string))
	}
	if len(got) != len(want) || len(got) > 0 && !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings %v; want %v", got, want)
	}
}

func TestHeartbeatEstimateWarningMatrix(t *testing.T) {
	f := fixture(t)
	lease := "guidance-worker-lease-00000000000001"
	session := f.registerSession(t, f.agent.ID, "worker", f.ticket, "guidance-worker-ref-0000000001", lease)
	for seq := 1; seq <= 2; seq++ {
		warningCodes(t, f.beat(t, session, lease, seq, nil), "missing_eta", "ticket_without_estimate")
	}
	warningCodes(t, f.beat(t, session, lease, 3, nil), "missing_progress", "missing_eta", "ticket_without_estimate")
	ready := time.Now().Add(time.Hour).Format(time.RFC3339)
	warningCodes(t, f.beat(t, session, lease, 4, map[string]any{"progress_pct": 0, "eta_ready_at": ready}), "ticket_without_estimate")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"estimate_hours":2}' WHERE id=$1`, f.ticket)
		return err
	})
	warningCodes(t, f.beat(t, session, lease, 5, nil))
	warningCodes(t, f.beat(t, session, lease, 6, nil))
	// A stored zero is known, but it is not a fresh progress report.
	warningCodes(t, f.beat(t, session, lease, 7, nil), "missing_progress")
	warningCodes(t, f.beat(t, session, lease, 8, map[string]any{"phase": "yielded"}))
	warningCodes(t, f.beat(t, session, lease, 9, map[string]any{"progress_pct": nil, "eta_ready_at": nil}), "missing_eta")
	warningCodes(t, f.beat(t, session, lease, 10, nil), "missing_eta")
	warningCodes(t, f.beat(t, session, lease, 11, nil), "missing_progress", "missing_eta")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"estimate_hours":"2"}' WHERE id=$1`, f.ticket)
		return err
	})
	warningCodes(t, f.beat(t, session, lease, 12, map[string]any{"phase": "starting"}), "ticket_without_estimate")
	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/heartbeat"
	expect(t, f.call(f.foreign, "POST", path, map[string]any{"phase": "working", "activity_sequence": 13}, lease), 403)

	coordLease := "guidance-coordinator-lease-0000000001"
	coord := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "guidance-coordinator-ref-00000001", coordLease)
	for seq := 1; seq <= 4; seq++ {
		warningCodes(t, f.beat(t, coord, coordLease, seq, nil), "missing_eta", "ticket_without_estimate")
	}
	warningCodes(t, f.beat(t, coord, coordLease, 5, map[string]any{"eta_live_at": ready}), "ticket_without_estimate")
	warningCodes(t, f.beat(t, coord, coordLease, 6, map[string]any{"phase": "stopping"}), "ticket_without_estimate")

	bareLease := "guidance-unbound-lease-0000000000001"
	bare := f.registerSession(t, f.agent.ID, "worker", "", "guidance-unbound-ref-00000001", bareLease)
	for seq := 1; seq <= 2; seq++ {
		warningCodes(t, f.beat(t, bare, bareLease, seq, nil))
	}
	warningCodes(t, f.beat(t, bare, bareLease, 3, nil), "missing_progress")
	bareCoordLease := "guidance-unbound-coordinator-lease-01"
	bareCoord := f.registerSession(t, f.agent.ID, "coordinator", "", "guidance-unbound-coord-ref-001", bareCoordLease)
	for seq := 1; seq <= 4; seq++ {
		warningCodes(t, f.beat(t, bareCoord, bareCoordLease, seq, nil))
	}
}

func TestHeartbeatWarningSQLFailureDoesNotRollbackAcceptedBeat(t *testing.T) {
	f := fixture(t)
	lease := "guidance-sql-failure-lease-00000001"
	session := f.registerSession(t, f.agent.ID, "worker", f.ticket, "guidance-sql-failure-ref-00001", lease)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_estimate_warnings() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'guidance unavailable'; END $$;
			CREATE TRIGGER reject_estimate_warnings BEFORE UPDATE OF missing_progress_beats ON harness_sessions
			FOR EACH ROW EXECUTE FUNCTION reject_estimate_warnings()`)
		return err
	})
	for seq := 1; seq <= 2; seq++ {
		body := f.beat(t, session, lease, seq, map[string]any{"activity_note": "Still working"})
		warningCodes(t, body)
		if body["activity_sequence"] != float64(seq) || body["activity_note"] != "Still working" || body["heartbeat_at"] == nil {
			t.Fatal("guidance failure lost accepted heartbeat fields", body)
		}
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var sequence int
		var note string
		err := tx.QueryRow(t.Context(), `SELECT activity_sequence,activity_note FROM harness_sessions WHERE id=$1`, session).Scan(&sequence, &note)
		if err == nil && (sequence != 2 || note != "Still working") {
			t.Fatalf("heartbeat did not commit: %d %q", sequence, note)
		}
		return err
	})
}

func TestTicketEtaRetainsWorkingHintWithoutReport(t *testing.T) {
	f := fixture(t)
	lease := "guidance-working-lease-0000000000001"
	session := f.registerSession(t, f.agent.ID, "worker", f.ticket, "guidance-working-ref-00000001", lease)
	f.beat(t, session, lease, 1, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		view, err := eta.One(t.Context(), tx, f.ticket)
		if err != nil {
			return err
		}
		if !view.HasWorkingSession || view.Blank() || view.ReadyAt != nil {
			t.Fatal("working session lost its missing ETA hint")
		}
		views, err := eta.Load(t.Context(), tx, []string{f.ticket})
		if err != nil {
			return err
		}
		if !views[f.ticket].HasWorkingSession {
			t.Fatal("list lost working hint")
		}
		return nil
	})
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		views, err := eta.Load(t.Context(), tx, []string{f.ticket})
		if err == nil && len(views) != 0 {
			t.Fatal("foreign tenant saw a working hint")
		}
		return err
	})
	f.beat(t, session, lease, 2, map[string]any{"phase": "yielded"})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		view, err := eta.One(t.Context(), tx, f.ticket)
		if err == nil && (!view.Blank() || view.HasWorkingSession) {
			t.Fatal("yielded session still shows working hint")
		}
		return err
	})
}
