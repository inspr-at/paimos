// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-437: Done needs positive evidence. A worker finished when it reported 100%
// and its launcher recorded a clean exit. Everything below is read from the real
// heartbeat and stop endpoints, then through the three places a screen looks:
// the session, the live feed (also for a viewer who may not read the stop reason)
// and the ticket row.
func TestFinishedNeedsAReportedHundredAndARecordedCleanExit(t *testing.T) {
	f := fixture(t)
	guest := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, guest, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','guest')`, guest.TenantID, guest.ID)
		return err
	})
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key='guest'`, guest.TenantID, guest.ID, f.project); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	nodes.New(f.db.App, nil).Mount(mux)
	type ticketEta struct {
		Finished   bool    `json:"finished"`
		FinishedBy string  `json:"finished_by"`
		FinishedAt *string `json:"finished_at"`
		Progress   *int    `json:"progress_pct"`
	}
	ticketRow := func(p tenant.Principal, ticket string) *ticketEta {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/nodes?within="+f.project+"&kind=ticket", nil)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		expect(t, w, 200)
		var page struct {
			Items []struct {
				ID  string     `json:"id"`
				Eta *ticketEta `json:"eta"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.ID == ticket {
				return item.Eta
			}
		}
		t.Fatalf("ticket %s not listed", ticket)
		return nil
	}
	// finishedOf reads one session's answer from an endpoint: the session itself, or the
	// live feed, where the ticket is the key a viewer without the session id can match.
	finishedOf := func(p tenant.Principal, endpoint, ticket string) (finished bool, stopReasonShown bool) {
		t.Helper()
		w := f.call(p, "GET", endpoint, nil, "")
		expect(t, w, 200)
		data := decode(t, w)
		if items, ok := data["items"].([]any); ok {
			data = nil
			for _, raw := range items {
				item := raw.(map[string]any)
				if bound, _ := item["ticket"].(map[string]any); bound != nil && bound["id"] == ticket {
					data = item
				}
			}
			if data == nil {
				t.Fatalf("ticket %s has no session in %s", ticket, endpoint)
			}
		}
		return data["finished"] == true, data["stop_reason"] != nil
	}

	n := 0
	run := func(name string, progress int, stop string) (session, ticket string) {
		t.Helper()
		n++
		ticket = uid()
		f.addNode(t, ticket, fmt.Sprintf("FIN-%d", n), "ticket", f.project, name)
		lease := fmt.Sprintf("fin-lease-%022d", n)
		session = f.registerSession(t, f.agent.ID, "worker", ticket, fmt.Sprintf("fin-ref-%016d", n), lease)
		f.beat(t, session, lease, 1, map[string]any{"progress_pct": progress})
		if stop != "" {
			expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/stop", map[string]string{"reason": stop}, lease), 200)
		}
		return session, ticket
	}
	sessionURL := func(session string) string { return "/api/projects/" + f.project + "/harness-sessions/" + session }
	const liveURL = "/api/harness-sessions/live?include_inactive=true"

	// Reported 100% and left cleanly: finished, for everyone, with the ticket agreeing.
	session, ticket := run("clean", 100, "process_exited")
	for _, p := range []tenant.Principal{f.person, guest} {
		if got, _ := finishedOf(p, liveURL, ticket); !got {
			t.Fatalf("live feed did not report the clean 100%% exit as finished for %v", p.ID == guest.ID)
		}
	}
	if got, _ := finishedOf(f.person, sessionURL(session), ticket); !got {
		t.Fatal("session read did not report finished")
	}
	if _, shown := finishedOf(guest, liveURL, ticket); shown {
		t.Fatal("the restricted viewer read the stop reason")
	}
	row := ticketRow(f.person, ticket)
	if row == nil || !row.Finished || row.Progress == nil || *row.Progress != 100 || row.FinishedBy != "worker" || row.FinishedAt == nil {
		t.Fatalf("ticket row %+v, want finished at 100%% by the worker with a time", row)
	}
	if row = ticketRow(guest, ticket); row == nil || !row.Finished {
		t.Fatalf("restricted ticket row %+v, want finished", row)
	}

	// Everything else that stopped is not finished, however much was reported.
	for _, tc := range []struct {
		name     string
		progress int
		stop     string
	}{
		{"short of 100", 99, "process_exited"},
		{"plain stop", 100, "stopped"},
		{"failed exit", 100, "process_failed"},
		{"force stop", 100, "force_stopped"},
		{"token budget", 100, "token_budget_exhausted"},
		{"turn budget", 100, "turn_budget_exhausted"},
		{"ownership lost", 100, "ownership_lost"},
	} {
		session, ticket := run(tc.name, tc.progress, tc.stop)
		if got, _ := finishedOf(f.person, liveURL, ticket); got {
			t.Fatalf("%s: live feed reads finished", tc.name)
		}
		if got, _ := finishedOf(f.person, sessionURL(session), ticket); got {
			t.Fatalf("%s: session reads finished", tc.name)
		}
		if row := ticketRow(f.person, ticket); row != nil && (row.Finished || row.FinishedBy != "") {
			t.Fatalf("%s: ticket reads finished: %+v", tc.name, row)
		}
	}

	// A silence the server closed is lost contact, and so is a stop with no reason.
	for _, reason := range []any{"heartbeat_lost", nil} {
		session, ticket := run("closed silently", 100, "")
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped', stopped_at=clock_timestamp(), stop_reason=$2 WHERE id=$1`, session, reason)
			return err
		})
		if got, _ := finishedOf(guest, liveURL, ticket); got {
			t.Fatalf("stop reason %v: a withheld reason reads finished", reason)
		}
		if got, _ := finishedOf(f.person, liveURL, ticket); got {
			t.Fatalf("stop reason %v reads finished", reason)
		}
		if row := ticketRow(f.person, ticket); row != nil && row.Finished {
			t.Fatalf("stop reason %v: ticket reads finished", reason)
		}
	}

	// Still open is not finished: a yielded worker at 100% (no working session, yet
	// not gone) and a worker that is stopping.
	for _, phase := range []string{"yielded", "stopping"} {
		session, ticket := run(phase, 100, "")
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase=$2 WHERE id=$1`, session, phase)
			return err
		})
		if row := ticketRow(f.person, ticket); row == nil || row.Finished {
			t.Fatalf("%s at 100%%: ticket row %+v must not read finished", phase, row)
		}
		if got, _ := finishedOf(f.person, sessionURL(session), ticket); got {
			t.Fatalf("%s session reads finished", phase)
		}
	}

	// Work that restarted is not done any more: a new session on a finished ticket
	// holds it open, and a later early end replaces the earlier clean one.
	_, reopened := run("reopened", 100, "process_exited")
	if row := ticketRow(f.person, reopened); row == nil || !row.Finished {
		t.Fatalf("reopened baseline %+v", row)
	}
	n++
	lease := fmt.Sprintf("fin-lease-%022d", n)
	again := f.registerSession(t, f.agent.ID, "worker", reopened, fmt.Sprintf("fin-ref-%016d", n), lease)
	if row := ticketRow(f.person, reopened); row != nil && row.Finished {
		t.Fatalf("a new open session left the ticket finished: %+v", row)
	}
	f.beat(t, again, lease, 1, map[string]any{"progress_pct": 40})
	expect(t, f.call(f.agent, "POST", sessionURL(again)+"/stop", map[string]string{"reason": "stopped"}, lease), 200)
	if row := ticketRow(f.person, reopened); row != nil && row.Finished {
		t.Fatalf("an earlier clean exit outlived a later early end: %+v", row)
	}
}

// AEON-437, fix round 2: finished is a required boolean in every payload a screen
// renders a session from, so no client needs a fallback that derives Done from the
// percent and the stop reason. Registration, heartbeat, yield and stop responses,
// the stored event snapshots, detail, list and live all carry it, false included.
func TestFinishedIsARequiredBooleanInEveryPayload(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	mustBe := func(what string, data map[string]any, want bool) {
		t.Helper()
		got, present := data["finished"]
		if !present {
			t.Fatalf("%s omits finished", what)
		}
		if b, ok := got.(bool); !ok || b != want {
			t.Fatalf("%s finished = %#v, want %v", what, got, want)
		}
	}
	ticket := uid()
	f.addNode(t, ticket, "REQ-1", "ticket", f.project, "required flag")
	lease := "req-lease-0000000000000000000001"
	registration := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host",
		"harness_session_ref": "req-ref-0000000000000001", "worker_lease": lease, "management_mode": "managed",
		"role": "worker", "ticket_node_id": ticket, "work_shape": "ship",
	}
	w := f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	registered := decode(t, w)
	mustBe("registration", registered, false)
	id := registered["id"].(string)
	path := base + "/" + id
	w = f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	mustBe("registration replay", decode(t, w), false)

	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "progress_pct": 100}, lease)
	expect(t, w, 200)
	mustBe("heartbeat at 100%", decode(t, w), false)
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	yielded, _ := decode(t, w)["session"].(map[string]any)
	mustBe("yield", yielded, false)
	w = f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "process_exited"}, lease)
	expect(t, w, 200)
	mustBe("stop at 100% with a clean exit", decode(t, w), true)

	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	mustBe("detail", decode(t, w), true)
	w = f.call(f.person, "GET", base, nil, "")
	expect(t, w, 200)
	listed := false
	for _, raw := range decode(t, w)["items"].([]any) {
		if item := raw.(map[string]any); item["id"] == id {
			mustBe("list", item, true)
			listed = true
		}
	}
	if !listed {
		t.Fatal("session missing from the list")
	}

	// The snapshots in the event log carry it too; an event a screen renders a
	// session from is never missing the flag.
	var snapshots, booleans int
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE jsonb_typeof(after->'finished')='boolean')
			FROM events WHERE type LIKE 'harness.%' AND after->>'id'=$1`, id).Scan(&snapshots, &booleans)
	})
	if snapshots < 3 || booleans != snapshots {
		t.Fatalf("%d of %d event snapshots carry a boolean finished", booleans, snapshots)
	}
}

// A stopped session at 100% whose launcher never recorded a reason is not finished,
// and the answer is an explicit false, never a missing field and never an unknown.
func TestFinishedIsExplicitFalseWithoutAStopReason(t *testing.T) {
	f := fixture(t)
	var unknown *bool
	var answers [4]bool
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT aeon_session_finished(now(), NULL, 100::smallint)`).Scan(&unknown); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT aeon_session_finished(NULL, NULL, NULL),
			aeon_session_finished(now(), 'process_exited', NULL),
			aeon_session_finished(now(), NULL, NULL),
			aeon_session_finished(now(), 'process_exited', 100::smallint)`).Scan(&answers[0], &answers[1], &answers[2], &answers[3])
	})
	if unknown == nil || *unknown {
		t.Fatalf("NULL stop reason answered %v, want an explicit false", unknown)
	}
	if answers != [4]bool{false, false, false, true} {
		t.Fatalf("function answers %v", answers)
	}

	ticket := uid()
	f.addNode(t, ticket, "REQ-2", "ticket", f.project, "no reason")
	lease := "req-lease-0000000000000000000002"
	session := f.registerSession(t, f.agent.ID, "worker", ticket, "req-ref-0000000000000002", lease)
	f.beat(t, session, lease, 1, map[string]any{"progress_pct": 100})
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped', stopped_at=clock_timestamp(), stop_reason=NULL WHERE id=$1`, session)
		return err
	})
	explicitFalse := func(what string, data map[string]any) {
		t.Helper()
		if got, present := data["finished"]; !present || got != false {
			t.Fatalf("%s finished = %#v (present %v), want an explicit false", what, got, present)
		}
	}
	w := f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/"+session, nil, "")
	expect(t, w, 200)
	explicitFalse("detail", decode(t, w))
	w = f.call(f.person, "GET", "/api/harness-sessions/live?include_inactive=true", nil, "")
	expect(t, w, 200)
	found := false
	for _, raw := range decode(t, w)["items"].([]any) {
		item := raw.(map[string]any)
		if bound, _ := item["ticket"].(map[string]any); bound != nil && bound["id"] == ticket {
			explicitFalse("live", item)
			found = true
		}
	}
	if !found {
		t.Fatal("session missing from the live feed")
	}
}

// AEON-437, fix round 2: the ticket list sorts by the progress it displays. A
// finished ticket shows 100, so ascending puts it after the open work and
// descending puts it first; a ticket with nothing reported is last both ways.
func TestProgressSortUsesTheDisplayedCompletionAwarePercent(t *testing.T) {
	f := fixture(t)
	mux := http.NewServeMux()
	nodes.New(f.db.App, nil).Mount(mux)
	n := 0
	ticketAt := func(key string, progress int, stop string) string {
		t.Helper()
		n++
		ticket := uid()
		f.addNode(t, ticket, key, "ticket", f.project, key)
		lease := fmt.Sprintf("sort-lease-%022d", n)
		session := f.registerSession(t, f.agent.ID, "worker", ticket, fmt.Sprintf("sort-ref-%016d", n), lease)
		f.beat(t, session, lease, 1, map[string]any{"progress_pct": progress})
		if stop != "" {
			expect(t, f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+session+"/stop", map[string]string{"reason": stop}, lease), 200)
		}
		return ticket
	}
	ticketAt("SORT-1", 40, "")
	ticketAt("SORT-2", 100, "process_exited")
	ticketAt("SORT-3", 70, "")
	ticketAt("SORT-4", 100, "stopped") // ended early at 100%: no percent survives, unknown
	f.addNode(t, uid(), "SORT-5", "ticket", f.project, "nothing reported")
	order := func(sort string) []string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/nodes?within="+f.project+"&kind=ticket&sort="+sort+"&limit=100", nil)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), f.person))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		expect(t, w, 200)
		var page struct {
			Items []struct {
				Key string `json:"key"`
				Eta *struct {
					Progress *int `json:"progress_pct"`
				} `json:"eta"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, item := range page.Items {
			if len(item.Key) >= 5 && item.Key[:5] == "SORT-" {
				keys = append(keys, item.Key)
				if item.Key == "SORT-2" && (item.Eta == nil || item.Eta.Progress == nil || *item.Eta.Progress != 100) {
					t.Fatalf("finished ticket shows %+v, want 100", item.Eta)
				}
			}
		}
		return keys
	}
	for _, tc := range []struct {
		sort string
		want []string
	}{
		{"progress", []string{"SORT-1", "SORT-3", "SORT-2", "SORT-4", "SORT-5"}},
		{"-progress", []string{"SORT-2", "SORT-3", "SORT-1", "SORT-4", "SORT-5"}},
	} {
		got := order(tc.sort)
		// The two rows with no percent tie; their order is the key tiebreak, so
		// compare the known prefix exactly and the unknown tail as a set.
		if fmt.Sprint(got[:3]) != fmt.Sprint(tc.want[:3]) || len(got) != 5 || !(got[3] == "SORT-4" && got[4] == "SORT-5" || got[3] == "SORT-5" && got[4] == "SORT-4") {
			t.Fatalf("sort=%s gave %v, want %v", tc.sort, got, tc.want)
		}
	}
}
