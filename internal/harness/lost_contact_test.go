// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
)

type lostSession struct{ id, lease, ref, path string }

func registerUnmanaged(t *testing.T, f *harnessFixture, managed bool) lostSession {
	t.Helper()
	base := "/api/projects/" + f.project + "/harness-sessions"
	s := lostSession{lease: "lost-lease-" + uid(), ref: "lost-ref-" + uid()}
	body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "cursor", "host": "lost-host", "harness_session_ref": s.ref, "worker_lease": s.lease, "management_mode": "unmanaged", "role": "worker"}
	if managed {
		order, run := stateRun(t, f, f.project, "running", "LOST-10")
		body["management_mode"], body["run_id"], body["work_order_id"] = "managed", run, order
	}
	w := f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	s.id = decode(t, w)["id"].(string)
	s.path = base + "/" + s.id
	return s
}

// age moves a session's last sign of life (and optionally its stop) into the past.
func age(t *testing.T, f *harnessFixture, id, heartbeat string, neverBeat bool) {
	t.Helper()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET created_at=clock_timestamp()-interval '2 hours',
			heartbeat_at=CASE WHEN $3 THEN NULL ELSE clock_timestamp()-$2::interval END WHERE id=$1`, id, heartbeat, neverBeat)
		if err == nil && neverBeat {
			_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET created_at=clock_timestamp()-$2::interval WHERE id=$1`, id, heartbeat)
		}
		return err
	})
}

func sessionState(t *testing.T, f *harnessFixture, s lostSession) map[string]any {
	t.Helper()
	w := f.call(f.person, "GET", s.path, nil, "")
	expect(t, w, 200)
	return decode(t, w)
}

func sweep(t *testing.T, f *harnessFixture) int {
	t.Helper()
	n, err := harness.SweepLostContact(t.Context(), f.db.App, f.person.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLostContactSweepClosesOnlySilentUnmanagedSessions(t *testing.T) {
	f := fixture(t)
	silent, never, fresh, managed, stopped := registerUnmanaged(t, f, false), registerUnmanaged(t, f, false), registerUnmanaged(t, f, false), registerUnmanaged(t, f, true), registerUnmanaged(t, f, false)
	for _, s := range []lostSession{silent, fresh, managed, stopped} {
		expect(t, f.call(f.agent, "POST", s.path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, s.lease), 200)
	}
	expect(t, f.call(f.agent, "POST", stopped.path+"/stop", map[string]any{"reason": "process_exited"}, stopped.lease), 200)
	age(t, f, silent.id, "16 minutes", false)
	age(t, f, never.id, "16 minutes", true)
	age(t, f, fresh.id, "14 minutes", false)
	age(t, f, managed.id, "3 hours", false)
	age(t, f, stopped.id, "3 hours", false)

	if n := sweep(t, f); n != 2 {
		t.Fatalf("closed %d, want 2", n)
	}
	for _, s := range []lostSession{silent, never} {
		got := sessionState(t, f, s)
		if got["phase"] != "stopped" || got["stop_reason"] != "heartbeat_lost" || got["stopped_at"] == nil || got["archived_at"] != nil || got["has_problem"] != false {
			t.Fatalf("silent session not lost: %v", got)
		}
	}
	if got := sessionState(t, f, fresh); got["stopped_at"] != nil {
		t.Fatalf("fresh session closed: %v", got)
	}
	if got := sessionState(t, f, managed); got["stopped_at"] != nil {
		t.Fatalf("managed session closed by the sweeper: %v", got)
	}
	if got := sessionState(t, f, stopped); got["stop_reason"] != "process_exited" {
		t.Fatalf("stopped session rewritten: %v", got)
	}
	if n := sweep(t, f); n != 0 {
		t.Fatalf("second sweep closed %d", n)
	}
	// The existing stop event, attributed to System, carries the reason.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events e JOIN principals p ON p.id=e.actor_principal_id
			WHERE e.type='harness.stopped' AND e.after->>'id'=ANY($1::text[]) AND e.after->>'stop_reason'='heartbeat_lost' AND p.name='System' AND 'system'=ANY(p.roles)`,
			[]string{silent.id, never.id}).Scan(&count)
		if err == nil && count != 2 {
			t.Fatalf("stop events %d", count)
		}
		return err
	})
}

func TestLostContactThresholdIsTenantSetting(t *testing.T) {
	f := fixture(t)
	w := f.call(f.person, "GET", "/api/settings/heartbeat-lost", nil, "")
	expect(t, w, 200)
	if decode(t, w)["heartbeat_lost_minutes"] != float64(15) {
		t.Fatalf("default threshold: %s", w.Body.String())
	}
	for _, bad := range []int{4, 1441} {
		expect(t, f.call(f.person, "PUT", "/api/settings/heartbeat-lost", map[string]any{"heartbeat_lost_minutes": bad}, ""), 400)
	}
	expect(t, f.call(f.agent, "PUT", "/api/settings/heartbeat-lost", map[string]any{"heartbeat_lost_minutes": 30}, ""), 403)
	expect(t, f.call(f.person, "PUT", "/api/settings/heartbeat-lost", map[string]any{"heartbeat_lost_minutes": 30}, ""), 200)
	w = f.call(f.person, "GET", "/api/settings/heartbeat-lost", nil, "")
	if decode(t, w)["heartbeat_lost_minutes"] != float64(30) {
		t.Fatalf("threshold not saved: %s", w.Body.String())
	}
	s := registerUnmanaged(t, f, false)
	age(t, f, s.id, "20 minutes", false)
	if n := sweep(t, f); n != 0 {
		t.Fatalf("closed %d under a 30-minute threshold", n)
	}
	age(t, f, s.id, "31 minutes", false)
	if n := sweep(t, f); n != 1 {
		t.Fatalf("closed %d after 31 minutes", n)
	}
}

func TestLostContactRevivesOnSameWorkerProof(t *testing.T) {
	f := fixture(t)
	s := registerUnmanaged(t, f, false)
	beat := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}
	expect(t, f.call(f.agent, "POST", s.path+"/heartbeat", beat, s.lease), 200)
	age(t, f, s.id, "20 minutes", false)
	if sweep(t, f) != 1 {
		t.Fatal("not swept")
	}
	// Another lease is still no proof; the generation stays closed.
	expect(t, f.call(f.agent, "POST", s.path+"/heartbeat", beat, "wrong-lease-"+uid()), 403)
	// Other worker writes do not revive; only a heartbeat does.
	expect(t, f.call(f.agent, "POST", s.path+"/yield", map[string]any{}, s.lease), 403)
	beat["activity_sequence"], beat["activity"] = 2, "idle"
	w := f.call(f.agent, "POST", s.path+"/heartbeat", beat, s.lease)
	expect(t, w, 200)
	got := decode(t, w)
	if got["phase"] != "working" || got["stopped_at"] != nil || got["stop_reason"] != nil || got["activity"] != "idle" {
		t.Fatalf("not revived: %v", got)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.revived' AND actor_principal_id=$1 AND after->>'id'=$2`, f.agent.ID, s.id).Scan(&count)
		if err == nil && count != 1 {
			t.Fatalf("revived events %d", count)
		}
		return err
	})
	// Fresh again, so the next sweep leaves it alone.
	if n := sweep(t, f); n != 0 {
		t.Fatalf("revived session swept again: %d", n)
	}
	// A worker stop still ends it for good.
	expect(t, f.call(f.agent, "POST", s.path+"/stop", map[string]any{"reason": "process_exited"}, s.lease), 200)
	beat["activity_sequence"] = 3
	expect(t, f.call(f.agent, "POST", s.path+"/heartbeat", beat, s.lease), 403)
}

func TestLostContactStaysClosedWhenRemovedOrReplaced(t *testing.T) {
	f := fixture(t)
	beat := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}
	removed, replaced := registerUnmanaged(t, f, false), registerUnmanaged(t, f, false)
	age(t, f, removed.id, "20 minutes", false)
	age(t, f, replaced.id, "20 minutes", false)
	if sweep(t, f) != 2 {
		t.Fatal("not swept")
	}
	expect(t, f.call(f.person, "POST", removed.path+"/remove", map[string]any{"reason": "Clean up a lost session"}, ""), 200)
	expect(t, f.call(f.agent, "POST", removed.path+"/heartbeat", beat, removed.lease), 410)
	// The launcher registered a new generation for the same session reference.
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "cursor", "host": "lost-host", "harness_session_ref": replaced.ref, "worker_lease": "next-lease-" + uid(), "management_mode": "unmanaged", "role": "worker"}, "")
	expect(t, w, 201)
	if decode(t, w)["id"] == replaced.id {
		t.Fatal("registration reused the lost generation")
	}
	expect(t, f.call(f.agent, "POST", replaced.path+"/heartbeat", beat, replaced.lease), 403)
	if got := sessionState(t, f, replaced); got["stop_reason"] != "heartbeat_lost" {
		t.Fatalf("replaced generation changed: %v", got)
	}
}

func TestLostContactWorkerStopReplacesReason(t *testing.T) {
	f := fixture(t)
	s := registerUnmanaged(t, f, false)
	age(t, f, s.id, "20 minutes", false)
	if sweep(t, f) != 1 {
		t.Fatal("not swept")
	}
	w := f.call(f.agent, "POST", s.path+"/stop", map[string]any{"reason": "process_exited"}, s.lease)
	expect(t, w, 200)
	if got := decode(t, w); got["stop_reason"] != "process_exited" || got["phase"] != "stopped" {
		t.Fatalf("stop after lost contact: %v", got)
	}
	expect(t, f.call(f.agent, "POST", s.path+"/stop", map[string]any{"reason": "process_exited"}, s.lease), 403)
}

func TestLostContactSweepIsSingleRunner(t *testing.T) {
	f := fixture(t)
	s := registerUnmanaged(t, f, false)
	age(t, f, s.id, "20 minutes", false)
	holder, err := f.db.App.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(t.Context()) }()
	if _, err = holder.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,91))`, "harness-lost-contact:"+f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if n := sweep(t, f); n != 0 {
		t.Fatalf("second runner closed %d", n)
	}
	if err = holder.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := sweep(t, f); n != 1 {
		t.Fatalf("runner closed %d after the lock was free", n)
	}
}

func TestCurrentViewHidesSessionsEndedADayAgo(t *testing.T) {
	f := fixture(t)
	live, recent, old, oldRemoved := registerUnmanaged(t, f, false), registerUnmanaged(t, f, false), registerUnmanaged(t, f, false), registerUnmanaged(t, f, false)
	for _, s := range []lostSession{recent, old} {
		expect(t, f.call(f.agent, "POST", s.path+"/stop", map[string]any{"reason": "stopped"}, s.lease), 200)
	}
	expect(t, f.call(f.person, "POST", oldRemoved.path+"/remove", map[string]any{"reason": "Old ghost"}, ""), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp()-interval '25 hours',archived_at=CASE WHEN archived_at IS NOT NULL THEN clock_timestamp()-interval '25 hours' END WHERE id=ANY($1::uuid[])`, []string{old.id, oldRemoved.id})
		return err
	})
	ids := func(view string) map[string]bool {
		w := f.call(f.person, "GET", "/api/harness-sessions?project="+f.project+view, nil, "")
		expect(t, w, 200)
		out := map[string]bool{}
		for _, item := range decode(t, w)["items"].([]any) {
			out[item.(map[string]any)["id"].(string)] = true
		}
		return out
	}
	current := ids("&view=current")
	if !current[live.id] || !current[recent.id] || current[old.id] || current[oldRemoved.id] {
		t.Fatalf("current view: %v", current)
	}
	for _, view := range []string{"", "&view=all"} {
		all := ids(view)
		for _, s := range []lostSession{live, recent, old, oldRemoved} {
			if !all[s.id] {
				t.Fatalf("history view %q misses %s", view, s.id)
			}
		}
	}
	expect(t, f.call(f.person, "GET", "/api/harness-sessions?view=recent", nil, ""), 400)
	// A cursor from one view is not valid in the other.
	w := f.call(f.person, "GET", "/api/harness-sessions?limit=1&view=current", nil, "")
	expect(t, w, 200)
	if next, _ := decode(t, w)["next_cursor"].(string); next != "" {
		expect(t, f.call(f.person, "GET", "/api/harness-sessions?limit=1&cursor="+next, nil, ""), 400)
	}
}

// Keep the helpers honest about RLS: the sweeper sees only its own tenant.
func TestLostContactSweepIsTenantScoped(t *testing.T) {
	f := fixture(t)
	s := registerUnmanaged(t, f, false)
	age(t, f, s.id, "20 minutes", false)
	n, err := harness.SweepLostContact(t.Context(), f.db.App, f.foreign.TenantID)
	if err != nil || n != 0 {
		t.Fatalf("foreign sweep closed %d: %v", n, err)
	}
	var visible int
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE id=$1`, s.id).Scan(&visible)
	})
	if err != nil || visible != 0 || !strings.HasPrefix(s.path, "/api/projects/") {
		t.Fatalf("tenant isolation: %d %v", visible, err)
	}
	if sweep(t, f) != 1 {
		t.Fatal("own tenant not swept")
	}
}

func TestRemovalUndoRestoresTheRecord(t *testing.T) {
	f := fixture(t)
	events.New(f.db.App, events.WithUndoHandlers(harness.UndoHandlers())).Mount(f.mux)
	remove := func(s lostSession) float64 {
		t.Helper()
		w := f.call(f.person, "POST", s.path+"/remove", map[string]any{"reason": "Removed from Agents by a person"}, "")
		expect(t, w, 200)
		id, _ := decode(t, w)["event_id"].(float64)
		if id < 1 {
			t.Fatalf("remove answered no event: %s", w.Body.String())
		}
		return id
	}
	undo := func(id float64) string { return "/api/events/" + strconv.FormatInt(int64(id), 10) + "/undo" }
	beat := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}

	stopped := registerUnmanaged(t, f, false)
	expect(t, f.call(f.agent, "POST", stopped.path+"/stop", map[string]any{"reason": "process_exited"}, stopped.lease), 200)
	event := remove(stopped)
	// Retrying the removal has nothing new to undo.
	if again := decode(t, f.call(f.person, "POST", stopped.path+"/remove", map[string]any{"reason": "again"}, "")); again["event_id"] != nil {
		t.Fatalf("retry answered an event: %v", again)
	}
	expect(t, f.call(f.agent, "POST", undo(event), nil, ""), 403)
	expect(t, f.call(f.person, "POST", undo(event), nil, ""), 201)
	got := sessionState(t, f, stopped)
	if got["archived_at"] != nil || got["phase"] != "stopped" || got["stop_reason"] != "process_exited" {
		t.Fatalf("stopped record not restored: %v", got)
	}
	expect(t, f.call(f.person, "POST", undo(event), nil, ""), 409)

	// A silent live session comes back live; its worker's lease is proof again.
	silent := registerUnmanaged(t, f, false)
	expect(t, f.call(f.agent, "POST", silent.path+"/heartbeat", beat, silent.lease), 200)
	event = remove(silent)
	expect(t, f.call(f.person, "POST", undo(event), nil, ""), 201)
	got = sessionState(t, f, silent)
	if got["archived_at"] != nil || got["stopped_at"] != nil || got["phase"] != "working" || got["stop_reason"] != nil {
		t.Fatalf("live record not restored: %v", got)
	}
	beat["activity_sequence"] = 2
	expect(t, f.call(f.agent, "POST", silent.path+"/heartbeat", beat, silent.lease), 200)

	// While removed, the reference is revoked, so nothing can take it before the undo.
	replaced := registerUnmanaged(t, f, false)
	event = remove(replaced)
	base := "/api/projects/" + f.project + "/harness-sessions"
	expect(t, f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "cursor", "host": "lost-host", "harness_session_ref": replaced.ref, "worker_lease": "newer-lease-" + uid(), "management_mode": "unmanaged", "role": "worker"}, ""), 409)
	expect(t, f.call(f.person, "POST", undo(event), nil, ""), 201)
}
