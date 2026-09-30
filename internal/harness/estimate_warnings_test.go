// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/jackc/pgx/v5"
)

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
	warningCodes(t, f.beat(t, bare, bareLease, 1, nil), "missing_eta")
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
