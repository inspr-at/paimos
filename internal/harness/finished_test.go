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
	finishedOf := func(p tenant.Principal, endpoint, session string) (finished bool, stopReasonShown bool) {
		t.Helper()
		w := f.call(p, "GET", endpoint, nil, "")
		expect(t, w, 200)
		data := decode(t, w)
		if items, ok := data["items"].([]any); ok {
			data = nil
			for _, raw := range items {
				if item := raw.(map[string]any); item["session_id"] == session {
					data = item
				}
			}
			if data == nil {
				t.Fatalf("session %s missing from %s", session, endpoint)
			}
		}
		_, shown := data["stop_reason"]
		if shown && data["stop_reason"] == nil {
			shown = false
		}
		return data["finished"] == true, shown
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

	// Reported 100% and left cleanly: finished, for everyone, with the ticket agreeing.
	session, ticket := run("clean", 100, "process_exited")
	for _, p := range []tenant.Principal{f.person, guest} {
		if got, _ := finishedOf(p, "/api/harness-sessions/live?include_inactive=true", session); !got {
			t.Fatalf("live feed did not report the clean 100%% exit as finished for %v", p.ID == guest.ID)
		}
	}
	if got, _ := finishedOf(f.person, sessionURL(session), session); !got {
		t.Fatal("session read did not report finished")
	}
	if _, shown := finishedOf(guest, "/api/harness-sessions/live?include_inactive=true", session); shown {
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
		if got, _ := finishedOf(f.person, "/api/harness-sessions/live?include_inactive=true", session); got {
			t.Fatalf("%s: live feed reads finished", tc.name)
		}
		if got, _ := finishedOf(f.person, sessionURL(session), session); got {
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
		if got, _ := finishedOf(guest, "/api/harness-sessions/live?include_inactive=true", session); got {
			t.Fatalf("stop reason %v: a withheld reason reads finished", reason)
		}
		if got, _ := finishedOf(f.person, "/api/harness-sessions/live?include_inactive=true", session); got {
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
		if got, _ := finishedOf(f.person, sessionURL(session), session); got {
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
