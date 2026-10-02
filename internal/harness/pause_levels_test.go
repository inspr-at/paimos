// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func registerPauseWorker(t *testing.T, f *harnessFixture, caps []string) (string, string) {
	t.Helper()
	body := pauseRegistration(f)
	body["advertised_capabilities"] = caps
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", body, "")
	expect(t, w, 201)
	return "/api/projects/" + f.project + "/harness-sessions/" + decode(t, w)["id"].(string), body["worker_lease"].(string)
}

func TestPauseLevelsDefaultAndCapabilityHonesty(t *testing.T) {
	f := fixture(t)
	path, _ := registerPauseWorker(t, f, []string{"pause"})
	w := f.call(f.person, "GET", "/api/me/agent-pause-settings", nil, "")
	expect(t, w, 200)
	if decode(t, w)["default_level"] != "pause" {
		t.Fatal("missing default pause")
	}
	expect(t, f.call(f.person, "PUT", "/api/me/agent-pause-settings", map[string]any{"default_level": "pause_quickly"}, ""), 200)
	w = f.call(f.person, "POST", path+"/pause", map[string]any{"note": "Save the failing fixture for the next worker."}, "")
	expect(t, w, 200)
	p := decode(t, w)["pause"].(map[string]any)
	if p["level"] != "pause_quickly" || p["note"] == "" {
		t.Fatal(p)
	}
	at, _ := time.Parse(time.RFC3339Nano, p["requested_at"].(string))
	deadline, _ := time.Parse(time.RFC3339Nano, p["deadline_at"].(string))
	if deadline.Sub(at) != 2*time.Minute {
		t.Fatal("quick pause limit is not two minutes")
	}
	path, _ = registerPauseWorker(t, f, []string{"owned_stop_v1"})
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	out := decode(t, w)
	if levels := out["supported_pause_levels"].([]any); len(levels) != 1 || levels[0] != "stop_now" || out["pause_can_interrupt"] != false {
		t.Fatal(out)
	}
	expect(t, f.call(f.person, "POST", path+"/pause", map[string]any{"level": "pause"}, ""), 409)
	w = f.call(f.person, "POST", path+"/pause", map[string]any{"level": "stop_now"}, "")
	expect(t, w, 200)
	if p := decode(t, w)["pause"].(map[string]any); p["stop_requested"] != true || p["stop_control_id"] == "" {
		t.Fatal(p)
	}
	// A different person gets a workspace-specific default, never our preference.
	other := tenant.Principal{TenantID: f.person.TenantID, ID: uid(), Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','another person')`, other.TenantID, other.ID)
		return err
	})
	// Existing fixture admin binding gives this role only to f.person; direct
	// tenant reads prove the restrictive per-person policy below.
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), other), f.db.App, other.TenantID, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM person_pause_settings`).Scan(&count)
		if err == nil && count != 0 {
			t.Error("foreign default visible")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(f.agent, "PUT", "/api/me/agent-pause-settings", map[string]any{"default_level": "stop_now"}, ""), 403)
	expect(t, f.call(f.person, "PUT", "/api/me/agent-pause-settings", map[string]any{"default_level": "invalid"}, ""), 400)
}

func TestLeavingAtSchedulesCancelsAndPreservesPausedSessions(t *testing.T) {
	f := fixture(t)
	waiting, waitingLease := registerPauseWorker(t, f, []string{"pause"})
	finishing, finishingLease := registerPauseWorker(t, f, []string{"pause"})
	stopOnly, _ := registerPauseWorker(t, f, []string{"owned_stop_v1"})
	expect(t, f.call(f.agent, "POST", finishing+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "eta_ready_at": time.Now().Add(5 * time.Minute).Format(time.RFC3339Nano)}, finishingLease), 200)
	// Force sub-microsecond input so retry coverage does not depend on host clock
	// precision (Darwin often supplies only microseconds, unlike Linux CI).
	deadline := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond).Format(time.RFC3339Nano)
	body := map[string]any{"deadline_at": deadline, "reason": "Leaving", "note": "Continue tomorrow"}
	w := f.call(f.person, "PUT", "/api/me/leaving-at", body, "")
	expect(t, w, 200)
	report := decode(t, w)
	id := report["request_id"].(string)
	if len(report["items"].([]any)) != 3 {
		t.Fatal(report)
	}
	plans := map[string]map[string]any{}
	for _, item := range report["items"].([]any) {
		s := item.(map[string]any)
		plans[s["id"].(string)] = s["pause"].(map[string]any)
	}
	key := func(path string) string { return path[strings.LastIndex(path, "/")+1:] }
	if plans[key(waiting)]["level"] != "pause" || plans[key(waiting)]["deliver"] != false || plans[key(finishing)]["level"] != "wrap_up" || plans[key(stopOnly)]["level"] != "stop_now" || plans[key(stopOnly)]["deliver"] != false {
		t.Fatal(plans)
	}
	expect(t, f.call(f.agent, "POST", waiting+"/pause-plan", map[string]any{"control_id": plans[key(waiting)]["control_id"], "handover_point": "Too early"}, waitingLease), 409)
	w = f.call(f.person, "PUT", "/api/me/leaving-at", body, "")
	expect(t, w, 200)
	if decode(t, w)["request_id"] != id {
		t.Fatal("deadline retry created another request")
	}
	for _, item := range decode(t, w)["items"].([]any) {
		s := item.(map[string]any)
		if s["pause"].(map[string]any)["control_id"] != plans[s["id"].(string)]["control_id"] {
			t.Fatal("deadline retry replaced a pause control")
		}
	}
	finishPaused(t, f, finishing, finishingLease, plans[key(finishing)]["control_id"].(string))
	w = f.call(f.person, "DELETE", "/api/me/leaving-at", nil, "")
	expect(t, w, 200)
	if out := decode(t, w); out["deadline_at"] != nil || out["request_id"] != nil || out["stop_in_flight"] != false {
		t.Fatal(out)
	}
	for path, state := range map[string]string{waiting: "cancelled", stopOnly: "cancelled", finishing: "paused"} {
		w = f.call(f.person, "GET", path, nil, "")
		expect(t, w, 200)
		if decode(t, w)["pause"].(map[string]any)["state"] != state {
			t.Fatal(path)
		}
	}
	expect(t, f.call(f.agent, "PUT", "/api/me/leaving-at", body, ""), 403)
	expect(t, f.call(f.person, "PUT", "/api/me/leaving-at", map[string]any{"deadline_at": time.Now().Add(-time.Second)}, ""), 400)
}

func TestLeavingAtDeadlinePrecisionAndReplacement(t *testing.T) {
	f := fixture(t)
	path, _ := registerPauseWorker(t, f, []string{"pause"})
	deadline := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
	put := func(at time.Time) map[string]any {
		t.Helper()
		w := f.call(f.person, "PUT", "/api/me/leaving-at", map[string]any{"deadline_at": at.Format(time.RFC3339Nano)}, "")
		expect(t, w, 200)
		return decode(t, w)
	}
	first := put(deadline)
	want := deadline.Truncate(time.Microsecond)
	stored, err := time.Parse(time.RFC3339Nano, first["deadline_at"].(string))
	if err != nil || !stored.Equal(want) {
		t.Fatalf("stored deadline %v, want %v (parse error: %v)", stored, want, err)
	}
	w := f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if decode(t, w)["pause"].(map[string]any)["deadline_at"] != first["deadline_at"] {
		t.Fatal("session and person deadlines differ")
	}
	// A retry may carry the original input, the returned deadline, a different
	// zone, or nanoseconds within the same stored microsecond.
	for _, at := range []time.Time{deadline, want, deadline.In(time.FixedZone("offset", 2*60*60)), want.Add(999 * time.Nanosecond)} {
		if put(at)["request_id"] != first["request_id"] {
			t.Fatalf("equivalent deadline %v replaced the request", at)
		}
	}
	if put(want.Add(time.Microsecond))["request_id"] == first["request_id"] {
		t.Fatal("different stored deadline did not replace the request")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var requested, cancelled int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='harness.leaving_requested'),count(*) FILTER(WHERE type='harness.leaving_cancelled') FROM events`).Scan(&requested, &cancelled)
		if requested != 2 || cancelled != 1 {
			t.Errorf("deadline audit requested=%d cancelled=%d, want 2 and 1", requested, cancelled)
		}
		return err
	})
}

func TestLeavingDeadlineEscalatesStopsOnceAndIsAudited(t *testing.T) {
	f := fixture(t)
	path, lease := registerPauseWorker(t, f, []string{"pause", "owned_stop_v1"})
	w := f.call(f.person, "PUT", "/api/me/leaving-at", map[string]any{"deadline_at": time.Now().Add(15 * time.Minute).UTC()}, "")
	expect(t, w, 200)
	id := path[strings.LastIndex(path, "/")+1:]
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET pause_record=jsonb_set(pause_record,'{deadline_at}',to_jsonb(clock_timestamp()+interval '90 seconds')) WHERE id=$1`, id)
		return err
	})
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, lease)
	expect(t, w, 200)
	if p := decode(t, w)["pause"].(map[string]any); p["level"] != "pause_quickly" || p["deliver"] != true {
		t.Fatal(p)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET pause_record=jsonb_set(pause_record,'{deadline_at}',to_jsonb(clock_timestamp()-interval '1 second')) WHERE id=$1`, id)
		return err
	})
	if n := sweep(t, f); n != 0 {
		t.Fatalf("stop request fabricated %d exited processes", n)
	}
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2}, lease)
	expect(t, w, 200)
	p := decode(t, w)["pause"].(map[string]any)
	control := p["stop_control_id"].(string)
	if p["stop_requested"] != true || p["level"] != "stop_now" || p["stop_expires_in_ms"].(float64) <= 0 {
		t.Fatal(p)
	}
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 3}, lease)
	expect(t, w, 200)
	if decode(t, w)["pause"].(map[string]any)["stop_control_id"] != control {
		t.Fatal("duplicate deadline stop")
	}
	w = f.call(f.person, "DELETE", "/api/me/leaving-at", nil, "")
	expect(t, w, 200)
	if decode(t, w)["stop_in_flight"] != true {
		t.Fatal("claimed stop was silently recalled")
	}
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "queued"}, lease), 400)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "owned_group_signalled_root_exited"}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "stopped"}, lease), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var stops, escalations int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='harness.pause_stop_requested'),count(*) FILTER(WHERE type='harness.pause_level_changed') FROM events WHERE after->>'id'=$1`, id).Scan(&stops, &escalations)
		if stops != 1 || escalations != 1 {
			t.Errorf("audit stop=%d escalation=%d", stops, escalations)
		}
		return err
	})
}

func TestStopOnlyBatchSkipsCooperativeLevelsButAcceptsStopNow(t *testing.T) {
	f := fixture(t)
	_, _ = registerPauseWorker(t, f, []string{"owned_stop_v1"})
	base := "/api/projects/" + f.project + "/harness-sessions/pause"
	w := f.call(f.person, "POST", base, map[string]any{"level": "pause"}, "")
	expect(t, w, http.StatusOK)
	out := decode(t, w)
	if len(out["items"].([]any)) != 0 || len(out["skipped"].([]any)) != 1 {
		t.Fatal(out)
	}
	w = f.call(f.person, "POST", base, map[string]any{"level": "stop_now"}, "")
	expect(t, w, 200)
	out = decode(t, w)
	if len(out["items"].([]any)) != 1 || len(out["skipped"].([]any)) != 0 {
		t.Fatal(out)
	}
}
