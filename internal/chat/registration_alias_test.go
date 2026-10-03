// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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
				var sessions, events, claims, afterSessions, afterEvents, afterClaims int
				const counts = `SELECT (SELECT count(*) FROM harness_sessions),(SELECT count(*) FROM events),(SELECT count(*) FROM chat_native_contexts)`
				if err := f.d.Admin.QueryRow(t.Context(), counts).Scan(&sessions, &events, &claims); err != nil {
					t.Fatal(err)
				}
				w := f.replayNative(actor, f.project, ref, "conflict-creation-lease-"+uid(), alias)
				expect(t, w, http.StatusConflict)
				if !strings.Contains(w.Body.String(), "chat binding unavailable") {
					t.Fatal("creation rejected for a reason other than historical ownership")
				}
				if err := f.d.Admin.QueryRow(t.Context(), counts).Scan(&afterSessions, &afterEvents, &afterClaims); err != nil || sessions != afterSessions || events != afterEvents || claims != afterClaims {
					t.Fatalf("failed creation left partial state: sessions=%d/%d events=%d/%d claims=%d/%d err=%v", sessions, afterSessions, events, afterEvents, claims, afterClaims, err)
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
