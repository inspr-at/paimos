// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPauseProgressHeartbeatSnapshotAndValidation(t *testing.T) {
	f := fixture(t)
	path, lease := registerPauseWorker(t, f, []string{"pause"})
	beat := func(fields map[string]any, proof string, status int) map[string]any {
		t.Helper()
		fields["phase"], fields["activity_sequence"] = "working", 1
		w := f.call(f.agent, "POST", path+"/heartbeat", fields, proof)
		expect(t, w, status)
		return decode(t, w)
	}
	fields := map[string]any{"step": "Running checks", "next_point": "After the current check", "next_point_in_min": 2.5, "finish_in_min": 5.0, "finish_outcome": "Checks complete", "interrupt": false, "command": "Go tests", "command_left_min": 1.5}
	beat(fields, "wrong-"+lease, 403)
	out := beat(fields, lease, 200)
	progress := out["pause_progress"].(map[string]any)
	for key, want := range fields {
		if key != "phase" && key != "activity_sequence" && progress[key] != want {
			t.Fatalf("planning %s = %v, want %v", key, progress[key], want)
		}
	}
	if out["command"] != nil {
		t.Fatal("current operation replaced the terminal command label")
	}
	w := f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if decode(t, w)["pause_progress"].(map[string]any)["reported_at"] != progress["reported_at"] {
		t.Fatal("read did not preserve the planning snapshot")
	}
	// Omission preserves the snapshot, including its independent freshness clock.
	out = beat(map[string]any{}, lease, 200)
	if out["pause_progress"].(map[string]any)["reported_at"] != progress["reported_at"] {
		t.Fatal("liveness heartbeat refreshed planning estimates")
	}
	for _, fields := range []map[string]any{
		{"finish_in_min": -1}, {"next_point_in_min": 1441}, {"command_left_min": "soon"},
		{"finish_in_min": true}, {"next_point": strings.Repeat("a", 241)},
		{"interrupt": "yes"}, {"command": "https://example.invalid/private"},
	} {
		beat(fields, lease, 400)
	}
	out = beat(map[string]any{}, lease, 200)
	if out["pause_progress"].(map[string]any)["finish_in_min"] != 5.0 {
		t.Fatal("rejected planning report changed the previous snapshot")
	}
	// Any new snapshot replaces omitted predictions instead of refreshing them.
	out = beat(map[string]any{"step": "Writing the handover"}, lease, 200)
	if p := out["pause_progress"].(map[string]any); p["step"] != "Writing the handover" || p["finish_in_min"] != nil || p["interrupt"] != nil {
		t.Fatal(p)
	}
	out = beat(map[string]any{"step": nil, "finish_in_min": nil}, lease, 200)
	if p := out["pause_progress"].(map[string]any); p["step"] != nil || p["finish_in_min"] != nil {
		t.Fatal(p)
	}
}

func TestLeavingHeartbeatFinishWinsNearDeadline(t *testing.T) {
	f := fixture(t)
	path, lease := registerPauseWorker(t, f, []string{"pause"})
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "finish_in_min": 1, "next_point_in_min": 5}, lease), 200)
	w := f.call(f.person, "PUT", "/api/me/leaving-at", map[string]any{"deadline_at": time.Now().Add(2 * time.Minute)}, "")
	expect(t, w, 200)
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2}, lease)
	expect(t, w, 200)
	if p := decode(t, w)["pause"].(map[string]any); p["level"] != "wrap_up" {
		t.Fatalf("fresh one-minute finish was interrupted near deadline: %v", p)
	}
}

func TestLeavingHeartbeatReplansEarlierAndCapsWrapUp(t *testing.T) {
	f := fixture(t)
	path, lease := registerPauseWorker(t, f, []string{"pause"})
	w := f.call(f.person, "PUT", "/api/me/leaving-at", map[string]any{"deadline_at": time.Now().Add(20 * time.Minute)}, "")
	expect(t, w, 200)
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "finish_in_min": 5}, lease)
	expect(t, w, 200)
	p := decode(t, w)["pause"].(map[string]any)
	start, _ := time.Parse(time.RFC3339Nano, p["starts_at"].(string))
	deadline, _ := time.Parse(time.RFC3339Nano, p["deadline_at"].(string))
	if p["level"] != "wrap_up" || p["deliver"] != true || deadline.Sub(start) != 10*time.Minute {
		t.Fatalf("replanned Wrap up did not use start-plus-limit: %v", p)
	}
}

func TestLeavingHeartbeatHandoverAndCommandSelection(t *testing.T) {
	f := fixture(t)
	for _, tc := range []struct {
		name, level string
		report      map[string]any
	}{
		{"next good point fits", "pause", map[string]any{"next_point_in_min": 0.5}},
		{"next good point misses", "pause_quickly", map[string]any{"next_point_in_min": 5, "interrupt": true}},
		{"command wait consumes budget", "pause_quickly", map[string]any{"next_point_in_min": 0.5, "command_left_min": 1.5}},
		{"command cannot be interrupted", "pause", map[string]any{"next_point_in_min": 5, "command_left_min": 1.5, "interrupt": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, lease := registerPauseWorker(t, f, []string{"pause"})
			tc.report["phase"], tc.report["activity_sequence"] = "working", 1
			expect(t, f.call(f.agent, "POST", path+"/heartbeat", tc.report, lease), 200)
			id := path[strings.LastIndex(path, "/")+1:]
			w := f.call(f.person, "PUT", "/api/me/leaving-at", map[string]any{"deadline_at": time.Now().Add(2 * time.Minute), "hosts": []string{}, "agents": []string{id}}, "")
			expect(t, w, 200)
			w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2}, lease)
			expect(t, w, 200)
			if p := decode(t, w)["pause"].(map[string]any); p["level"] != tc.level {
				t.Fatalf("level %v, want %s", p["level"], tc.level)
			}
		})
	}
}

func TestLeavingStalePlanningDoesNotReuseReadyETA(t *testing.T) {
	f := fixture(t)
	path, lease := registerPauseWorker(t, f, []string{"pause"})
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "finish_in_min": 1, "eta_ready_at": time.Now().Add(time.Minute)}, lease), 200)
	id := path[strings.LastIndex(path, "/")+1:]
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET pause_progress=jsonb_set(pause_progress,'{reported_at}',to_jsonb(clock_timestamp()-interval '1 hour')) WHERE id=$1`, id)
		return err
	})
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2}, lease), 200)
	w := f.call(f.person, "PUT", "/api/me/leaving-at", map[string]any{"deadline_at": time.Now().Add(15 * time.Minute)}, "")
	expect(t, w, 200)
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if p := decode(t, w)["pause"].(map[string]any); p["level"] != "pause" || p["deliver"] != false {
		t.Fatal("stale planning report was treated as a fresh finish")
	}
}

func TestQuickPauseWaitsForUninterruptibleCommand(t *testing.T) {
	f := fixture(t)
	body := pauseRegistration(f)
	body["management_mode"], body["advertised_capabilities"] = "managed", []string{"stop", "interrupt", "inbox"}
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", body, "")
	expect(t, w, 201)
	path, lease := "/api/projects/"+f.project+"/harness-sessions/"+decode(t, w)["id"].(string), body["worker_lease"].(string)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "interrupt": false, "command": "Database migration", "command_left_min": 2.5}, lease), 200)
	w = f.call(f.person, "POST", path+"/pause", map[string]any{"level": "pause_quickly"}, "")
	expect(t, w, 200)
	p := decode(t, w)["pause"].(map[string]any)
	requested, _ := time.Parse(time.RFC3339Nano, p["requested_at"].(string))
	deadline, _ := time.Parse(time.RFC3339Nano, p["deadline_at"].(string))
	if deadline.Sub(requested) != 3*time.Minute || p["interrupt_control_id"] != nil {
		t.Fatalf("quick pause ignored the command or its three-minute cap: %v", p)
	}
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2, "interrupt": true, "command_left_min": 0}, lease), 200)
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if decode(t, w)["pause"].(map[string]any)["interrupt_control_id"] == nil {
		t.Fatal("new interruptible command did not receive the pending quick pause")
	}
}

func TestPausePlanningPreservesTerminalCommandLabel(t *testing.T) {
	f := fixture(t)
	body := pauseRegistration(f)
	delete(body, "model")
	delete(body, "reasoning_effort")
	body["harness"], body["command"] = "terminal", "ffmpeg"
	body["parent_harness_session_id"] = f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "parent-ref-"+uid(), "parent-lease-"+uid())
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", body, "")
	expect(t, w, 201)
	path := "/api/projects/" + f.project + "/harness-sessions/" + decode(t, w)["id"].(string)
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "command": "Encoding video", "command_left_min": 2, "interrupt": false}, body["worker_lease"].(string))
	expect(t, w, 200)
	out := decode(t, w)
	if out["command"] != "ffmpeg" || out["pause_progress"].(map[string]any)["command"] != "Encoding video" {
		t.Fatal("current operation overwrote the terminal execution label")
	}
}
