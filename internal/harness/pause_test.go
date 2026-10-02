// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func pauseRegistration(f *harnessFixture) map[string]any {
	return map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "advertised_capabilities": []string{"pause"}, "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "pause-ref-" + uid(), "worker_lease": "pause-lease-" + uid(), "ticket_node_id": f.ticket, "work_shape": "ship", "worktree": "/owned/worktree", "branch": "work/owned", "model": "fixture-model", "reasoning_effort": "high"}
}

func pauseHandover() map[string]any {
	return map[string]any{"state": "Current transaction finished; WIP committed.", "next_steps": []string{"Run the remaining checks.", "Continue from the saved branch."}, "open_questions": []string{}, "worktree_state": "committed", "commit_sha": strings.Repeat("a", 40)}
}

func pauseSession(t *testing.T, f *harnessFixture, body map[string]any) (path, lease, control string) {
	t.Helper()
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	path = base + "/" + decode(t, w)["id"].(string)
	lease = body["worker_lease"].(string)
	w = f.call(f.person, "POST", path+"/pause", map[string]any{"reason": "Reboot the workstation"}, "")
	expect(t, w, 200)
	control = decode(t, w)["pause"].(map[string]any)["control_id"].(string)
	return
}

func finishPaused(t *testing.T, f *harnessFixture, path, lease, control string) {
	t.Helper()
	body := map[string]any{"control_id": control, "handover_point": "After the current commit and rollback check"}
	expect(t, f.call(f.agent, "POST", path+"/pause-plan", body, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "paused", "handover": pauseHandover()}, lease), 200)
}

func TestPauseControlSurvivesLostContactAndRevive(t *testing.T) {
	for _, planned := range []bool{false, true} {
		t.Run(fmt.Sprintf("planned=%t", planned), func(t *testing.T) {
			f := fixture(t)
			path, lease, control := pauseSession(t, f, pauseRegistration(f))
			id := strings.TrimPrefix(path, "/api/projects/"+f.project+"/harness-sessions/")
			plan := map[string]any{"control_id": control, "handover_point": "After the current commit"}
			wantState := "pending"
			if planned {
				expect(t, f.call(f.agent, "POST", path+"/pause-plan", plan, lease), 200)
				wantState = "claimed"
			}
			var other string
			f.tx(t, f.person, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `INSERT INTO harness_controls(tenant_id,session_id,kind,sequence,requested_by_principal_id) VALUES($1,$2,'interrupt',2,$3) RETURNING id::text`, f.person.TenantID, id, f.person.ID).Scan(&other)
			})
			age(t, f, id, "16 minutes", false)
			if n := sweep(t, f); n != 1 {
				t.Fatalf("sweep closed %d sessions", n)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				for _, check := range []struct{ id, state string }{{control, wantState}, {other, "completed"}} {
					var state string
					if err := tx.QueryRow(t.Context(), `SELECT state FROM harness_controls WHERE id=$1`, check.id).Scan(&state); err != nil {
						return err
					}
					if state != check.state {
						t.Errorf("control %s state %s, want %s", check.id, state, check.state)
					}
				}
				return nil
			})
			w := f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, lease)
			expect(t, w, 200)
			if got := decode(t, w); got["stopped_at"] != nil || got["pause"].(map[string]any)["control_id"] != control {
				t.Fatalf("pause lost on revive: %v", got)
			}
			expect(t, f.call(f.agent, "POST", path+"/pause-plan", plan, lease), 200)
			w = f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "paused", "handover": pauseHandover()}, lease)
			expect(t, w, 200)
			if got := decode(t, w); got["stop_reason"] != "paused" || got["pause"].(map[string]any)["state"] != "paused" {
				t.Fatalf("revived pause did not finish: %v", got)
			}
		})
	}
}

func TestLegacyPauseDeadlineCancelsWithoutStoppingAndAllowsNewControl(t *testing.T) {
	for _, tc := range []struct {
		trigger          string
		planned, managed bool
	}{
		{"heartbeat", false, false}, {"heartbeat", true, true},
		{"sweep", false, true}, {"request", true, false},
		{"batch", true, true},
		{"control", false, false}, {"status", true, false},
	} {
		t.Run(fmt.Sprintf("%s/planned=%t/managed=%t", tc.trigger, tc.planned, tc.managed), func(t *testing.T) {
			f := fixture(t)
			body := pauseRegistration(f)
			if tc.managed {
				body["management_mode"], body["advertised_capabilities"] = "managed", []string{"stop", "inbox"}
			}
			path, lease, control := pauseSession(t, f, body)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET pause_record=pause_record-'level' WHERE id=$1`, path[strings.LastIndex(path, "/")+1:])
				return err
			})
			id := strings.TrimPrefix(path, "/api/projects/"+f.project+"/harness-sessions/")
			plan := map[string]any{"control_id": control, "handover_point": "After the current step"}
			if tc.planned {
				expect(t, f.call(f.agent, "POST", path+"/pause-plan", plan, lease), 200)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET pause_record=jsonb_set(pause_record,'{deadline_at}',to_jsonb(clock_timestamp()-interval '1 second')) WHERE id=$1`, id)
				return err
			})
			// Planning/replaying or stopping after the DB deadline cannot win
			// before the next sweep. Error transactions leave no partial writes.
			expect(t, f.call(f.agent, "POST", path+"/pause-plan", plan, lease), 409)
			expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "paused", "handover": pauseHandover()}, lease), 409)
			var got map[string]any
			switch tc.trigger {
			case "heartbeat":
				w := f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, lease)
				expect(t, w, 200)
				got = decode(t, w)
			case "sweep":
				if n := sweep(t, f); n != 0 {
					t.Fatalf("deadline expiry stopped %d sessions", n)
				}
			case "control":
				expect(t, f.call(f.person, "GET", path+"/controls/"+control, nil, ""), 200)
			case "status":
				w := f.call(f.person, "GET", path, nil, "")
				expect(t, w, 200)
				got = decode(t, w)
			case "request":
				w := f.call(f.person, "POST", path+"/pause", map[string]any{}, "")
				expect(t, w, 200)
				got = decode(t, w)
			case "batch":
				w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions/pause", map[string]any{}, "")
				expect(t, w, 200)
				items := decode(t, w)["items"].([]any)
				if len(items) != 1 {
					t.Fatalf("expired pause not included in batch: %v", items)
				}
				got = items[0].(map[string]any)
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var state, reason, pauseState string
				var running, leaseIntact bool
				var cancellations, signals int
				if err := tx.QueryRow(t.Context(), `SELECT c.state,c.reason,s.pause_record->>'state',s.stopped_at IS NULL AND s.archived_at IS NULL,s.lease_digest IS NOT NULL FROM harness_controls c JOIN harness_sessions s ON s.id=c.session_id WHERE c.id=$1`, control).Scan(&state, &reason, &pauseState, &running, &leaseIntact); err != nil {
					return err
				}
				wantPause := "cancelled"
				if tc.trigger == "request" || tc.trigger == "batch" {
					wantPause = "requested"
				}
				if state != "completed" || reason != "pause_deadline_expired" || pauseState != wantPause || !running || !leaseIntact {
					t.Errorf("expiry state=%s reason=%s pause=%s running=%t lease=%t", state, reason, pauseState, running, leaseIntact)
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='harness.pause_cancelled'),count(*) FILTER(WHERE type='harness.stopped') FROM events WHERE after->>'id'=$1`, id).Scan(&cancellations, &signals); err != nil {
					return err
				}
				if cancellations != 1 || signals != 0 {
					t.Errorf("cancellation events=%d stopped events=%d", cancellations, signals)
				}
				return nil
			})
			if got != nil && tc.trigger != "request" && tc.trigger != "batch" && got["pause"].(map[string]any)["state"] != "cancelled" {
				t.Fatalf("response still offers an expired pause: %v", got)
			}
			w := f.call(f.person, "POST", path+"/pause", map[string]any{}, "")
			expect(t, w, 200)
			fresh := decode(t, w)["pause"].(map[string]any)["control_id"].(string)
			if fresh == control {
				t.Fatal("new pause reused the expired control")
			}
			finishPaused(t, f, path, lease, fresh)
		})
	}
}

func TestPauseRejectsManagedSessionsWithoutInboxDelivery(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	for _, harnessName := range []string{"claude", "codex", "pi", "cursor", "grok"} {
		body := pauseRegistration(f)
		body["harness"], body["management_mode"], body["advertised_capabilities"] = harnessName, "managed", []string{"stop"}
		w := f.call(f.person, "POST", base, body, "")
		expect(t, w, 201)
		id := decode(t, w)["id"].(string)
		w = f.call(f.person, "POST", base+"/"+id+"/pause", map[string]any{}, "")
		expect(t, w, 409)
		if !strings.Contains(w.Body.String(), "inbox delivery") {
			t.Fatalf("%s: %s", harnessName, w.Body)
		}
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var controls int
			var noPause bool
			if err := tx.QueryRow(t.Context(), `SELECT pause_record IS NULL,(SELECT count(*) FROM harness_controls WHERE session_id=$1) FROM harness_sessions WHERE id=$1`, id).Scan(&noPause, &controls); err != nil {
				return err
			}
			if !noPause || controls != 0 {
				t.Errorf("rejected %s pause left state/control", harnessName)
			}
			return nil
		})
	}
	for _, harnessName := range []string{"claude", "codex", "pi"} {
		body := pauseRegistration(f)
		body["harness"], body["management_mode"], body["advertised_capabilities"] = harnessName, "managed", []string{"stop", "inbox"}
		pauseSession(t, f, body)
	}
	for _, harnessName := range []string{"cursor", "grok"} {
		body := pauseRegistration(f)
		body["harness"] = harnessName
		path, lease, control := pauseSession(t, f, body)
		w := f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, lease)
		expect(t, w, 200)
		if decode(t, w)["pause"].(map[string]any)["control_id"] != control {
			t.Fatalf("unmanaged %s did not receive pause", harnessName)
		}
	}
}

func TestPauseAllSkipsManagedSessionsWithoutInbox(t *testing.T) {
	for _, scope := range []string{"project", "person"} {
		t.Run(scope, func(t *testing.T) {
			f := fixture(t)
			base := "/api/projects/" + f.project + "/harness-sessions"
			bodies := []map[string]any{pauseRegistration(f), pauseRegistration(f)}
			bodies[0]["management_mode"], bodies[0]["advertised_capabilities"] = "managed", []string{"stop", "inbox"}
			bodies[0]["display_label"] = "Inbox batch worker"
			bodies[1]["harness"], bodies[1]["management_mode"], bodies[1]["advertised_capabilities"] = "cursor", "managed", []string{"stop"}
			bodies[1]["display_label"] = "Cursor batch worker"
			ids := []string{}
			for _, body := range bodies {
				w := f.call(f.person, "POST", base, body, "")
				expect(t, w, 201)
				id := decode(t, w)["id"].(string)
				ids = append(ids, id)
				f.beat(t, id, body["worker_lease"].(string), 1, nil)
			}
			path := base + "/pause"
			if scope == "person" {
				path = "/api/harness-sessions/pause"
			}
			w := f.call(f.person, "POST", path, map[string]any{}, "")
			expect(t, w, 200)
			result := decode(t, w)
			items, skipped := result["items"].([]any), result["skipped"].([]any)
			if len(items) != 1 || items[0].(map[string]any)["id"] != ids[0] || result["more"] != false || result["skipped_more"] != false {
				t.Fatalf("capable pause missing or batch incomplete: %v", result)
			}
			if len(skipped) != 1 {
				t.Fatalf("skipped sessions: %v", skipped)
			}
			if s := skipped[0].(map[string]any); len(s) != 4 || s["id"] != ids[1] || s["project_id"] != f.project || s["display_label"] != "Cursor batch worker" || s["reason"] != "inbox_delivery_unavailable" {
				t.Fatalf("skip report must contain only public identity and reason: %v", s)
			}
			for _, body := range bodies {
				if strings.Contains(w.Body.String(), body["worker_lease"].(string)) || strings.Contains(w.Body.String(), body["harness_session_ref"].(string)) {
					t.Fatal("pause batch exposed private proofs")
				}
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				for i, id := range ids {
					var state, phase string
					var running bool
					var controls, pauseEvents int
					if err := tx.QueryRow(t.Context(), `SELECT coalesce(pause_record->>'state',''),phase,stopped_at IS NULL,
 (SELECT count(*) FROM harness_controls WHERE session_id=$1),
 (SELECT count(*) FROM events WHERE type='harness.pause_requested' AND after->>'id'=$1::text)
 FROM harness_sessions WHERE id=$1`, id).Scan(&state, &phase, &running, &controls, &pauseEvents); err != nil {
						return err
					}
					wantState, wantControls := "requested", 1
					if i == 1 {
						wantState, wantControls = "", 0
					}
					if state != wantState || controls != wantControls || pauseEvents != wantControls || phase != "working" || !running {
						t.Errorf("session %s: state=%s controls=%d events=%d phase=%s running=%t", id, state, controls, pauseEvents, phase, running)
					}
				}
				return nil
			})
			w = f.call(f.person, "POST", path, map[string]any{}, "")
			expect(t, w, 200)
			if replay := decode(t, w); len(replay["items"].([]any)) != 0 || len(replay["skipped"].([]any)) != 1 || replay["more"] != false {
				t.Fatalf("batch replay stalled or duplicated a pause: %v", replay)
			}
			w = f.call(f.person, "POST", path, map[string]any{"except": []string{ids[1]}}, "")
			expect(t, w, 200)
			if len(decode(t, w)["skipped"].([]any)) != 0 {
				t.Fatal("excluded session was reported as skipped")
			}
			control := items[0].(map[string]any)["pause"].(map[string]any)["control_id"].(string)
			finishPaused(t, f, base+"/"+ids[0], bodies[0]["worker_lease"].(string), control)
			if got := f.beat(t, ids[1], bodies[1]["worker_lease"].(string), 2, nil); got["pause"] != nil || got["stopped_at"] != nil || got["phase"] != "working" {
				t.Fatal("skipped Cursor session did not remain running")
			}
		})
	}
}

func TestPauseAllSkipReportsAreBoundedWithoutStarvingOtherProjects(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	body := pauseRegistration(f)
	body["harness"], body["management_mode"], body["advertised_capabilities"] = "cursor", "managed", []string{"stop"}
	w := f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,capabilities,ref_digest,lease_digest,owner_principal_id)
 SELECT tenant_id,project_id,agent_principal_id,harness,host,management,role,capabilities,
 sha256(convert_to('skip-ref-'||n::text,'UTF8')),sha256(convert_to('skip-lease-'||n::text,'UTF8')),owner_principal_id
 FROM harness_sessions CROSS JOIN generate_series(1,200) n WHERE id=$1`, id)
		return err
	})
	other := uid()
	f.addNode(t, other, "PAUSE-3", "project", "", "Other project")
	for _, projectID := range []string{f.project, other} {
		capable := pauseRegistration(f)
		delete(capable, "ticket_node_id")
		delete(capable, "work_shape")
		w = f.call(f.person, "POST", "/api/projects/"+projectID+"/harness-sessions", capable, "")
		expect(t, w, 201)
	}
	w = f.call(f.person, "POST", base+"/pause", map[string]any{}, "")
	expect(t, w, 200)
	if got := decode(t, w); len(got["items"].([]any)) != 1 || len(got["skipped"].([]any)) != 200 || got["skipped_more"] != true || got["more"] != false {
		t.Fatalf("project skip bound hid a capable session: %v", got)
	}
	w = f.call(f.person, "POST", "/api/harness-sessions/pause", map[string]any{}, "")
	expect(t, w, 200)
	if got := decode(t, w); len(got["items"].([]any)) != 1 || len(got["skipped"].([]any)) != 200 || got["skipped_more"] != true || got["more"] != false {
		t.Fatalf("person skip bound hid another project's capable session: %v", got)
	}
}

func TestPauseStateMachineSurvivesRestartAndResumesWorker(t *testing.T) {
	f := fixture(t)
	body := pauseRegistration(f)
	path, lease, control := pauseSession(t, f, body)
	// Restart the module: neither pending delivery nor handover depends on a relay.
	f.mux = http.NewServeMux()
	harness.New(f.db.App).Mount(f.mux)
	beat := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}
	w := f.call(f.agent, "POST", path+"/heartbeat", beat, lease)
	expect(t, w, 200)
	if p := decode(t, w)["pause"].(map[string]any); p["control_id"] != control || p["state"] != "requested" {
		t.Fatal(p)
	}
	// Stop is fenced by planning and an explicit worktree disposition.
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "paused", "handover": pauseHandover()}, lease), 409)
	expect(t, f.call(f.agent, "POST", path+"/pause-plan", map[string]any{"control_id": control, "handover_point": "Ready after the current step"}, "wrong-generation-lease-00000000000000"), 403)
	finishPaused(t, f, path, lease, control)
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	paused := decode(t, w)
	if paused["stop_reason"] != "paused" || paused["finished"] != false || paused["pause"].(map[string]any)["state"] != "paused" {
		t.Fatal(paused)
	}
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "paused", "handover": pauseHandover()}, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 403)
	expect(t, f.call(f.person, "POST", path+"/pause", map[string]any{}, ""), 409)
	base := "/api/projects/" + f.project + "/harness-sessions"
	body["succeeds_session_id"] = paused["id"]
	body["harness_session_ref"], body["worker_lease"] = "next-ref-"+uid(), "next-lease-"+uid()
	expect(t, f.call(f.person, "POST", base, body, ""), 409) // resume permission has not been requested
	w = f.call(f.person, "POST", path+"/resume", map[string]any{}, "")
	expect(t, w, 200)
	result := decode(t, w)
	for _, field := range []string{"generator", "command"} {
		if _, exists := result["registration"].(map[string]any)[field]; exists {
			t.Fatalf("codex recipe includes absent %s label", field)
		}
	}
	if c := result["continuation"].(map[string]any); !strings.Contains(c["brief"].(string), "remaining checks") {
		t.Fatal(c)
	}
	f.mux = http.NewServeMux()
	harness.New(f.db.App).Mount(f.mux)
	w = f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	next := decode(t, w)
	if next["id"] == paused["id"] || next["continuation"].(map[string]any)["succeeds_session_id"] != paused["id"] || next["branch"] != "work/owned" {
		t.Fatal(next)
	}
	w = f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	if decode(t, w)["id"] != next["id"] {
		t.Fatal("successor replay created a second generation")
	}
	w = f.call(f.person, "GET", path, nil, "")
	expect(t, w, 200)
	if p := decode(t, w)["pause"].(map[string]any); p["state"] != "resumed" || p["successor_session_id"] != next["id"] {
		t.Fatal(p)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var notes, requests, resumes int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='comment.created' AND node_id=$1),count(*) FILTER(WHERE type='harness.pause_requested'),count(*) FILTER(WHERE type='harness.resumed') FROM events`, f.ticket).Scan(&notes, &requests, &resumes)
		if err == nil && (notes != 1 || requests != 1 || resumes != 1) {
			t.Errorf("audit notes=%d requests=%d resumes=%d", notes, requests, resumes)
		}
		return err
	})
}

func TestPauseProcessRunContinuationRetainsKindLabels(t *testing.T) {
	for _, tc := range []struct{ kind, field, label, mismatch string }{
		{"media", "generator", "higgsfield/kling3_0", "higgsfield/veo3_1"},
		{"terminal", "command", "ffmpeg", "make render"},
	} {
		for _, mode := range []string{"atomic", "registration"} {
			t.Run(tc.kind+"/"+mode, func(t *testing.T) {
				f := fixture(t)
				base := "/api/projects/" + f.project + "/harness-sessions"
				parent := f.registerSession(t, f.agent.ID, "coordinator", f.ticket, "parent-ref-"+uid(), "parent-lease-"+uid())
				body := pauseRegistration(f)
				delete(body, "model")
				delete(body, "reasoning_effort")
				body["harness"], body[tc.field], body["parent_harness_session_id"] = tc.kind, tc.label, parent
				path, lease, control := pauseSession(t, f, body)
				finishPaused(t, f, path, lease, control)
				id := strings.TrimPrefix(path, base+"/")
				proof := map[string]any{"harness_session_ref": "continued-ref-" + uid(), "worker_lease": "continued-lease-" + uid()}
				assertRecipe := func(result map[string]any) {
					t.Helper()
					registration := result["registration"].(map[string]any)
					if registration[tc.field] != tc.label {
						t.Fatal("resume recipe lost the run-kind label")
					}
					for _, field := range []string{"generator", "command"} {
						if _, exists := registration[field]; field != tc.field && exists {
							t.Fatalf("recipe includes absent %s label", field)
						}
					}
				}

				assertNoSuccessor := func(wantState string) {
					t.Helper()
					f.tx(t, f.person, func(tx pgx.Tx) error {
						var state string
						var noSuccessor bool
						var activeChildren, resumes int
						if err := tx.QueryRow(t.Context(), `SELECT pause_record->>'state',handed_over_to_id IS NULL,
 (SELECT count(*) FROM harness_sessions WHERE parent_id=$2::uuid AND stopped_at IS NULL),
 (SELECT count(*) FROM events WHERE type='harness.resumed' AND after->>'id'=$1::text)
 FROM harness_sessions WHERE id=$1::uuid`, id, parent).Scan(&state, &noSuccessor, &activeChildren, &resumes); err != nil {
							return err
						}
						if state != wantState || !noSuccessor || activeChildren != 0 || resumes != 0 {
							t.Errorf("failed continuation changed state: pause=%s unlinked=%t active children=%d resumes=%d", state, noSuccessor, activeChildren, resumes)
						}
						return nil
					})
				}

				var next map[string]any
				if mode == "atomic" {
					// A failed registration must roll back the resume request too.
					expect(t, f.call(f.person, "POST", path+"/resume", map[string]any{"registration": map[string]any{"harness_session_ref": "short", "worker_lease": proof["worker_lease"]}}, ""), 400)
					assertNoSuccessor("paused")
					w := f.call(f.person, "POST", path+"/resume", map[string]any{"registration": proof}, "")
					expect(t, w, 200)
					result := decode(t, w)
					assertRecipe(result)
					next = result["successor"].(map[string]any)
					expect(t, f.call(f.person, "POST", path+"/resume", map[string]any{"registration": proof}, ""), 200)
					if strings.Contains(w.Body.String(), proof["worker_lease"].(string)) || strings.Contains(w.Body.String(), proof["harness_session_ref"].(string)) {
						t.Fatal("atomic resume exposed private proofs")
					}
				} else {
					w := f.call(f.person, "POST", path+"/resume", map[string]any{}, "")
					expect(t, w, 200)
					assertRecipe(decode(t, w))
					// Optional metadata and labels must inherit from the predecessor.
					registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": tc.kind, "host": "test", "role": "worker", "management_mode": "unmanaged", "succeeds_session_id": id, "harness_session_ref": proof["harness_session_ref"], "worker_lease": proof["worker_lease"], tc.field: tc.mismatch}
					w = f.call(f.person, "POST", base, registration, "")
					expect(t, w, 409)
					if !strings.Contains(w.Body.String(), "continuation must retain") {
						t.Fatal("mismatch was not rejected by the predecessor check")
					}
					assertNoSuccessor("resume_requested")
					delete(registration, tc.field)
					w = f.call(f.person, "POST", base, registration, "")
					expect(t, w, 201)
					next = decode(t, w)
					w = f.call(f.person, "POST", base, registration, "")
					expect(t, w, 201)
					if decode(t, w)["id"] != next["id"] {
						t.Fatal("registration replay created another successor")
					}
				}
				if next["id"] == id || next["harness"] != tc.kind || next[tc.field] != tc.label || next["parent_harness_session_id"] != parent || next["ticket_node_id"] != f.ticket || next["branch"] != body["branch"] || next["worktree"] != body["worktree"] {
					t.Fatal("continuation lost its run-kind label or binding")
				}
				if c := next["continuation"].(map[string]any); c["succeeds_session_id"] != id || !strings.Contains(c["brief"].(string), "remaining checks") {
					t.Fatal("continuation lost its handover")
				}
				w := f.call(f.person, "GET", base+"/"+next["id"].(string), nil, "")
				expect(t, w, 200)
				if decode(t, w)[tc.field] != tc.label {
					t.Fatal("successor run-kind label was not persisted")
				}
				w = f.call(f.person, "GET", path, nil, "")
				expect(t, w, 200)
				if p := decode(t, w)["pause"].(map[string]any); p["state"] != "resumed" || p["successor_session_id"] != next["id"] {
					t.Fatal("predecessor did not durably link its successor")
				}
				f.tx(t, f.person, func(tx pgx.Tx) error {
					var requests, resumes int
					err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='harness.resume_requested'),count(*) FILTER(WHERE type='harness.resumed') FROM events WHERE after->>'id'=$1`, id).Scan(&requests, &resumes)
					if err == nil && (requests != 1 || resumes != 1) {
						t.Errorf("duplicate or missing resume audit: requests=%d resumes=%d", requests, resumes)
					}
					return err
				})
			})
		}
	}
}

func TestPauseRequestsConcurrentAndManagedYieldKeepsLegacyStop(t *testing.T) {
	f := fixture(t)
	body := pauseRegistration(f)
	body["management_mode"] = "managed"
	body["advertised_capabilities"] = []string{"stop", "inbox"}
	path, lease, control := pauseSession(t, f, body)
	results := make(chan string, 2)
	for range 2 {
		go func() {
			w := f.call(f.person, "POST", path+"/pause", map[string]any{"reason": "Same intent"}, "")
			results <- w.Body.String()
		}()
	}
	for range 2 {
		if raw := <-results; !strings.Contains(raw, control) {
			t.Fatal(raw)
		}
	}
	// A cooperative pause cannot be accidentally treated as an immediate stop.
	w := f.call(f.person, "POST", path+"/controls/stop", map[string]any{}, "")
	expect(t, w, 201)
	stopID := decode(t, w)["id"]
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	controls := decode(t, w)["controls"].([]any)
	if len(controls) != 1 || controls[0].(map[string]any)["id"] != stopID {
		t.Fatal(controls)
	}
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "paused"}, lease), 409)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_controls WHERE session_id=$1 AND request_payload->>'pause'='true'`, strings.TrimPrefix(path, "/api/projects/"+f.project+"/harness-sessions/")).Scan(&count)
		if err == nil && count != 1 {
			t.Error("duplicate pause controls")
		}
		return err
	})
}

func TestPauseAuthorizationOwnerScopeAndProvenCoordinator(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	parentBody := pauseRegistration(f)
	parentBody["role"] = "coordinator"
	w := f.call(f.person, "POST", base, parentBody, "")
	expect(t, w, 201)
	parent := decode(t, w)["id"]
	childBody := pauseRegistration(f)
	childBody["parent_harness_session_id"] = parent
	w = f.call(f.person, "POST", base, childBody, "")
	expect(t, w, 201)
	path := base + "/" + decode(t, w)["id"].(string)
	other := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Other')`, other.TenantID, other.ID)
		return err
	})
	dbtest.BindRole(t, f.db, other.TenantID, other.ID, "member")
	expect(t, f.call(other, "POST", path+"/pause", map[string]any{}, ""), 403)
	expect(t, f.call(f.foreign, "POST", path+"/pause", map[string]any{}, ""), 403)
	f.agent.Scopes = append(f.agent.Scopes, "harness.control")
	dbtest.BindRole(t, f.db, f.agent.TenantID, f.agent.ID, "admin")
	expect(t, f.call(f.agent, "POST", path+"/pause", map[string]any{}, ""), 403)
	expect(t, f.call(f.agent, "POST", path+"/pause", map[string]any{"coordinator_session_id": parent}, childBody["worker_lease"].(string)), 403)
	w = f.call(f.agent, "POST", path+"/pause", map[string]any{"coordinator_session_id": parent}, parentBody["worker_lease"].(string))
	expect(t, w, 200)
	// A same-principal sibling is outside the proven coordinator's children.
	siblingBody := pauseRegistration(f)
	w = f.call(f.person, "POST", base, siblingBody, "")
	expect(t, w, 201)
	sibling := base + "/" + decode(t, w)["id"].(string)
	expect(t, f.call(f.agent, "POST", sibling+"/pause", map[string]any{"coordinator_session_id": parent}, parentBody["worker_lease"].(string)), 403)
	// Skip reporting uses the same proven-child boundary as pause writes,
	// even when the agent happens to have an admin role.
	var unsupportedChild string
	for _, child := range []bool{true, false} {
		body := pauseRegistration(f)
		body["harness"], body["management_mode"], body["advertised_capabilities"] = "cursor", "managed", []string{"stop"}
		if child {
			body["parent_harness_session_id"] = parent
		}
		w = f.call(f.person, "POST", base, body, "")
		expect(t, w, 201)
		if child {
			unsupportedChild = decode(t, w)["id"].(string)
		}
	}
	w = f.call(f.agent, "POST", base+"/pause", map[string]any{"coordinator_session_id": parent}, parentBody["worker_lease"].(string))
	expect(t, w, 200)
	if got := decode(t, w); len(got["items"].([]any)) != 0 || len(got["skipped"].([]any)) != 1 || got["skipped"].([]any)[0].(map[string]any)["id"] != unsupportedChild {
		t.Fatal("coordinator batch reported an unrelated session")
	}
	f.agent.Scopes = []string{"harness.worker"}
	expect(t, f.call(f.agent, "POST", path+"/pause", map[string]any{"coordinator_session_id": parent}, parentBody["worker_lease"].(string)), 403)
	expect(t, f.call(f.agent, "POST", base+"/pause", map[string]any{"coordinator_session_id": parent}, parentBody["worker_lease"].(string)), 403)
}

func TestPauseBatchExclusionsAndResumeRegistration(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	var ids, leases []string
	for range 3 {
		body := pauseRegistration(f)
		w := f.call(f.person, "POST", base, body, "")
		expect(t, w, 201)
		ids = append(ids, decode(t, w)["id"].(string))
		leases = append(leases, body["worker_lease"].(string))
	}
	w := f.call(f.person, "POST", base+"/pause", map[string]any{"except": []string{ids[2]}}, "")
	expect(t, w, 200)
	items := decode(t, w)["items"].([]any)
	if len(items) != 2 {
		t.Fatal(items)
	}
	for _, item := range items {
		s := item.(map[string]any)
		id := s["id"].(string)
		lease := leases[0]
		if id == ids[1] {
			lease = leases[1]
		}
		finishPaused(t, f, base+"/"+id, lease, s["pause"].(map[string]any)["control_id"].(string))
	}
	w = f.call(f.person, "POST", base+"/resume", map[string]any{}, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 2 {
		t.Fatal(w.Body.String())
	}
	w = f.call(f.person, "POST", base+"/resume", map[string]any{}, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 0 {
		t.Fatal("resume-all replay queued duplicates")
	}
	proof := map[string]any{"harness_session_ref": "continued-ref-" + uid(), "worker_lease": "continued-lease-" + uid()}
	w = f.call(f.person, "POST", base+"/"+ids[0]+"/resume", map[string]any{"registration": proof}, "")
	expect(t, w, 200)
	result := decode(t, w)
	if result["successor"].(map[string]any)["continuation"] == nil {
		t.Fatal(result)
	}
	if strings.Contains(w.Body.String(), proof["worker_lease"].(string)) || strings.Contains(w.Body.String(), proof["harness_session_ref"].(string)) {
		t.Fatal("resume exposed private proofs")
	}
	w = f.call(f.person, "POST", base+"/"+ids[0]+"/resume", map[string]any{"registration": proof}, "")
	expect(t, w, 200)
	if decode(t, w)["successor"].(map[string]any)["id"] != result["successor"].(map[string]any)["id"] {
		t.Fatal("resume replay duplicated successor")
	}
}

func TestPauseAllAcrossProjectsAndLongPausedHistory(t *testing.T) {
	f := fixture(t)
	otherProject := uid()
	f.addNode(t, otherProject, "PAUSE-3", "project", "", "Other project")
	for _, project := range []string{f.project, otherProject} {
		body := pauseRegistration(f)
		delete(body, "ticket_node_id")
		delete(body, "work_shape")
		w := f.call(f.person, "POST", "/api/projects/"+project+"/harness-sessions", body, "")
		expect(t, w, 201)
	}
	w := f.call(f.person, "POST", "/api/harness-sessions/pause", map[string]any{}, "")
	expect(t, w, 200)
	items := decode(t, w)["items"].([]any)
	if len(items) != 2 {
		t.Fatal(items)
	}
	// Close with the stored proof from the fixture rows, then backdate the
	// closed generation to exercise the current-view retention policy.
	var first string
	for _, item := range items {
		s := item.(map[string]any)
		if s["project_id"] == f.project {
			first = s["id"].(string)
		}
	}
	// Use an independently registered paused generation with known lease.
	path, lease, control := pauseSession(t, f, pauseRegistration(f))
	finishPaused(t, f, path, lease, control)
	id := strings.TrimPrefix(path, "/api/projects/"+f.project+"/harness-sessions/")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp()-interval '3 days' WHERE id=$1`, id)
		return err
	})
	w = f.call(f.person, "GET", "/api/harness-sessions?view=current&state=paused", nil, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), id) || strings.Contains(w.Body.String(), first) {
		t.Fatal("paused history disappeared or a running request entered the paused filter")
	}
	w = f.call(f.person, "POST", "/api/harness-sessions/resume", map[string]any{}, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 1 {
		t.Fatal(w.Body.String())
	}
	if f.call(f.agent, "POST", "/api/harness-sessions/pause", map[string]any{}, "").Code != 403 {
		t.Fatal("agent got global control")
	}
}

func TestPausedCoordinatorAdoptsPausedChildrenForContinuation(t *testing.T) {
	f := fixture(t)
	parentBody := pauseRegistration(f)
	parentBody["role"] = "coordinator"
	parentPath, parentLease, parentControl := pauseSession(t, f, parentBody)
	parentID := strings.TrimPrefix(parentPath, "/api/projects/"+f.project+"/harness-sessions/")
	childBody := pauseRegistration(f)
	childBody["parent_harness_session_id"] = parentID
	childPath, childLease, childControl := pauseSession(t, f, childBody)
	finishPaused(t, f, childPath, childLease, childControl)
	finishPaused(t, f, parentPath, parentLease, parentControl)
	newProof := func() map[string]any {
		return map[string]any{"registration": map[string]any{"harness_session_ref": "next-ref-" + uid(), "worker_lease": "next-lease-" + uid()}}
	}
	w := f.call(f.person, "POST", parentPath+"/resume", newProof(), "")
	expect(t, w, 200)
	parent := decode(t, w)["successor"].(map[string]any)
	w = f.call(f.person, "POST", childPath+"/resume", newProof(), "")
	expect(t, w, 200)
	child := decode(t, w)["successor"].(map[string]any)
	if child["parent_harness_session_id"] != parent["id"] {
		t.Fatal("continuation retained a stopped coordinator")
	}
}
