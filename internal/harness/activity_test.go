// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestActivityNotesValidationHistoryAndIsolation(t *testing.T) {
	f := fixture(t)
	lease := "activity-note-lease-00000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host",
		"harness_session_ref": "activity-note-ref-00000000000001", "worker_lease": lease,
		"management_mode": "managed", "role": "worker", "ticket_node_id": f.ticket, "work_shape": "ship",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	beat := func(p int, note any, proof string) *string {
		t.Helper()
		body := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": p, "activity_note": note}
		w := f.call(f.agent, "POST", path+"/heartbeat", body, proof)
		if w.Code != 200 {
			msg := w.Body.String()
			return &msg
		}
		return nil
	}
	if err := beat(1, "  Running\nPDF\t tests  ", lease); err != nil {
		t.Fatal(*err)
	}
	if got := decode(t, f.call(f.person, "GET", path, nil, ""))["activity_note"]; got != "RunningPDF tests" {
		t.Fatalf("normalized note: %v", got)
	}
	for _, bad := range []string{"  ", strings.Repeat("x", 121)} {
		expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 2, "activity_note": bad}, lease), 400)
	}
	expect(t, f.call(f.person, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 2, "activity_note": "spoofed"}, lease), 403)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 2, "activity_note": "spoofed"}, "wrong-generation-lease-00000000001"), 403)
	for n := 2; n <= 23; n++ {
		if err := beat(n, fmt.Sprintf("Step %02d", n), lease); err != nil {
			t.Fatal(*err)
		}
	}
	latest := func(endpoint string) float64 {
		t.Helper()
		page := decode(t, f.call(f.person, "GET", endpoint, nil, ""))["items"].([]any)
		if len(page) != 1 {
			t.Fatalf("activity page: %#v", page)
		}
		return page[0].(map[string]any)["activity_note_id"].(float64)
	}
	listID := latest("/api/harness-sessions")
	liveID := latest("/api/harness-sessions/live")
	if listID != liveID || listID <= 0 {
		t.Fatalf("latest note differs: list=%v live=%v", listID, liveID)
	}
	// Identical notes are a heartbeat, not another activity item.
	if err := beat(24, "Step 23", lease); err != nil {
		t.Fatal(*err)
	}
	if latest("/api/harness-sessions") != listID || latest("/api/harness-sessions/live") != liveID {
		t.Fatal("plain heartbeat advanced latest activity entry")
	}
	if err := beat(25, "Step 24", lease); err != nil {
		t.Fatal(*err)
	}
	if latest("/api/harness-sessions") <= listID || latest("/api/harness-sessions/live") <= liveID {
		t.Fatal("new note did not advance latest activity entry")
	}
	detail := decode(t, f.call(f.person, "GET", path, nil, ""))
	history := detail["activity_history"].([]any)
	if len(history) != 20 || history[0].(map[string]any)["note"] != "Step 24" || history[19].(map[string]any)["note"] != "Step 05" {
		t.Fatalf("bounded history: %#v", history)
	}
	live := decode(t, f.call(f.person, "GET", "/api/harness-sessions/live", nil, ""))["items"].([]any)
	if len(live) != 1 || live[0].(map[string]any)["activity_note"] != "Step 24" {
		t.Fatalf("live note: %#v", live)
	}
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_activity_notes WHERE session_id=$1`, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("foreign tenant saw %d notes", count)
		}
		return nil
	})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var forced bool
		if err := tx.QueryRow(t.Context(), `SELECT relforcerowsecurity FROM pg_class WHERE oid='harness_activity_notes'::regclass`).Scan(&forced); err != nil {
			return err
		}
		if !forced {
			t.Fatal("activity table lacks FORCE RLS")
		}
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.heartbeat'`).Scan(&count); err != nil {
			return err
		}
		if count != 25 {
			t.Fatalf("heartbeat events: %d", count)
		}
		return nil
	})
}
