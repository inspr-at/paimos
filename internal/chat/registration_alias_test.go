// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
)

func (f *fixture) stopNative(t *testing.T, id string) {
	t.Helper()
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) nativeClaims(t *testing.T, thread Thread, want int) {
	t.Helper()
	var count int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM chat_native_contexts n JOIN chat_roles r ON r.tenant_id=n.tenant_id AND r.id=n.role_id AND r.owner_person_id=n.owner_person_id AND r.project_id=n.project_id WHERE r.id=$1`, thread.Role.ID).Scan(&count); err != nil || count != want {
		t.Fatalf("historical ownership claims=%d want=%d err=%v", count, want, err)
	}
}

func TestRegistrationAliasInheritsUnboundHistory(t *testing.T) {
	for _, source := range []string{"ref", "vendor"} {
		for _, scenario := range []string{"person", "role", "project"} {
			t.Run(source+"/"+scenario, func(t *testing.T) {
				f := newFixture(t)
				thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
				ref, alias := "creation-history-"+uid(), "creation-alias-"+uid()
				first, _ := f.registerNative(t, f.alice, f.project, ref, "")
				bound := f.bind(t, f.alice, thread, first, "0")
				f.stopNative(t, first)
				nextRef, nextVendor := ref, alias
				if source == "vendor" {
					nextRef, nextVendor = alias, ref
				}
				next, _ := f.registerNative(t, f.alice, f.project, nextRef, nextVendor)
				// This replacement is deliberately never bound. Its initial alias
				// must nevertheless inherit the first generation's ownership.
				f.nativeClaims(t, thread, 2)
				var boundReplacement bool
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM chat_session_contexts WHERE session_id=$1)`, next).Scan(&boundReplacement); err != nil || boundReplacement {
					t.Fatalf("replacement was unexpectedly bound: %v %v", boundReplacement, err)
				}
				f.stopNative(t, next)
				owner, project := f.alice, f.project
				if scenario == "person" {
					owner = f.bob
				} else if scenario == "project" {
					project = f.secondProject
				}
				target := f.thread(t, owner, f.role(t, owner, project, "worker", "other"))
				if scenario == "role" {
					wrong, _ := f.registerNative(t, owner, project, alias, "")
					w := f.call(owner, "POST", "/api/chat-threads/"+target.ID+"/binding", map[string]string{"session_id": wrong, "expected_epoch": "0"}, "")
					expect(t, w, http.StatusNotFound)
					if !strings.Contains(w.Body.String(), "chat binding unavailable") {
						t.Fatal("wrong-role binding failed for the wrong reason")
					}
					f.stopNative(t, wrong)
				} else {
					w := f.replayNative(owner, project, alias, "creation-conflict-lease-"+uid(), "")
					expect(t, w, http.StatusConflict)
					if !strings.Contains(w.Body.String(), "chat binding unavailable") {
						t.Fatal("registration failed for the wrong reason")
					}
				}
				f.nativeClaims(t, target, 0)
				continued, lease := f.registerNative(t, f.alice, f.project, alias, "")
				resumed := f.bind(t, f.alice, bound, continued, "1")
				if resumed.ID != thread.ID || resumed.BindingEpoch != "2" {
					t.Fatal("legitimate alias continuation lost its conversation")
				}
				decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{thread.ID, continued, "2"}, lease))
				f.nativeClaims(t, thread, 2)
			})
		}
	}
}

func TestRegistrationAliasRejectsConflictingHistoryAtomically(t *testing.T) {
	for _, source := range []string{"ref", "vendor"} {
		for _, scenario := range []string{"person", "role", "project", "owner", "ownerless"} {
			t.Run(source+"/"+scenario, func(t *testing.T) {
				f := newFixture(t)
				thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
				ref, alias := "conflict-history-"+uid(), "conflict-alias-"+uid()
				first, _ := f.registerNative(t, f.alice, f.project, ref, "")
				f.bind(t, f.alice, thread, first, "0")
				f.stopNative(t, first)
				owner, project := f.alice, f.project
				if scenario == "person" {
					owner = f.bob
				} else if scenario == "project" {
					project = f.secondProject
				}
				if scenario != "owner" && scenario != "ownerless" {
					other := f.thread(t, owner, f.role(t, owner, project, "worker", "other"))
					second, _ := f.registerNative(t, owner, project, alias, "")
					f.bind(t, owner, other, second, "0")
					f.stopNative(t, second)
				}
				actor := f.alice
				if scenario == "owner" {
					actor = f.bob
				} else if scenario == "ownerless" {
					f.agent.Scopes = append(f.agent.Scopes, "harness.write")
					if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes); err != nil {
						t.Fatal(err)
					}
					actor = f.agent
				}
				if source == "vendor" {
					ref, alias = alias, ref
				}
				var sessions, events, claims, aliases, afterSessions, afterEvents, afterClaims, afterAliases int
				const counts = `SELECT (SELECT count(*) FROM harness_sessions),(SELECT count(*) FROM events),(SELECT count(*) FROM chat_native_contexts),(SELECT count(*) FROM chat_native_aliases)`
				if err := f.d.Admin.QueryRow(t.Context(), counts).Scan(&sessions, &events, &claims, &aliases); err != nil {
					t.Fatal(err)
				}
				w := f.replayNative(actor, f.project, ref, "conflict-creation-lease-"+uid(), alias)
				expect(t, w, http.StatusConflict)
				if !strings.Contains(w.Body.String(), "chat binding unavailable") {
					t.Fatal("creation rejected for a reason other than historical ownership")
				}
				if err := f.d.Admin.QueryRow(t.Context(), counts).Scan(&afterSessions, &afterEvents, &afterClaims, &afterAliases); err != nil || sessions != afterSessions || events != afterEvents || claims != afterClaims || aliases != afterAliases {
					t.Fatalf("failed creation left partial state: sessions=%d/%d events=%d/%d claims=%d/%d aliases=%d/%d err=%v", sessions, afterSessions, events, afterEvents, claims, afterClaims, aliases, afterAliases, err)
				}
			})
		}
	}
}

func TestRegistrationAliasUsesResumedOwner(t *testing.T) {
	for _, conflicting := range []bool{false, true} {
		t.Run(map[bool]string{false: "continuation", true: "conflict"}[conflicting], func(t *testing.T) {
			f := newFixture(t)
			thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
			ref := "resumed-history-" + uid()
			first, _ := f.registerNative(t, f.alice, f.project, ref, "")
			f.bind(t, f.alice, thread, first, "0")
			pause := harness.Pause{State: "resume_requested", Handover: &harness.Handover{State: "ready", NextSteps: []string{"continue"}}}
			raw, err := json.Marshal(pause)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped',stop_reason='paused',pause_record=$2 WHERE id=$1`, first, raw); err != nil {
				t.Fatal(err)
			}
			if conflicting {
				other := f.thread(t, f.admin, f.role(t, f.admin, f.project, "worker", "other"))
				ref = "resumed-foreign-" + uid()
				id, _ := f.registerNative(t, f.admin, f.project, ref, "")
				f.bind(t, f.admin, other, id, "0")
				f.stopNative(t, id)
			}
			alias := "resumed-alias-" + uid()
			w := f.call(f.admin, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": alias, "vendor_session_ref": ref, "worker_lease": "resume-creation-lease-" + uid(), "succeeds_session_id": first}, "")
			if conflicting {
				expect(t, w, http.StatusConflict)
				if !strings.Contains(w.Body.String(), "chat binding unavailable") {
					t.Fatal("resumed registration rejected for the wrong reason")
				}
				var intact bool
				if err = f.d.Admin.QueryRow(t.Context(), `SELECT handed_over_to_id IS NULL AND pause_record->>'state'='resume_requested' FROM harness_sessions WHERE id=$1`, first).Scan(&intact); err != nil || !intact {
					t.Fatalf("conflicting resume changed predecessor: intact=%v err=%v", intact, err)
				}
			} else {
				expect(t, w, http.StatusCreated)
				f.nativeClaims(t, thread, 2)
				var next harness.Session
				if err = json.Unmarshal(w.Body.Bytes(), &next); err != nil {
					t.Fatal(err)
				}
				f.bind(t, f.alice, thread, next.ID, "1")
			}
		})
	}
}

func TestRegistrationAliasExistingReplayPropagatesHistory(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "omitted", true: "unchanged"}[supplied], func(t *testing.T) {
			f := newFixture(t)
			thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
			ref, alias := "replay-history-"+uid(), "replay-alias-"+uid()
			// Register before either reference has ownership. Another session
			// subsequently binds the shared native reference to this role.
			pending, lease := f.registerNative(t, f.alice, f.project, alias, ref)
			first, _ := f.registerNative(t, f.alice, f.project, ref, "")
			f.bind(t, f.alice, thread, first, "0")
			// First binding must claim the earlier alias without waiting for replay.
			f.nativeClaims(t, thread, 2)
			vendor := ""
			if supplied {
				vendor = ref
			}
			expect(t, f.replayNative(f.alice, f.project, alias, lease, vendor), http.StatusCreated)
			f.nativeClaims(t, thread, 2)
			f.bind(t, f.alice, thread, pending, "1")
		})
	}
}

func TestRegistrationAliasAgentCreatorContinuation(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
	ref := "creator-history-" + uid()
	first, _ := f.registerNative(t, f.alice, f.project, ref, "")
	f.bind(t, f.alice, thread, first, "0")
	f.stopNative(t, first)
	f.agent.KeyCreatorID = f.alice.ID
	f.agent.Scopes = append(f.agent.Scopes, "harness.write")
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2,created_by_principal_id=$3 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	id, lease := f.registerNative(t, f.agent, f.project, ref, "creator-alias-"+uid())
	f.nativeClaims(t, thread, 2)
	f.bind(t, f.alice, thread, id, "1")
	decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{thread.ID, id, "2"}, lease))
}

func TestRegistrationAliasStaleCoordinatorBeforeEvents(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "continuation", true: "conflict"}[conflict], func(t *testing.T) {
			f := newFixture(t)
			thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "lead", "lead"))
			ref := "stale-history-" + uid()
			in := map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "management_mode": "unmanaged", "role": "coordinator", "harness_session_ref": ref, "worker_lease": "stale-first-lease-" + uid(), "advertised_capabilities": []string{"inbox"}}
			w := f.call(f.alice, "POST", "/api/projects/"+f.project+"/harness-sessions", in, "")
			expect(t, w, http.StatusCreated)
			var first harness.Session
			if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
				t.Fatal(err)
			}
			f.bind(t, f.alice, thread, first.ID, "0")
			if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp()-interval '1 day' WHERE id=$1`, first.ID); err != nil {
				t.Fatal(err)
			}
			message := f.replyObligation(t, first.ID)
			// At the first closure event, ownership propagation must already be
			// complete. This proves ordering without sleeps or timing thresholds.
			if _, err := f.d.Admin.Exec(t.Context(), `CREATE SEQUENCE test_registration_order_violation;
 GRANT USAGE ON SEQUENCE test_registration_order_violation TO PUBLIC;
 CREATE FUNCTION test_registration_claims_before_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.type IN ('harness.stopped','inbox.reply_obligation_closed') AND (SELECT count(*) FROM chat_native_contexts)<>2 THEN
 PERFORM nextval('test_registration_order_violation');
 RAISE EXCEPTION 'ownership missing before event'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER test_registration_claims_before_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION test_registration_claims_before_event()`); err != nil {
				t.Fatal(err)
			}
			in["worker_lease"] = "stale-next-lease-" + uid()
			in["vendor_session_ref"] = "stale-alias-" + uid()
			actor := f.alice
			if conflict {
				actor = f.bob
			}
			w = f.call(actor, "POST", "/api/projects/"+f.project+"/harness-sessions", in, "")
			// Sequence increments survive rollback: an unrelated HTTP failure
			// cannot masquerade as the event-order regression.
			var outOfOrder bool
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT is_called FROM test_registration_order_violation`).Scan(&outOfOrder); err != nil || outOfOrder {
				t.Fatalf("closure event ran before native ownership: %v %v", outOfOrder, err)
			}

			if conflict {
				expect(t, w, http.StatusConflict)
				if !strings.Contains(w.Body.String(), "chat binding unavailable") {
					t.Fatal("stale replacement rejected for the wrong reason")
				}
				var pending bool
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT closed_at IS NULL FROM inbox_reply_obligations WHERE message_id=$1`, message).Scan(&pending); err != nil || !pending {
					t.Fatalf("rejected replacement closed obligation: %v %v", pending, err)
				}

				var intact bool
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT stopped_at IS NULL AND handed_over_to_id IS NULL FROM harness_sessions WHERE id=$1`, first.ID).Scan(&intact); err != nil || !intact {
					t.Fatalf("rejected stale replacement changed predecessor: %v %v", intact, err)
				}
			} else {
				expect(t, w, http.StatusCreated)
				var closedEvents int
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='inbox.reply_obligation_closed' AND after->>'message_id'=$1`, message).Scan(&closedEvents); err != nil || closedEvents != 1 {
					t.Fatalf("obligation closure events=%d: %v", closedEvents, err)
				}
				f.nativeClaims(t, thread, 2)
				var next harness.Session
				if err := json.Unmarshal(w.Body.Bytes(), &next); err != nil {
					t.Fatal(err)
				}
				f.bind(t, f.alice, thread, next.ID, "1")
				var closed bool
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT stopped_at IS NOT NULL AND stop_reason='heartbeat_lost' AND handed_over_to_id=$2 FROM harness_sessions WHERE id=$1`, first.ID, next.ID).Scan(&closed); err != nil || !closed {
					t.Fatalf("stale predecessor not closed/handed over: %v %v", closed, err)
				}
			}
		})
	}
}

func TestRegistrationAliasInlineResumeFencesBeforeHierarchyAndEvents(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
	ref, alias := "inline-original-"+uid(), "inline-alias-"+uid()
	first, _ := f.registerNative(t, f.alice, f.project, ref, alias)
	f.bind(t, f.alice, thread, first, "0")
	pause, err := json.Marshal(harness.Pause{State: "paused", Handover: &harness.Handover{State: "ready"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped',stop_reason='paused',pause_record=$2 WHERE id=$1`, first, pause); err != nil {
		t.Fatal(err)
	}
	if _, err = f.d.Admin.Exec(t.Context(), `CREATE FUNCTION test_inline_registration_before_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.type='harness.resume_requested' AND (SELECT count(*) FROM harness_sessions)<>2 THEN
 RAISE EXCEPTION 'registration missing before event'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER test_inline_registration_before_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION test_inline_registration_before_event()`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.alice.TenantID).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.call(f.admin, "POST", "/api/projects/"+f.project+"/harness-sessions/"+first+"/resume", map[string]any{"registration": map[string]string{"harness_session_ref": alias, "worker_lease": "inline-resume-lease-" + uid()}}, "")
	}()
	for {
		select {
		case w := <-done:
			t.Fatalf("inline resume returned before tenant fence: status %d", w.Code)
		default:
		}
		var waiting bool
		// Admission now uses the shared work tenant fence before this handler.
		if err = f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'
		 AND $1::int=ANY(pg_blocking_pids(pid)) AND query LIKE '%FROM tenants%' AND query LIKE '%FOR NO KEY UPDATE%')`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	var earlyAdvisory bool
	if err = f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE granted AND locktype='advisory'
	 AND $1::int=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&earlyAdvisory); err != nil || earlyAdvisory {
		t.Fatalf("inline resume acquired an advisory lock before tenant: held=%v err=%v", earlyAdvisory, err)
	}
	var free bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, f.project).Scan(&free); err != nil || !free {
		t.Fatalf("inline resume locked hierarchy before tenant: free=%v err=%v", free, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		expect(t, w, http.StatusOK)
		var out struct{ Successor harness.Session }
		if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		f.bind(t, f.alice, thread, out.Successor.ID, "1")
	case <-ctx.Done():
		t.Fatal("inline resume did not finish")
	}
}
