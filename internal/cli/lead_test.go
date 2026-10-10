// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeadCLIContractAndProofPrivacy(t *testing.T) {
	const project = "11111111-1111-1111-1111-111111111111"
	const session = "22222222-2222-2222-2222-222222222222"
	const proof = "fixture-lead-lease-private-000000000000"
	for _, action := range []string{"status", "start", "claim", "pause", "yield"} {
		t.Run(action, func(t *testing.T) {
			isolate(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/nodes/"+project {
					_, _ = w.Write([]byte(`{"id":"` + project + `","key":"LEAD"}`))
					return
				}
				if r.URL.Path == "/api/projects/lookup" {
					_, _ = w.Write([]byte(`{"id":"` + project + `","key":"LEAD"}`))
					return
				}
				if r.URL.Path == "/api/nodes" {
					_, _ = w.Write([]byte(`{"items":[{"id":"` + project + `","key":"` + project + `","kind_id":"p"}]}`))
					return
				}
				if r.URL.Path == "/api/kinds" {
					_, _ = w.Write([]byte(`{"items":[{"id":"p","slug":"project"}]}`))
					return
				}
				path, method := "/api/projects/"+project+"/lead", "GET"
				if action != "status" {
					method = "POST"
				}
				if action == "claim" || action == "pause" || action == "yield" {
					path += "/" + action
				}
				if r.URL.Path != path || r.Method != method {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
					http.Error(w, "wrong request", 400)
					return
				}
				calls++
				if (action == "claim" || action == "yield") && r.Header.Get("X-Aeon-Worker-Lease") != proof {
					t.Error("lease not bound to claim")
				}
				if (action != "claim" && action != "yield") && r.Header.Get("X-Aeon-Worker-Lease") != "" {
					t.Error("person control sent worker proof")
				}
				if action != "status" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["expected_revision"] != float64(3) {
						t.Error("revision missing")
					}
					if action == "claim" && body["session_id"] != session {
						t.Error("session missing")
					}
					if (action == "pause" || action == "yield") && body["generation"] != float64(2) {
						t.Error("generation missing")
					}
					if _, ok := body["worker_lease"]; ok {
						t.Error("lease leaked to public body")
					}
				}
				_, _ = w.Write([]byte(`{"project_id":"` + project + `","revision":4,"generation":2,"state":"waiting_for_room","reason":"host_unavailable","process_active":true}`))
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			args := []string{"aeon", "--json", "lead", action, "--project", project}
			if action != "status" {
				args = append(args, "--expected-revision", "3")
			}
			input := ""
			if action == "claim" {
				args = append(args, "--session", session, "--worker-lease-file", "-")
				input = proof
			}
			if action == "yield" {
				args = append(args, "--worker-lease-file", "-")
				input = proof
			}
			if action == "pause" || action == "yield" {
				args = append(args, "--generation", "2")
			}
			code, out, stderr := runCLI(args, input)
			if code != 0 || calls != 1 {
				t.Fatalf("code %d calls %d out %s stderr %s", code, calls, out, stderr)
			}
			if strings.Contains(out+stderr, proof) {
				t.Fatal("worker proof printed")
			}
			assertNoSecret(t, out+stderr)
		})
	}
	for _, args := range [][]string{{"lead", "start", "--project", project}, {"lead", "claim", "--project", project, "--expected-revision", "1", "--session", "bad"}, {"lead", "pause", "--project", project, "--expected-revision", "1"}} {
		isolate(t)
		code, _, _ := runCLI(append([]string{"aeon"}, args...), "")
		if code != 2 {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
}

func TestLeadHandoffCLIAndQueueDispatchPrivacy(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	const proof = "fixture-lead-lease-private-000000000000"
	for _, action := range []string{"handoff", "next"} {
		t.Run(action, func(t *testing.T) {
			isolate(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if action == "handoff" {
					if r.Method != "GET" || r.URL.Path != "/api/runs/"+id+"/handoff" {
						t.Error("wrong handoff route")
					}
					_, _ = w.Write([]byte(`{"run_id":"` + id + `","ticket_id":"` + id + `","state":"launch_unknown","lead_generation":1,"work_order_revision":2}`))
				} else {
					if r.Method != "POST" || r.URL.Path != "/api/queue/next" || r.Header.Get("X-Aeon-Lead-Session") != id || r.Header.Get("X-Aeon-Lead-Generation") != "3" || r.Header.Get("X-Aeon-Worker-Lease") != proof {
						t.Error("dispatch proof lost")
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 0 {
						t.Error("proof leaked into body")
					}
					_, _ = w.Write([]byte(`{"entry":null}`))
				}
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			args := []string{"aeon", "--json", "lead", "handoff", id}
			input := ""
			if action == "next" {
				args = []string{"aeon", "--json", "queue", "next", "--lead-session", id, "--lead-generation", "3", "--worker-lease-file", "-"}
				input = proof
			}
			code, out, stderr := runCLI(args, input)
			if code != 0 || calls != 1 {
				t.Fatalf("code %d calls %d: %s %s", code, calls, out, stderr)
			}
			if strings.Contains(out+stderr, proof) {
				t.Fatal("worker lease printed")
			}
			assertNoSecret(t, out+stderr)
		})
	}
	for _, args := range [][]string{{"lead", "handoff", "bad"}, {"queue", "next", "--lead-session", id}, {"queue", "next", "--lead-generation", "1"}} {
		isolate(t)
		code, _, _ := runCLI(append([]string{"aeon"}, args...), "")
		if code != 2 {
			t.Fatalf("invalid handoff args: %v", args)
		}
	}
}

func TestHarnessLeadClaimUsesExistingFilesAndRedactsProof(t *testing.T) {
	const project = "11111111-1111-1111-1111-111111111111"
	const ref = "codex:existing-context-fixture-000000"
	const lease = "fixture-adoptlead-private-lease-000000"
	for _, mode := range []string{"success", "server_error", "echo_success", "no_person_confirmation"} {
		t.Run(mode, func(t *testing.T) {
			isolate(t)
			dir := t.TempDir()
			refFile := filepath.Join(dir, "session")
			leaseFile := filepath.Join(dir, "lease")
			if err := os.WriteFile(refFile, []byte(ref), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(leaseFile, []byte(lease), 0600); err != nil {
				t.Fatal(err)
			}
			claims := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/nodes/" + project:
					_, _ = w.Write([]byte(`{"id":"` + project + `","key":"LEAD"}`))
				case "/api/projects/lookup":
					_, _ = w.Write([]byte(`{"id":"` + project + `","key":"LEAD"}`))
				case "/api/nodes":
					_, _ = w.Write([]byte(`{"items":[{"id":"` + project + `","key":"` + project + `","kind_id":"p"}]}`))
				case "/api/kinds":
					_, _ = w.Write([]byte(`{"items":[{"id":"p","slug":"project"}]}`))
				case "/api/projects/" + project + "/lead":
					if r.Method != "GET" || r.Header.Get("X-Aeon-Worker-Lease") != "" {
						t.Error("state read leaked proof or mutated")
					}
					revision := 1
					if mode == "no_person_confirmation" {
						revision = 0
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"revision": revision, "reason": "adoption_pending"})
				case "/api/projects/" + project + "/lead/claim":
					claims++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if r.Method != "POST" || r.Header.Get("X-Aeon-Worker-Lease") != lease || body["harness_session_ref"] != ref || body["expected_revision"] != float64(1) || len(body) != 2 {
						t.Error("wrong claim proof or revision")
					}
					if mode == "server_error" {
						w.WriteHeader(403)
						_ = json.NewEncoder(w).Encode(map[string]string{"error": "rejected " + lease + " " + ref})
						return
					}
					reason := ""
					if mode == "echo_success" {
						reason = lease + ref
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"project_id": project, "revision": 2, "generation": 1, "state": "working", "reason": reason, "process_active": true})
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					http.Error(w, "unexpected", 400)
				}
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			code, out, stderr := runCLI([]string{"aeon", "--json", "harness", "lead", "claim", "--project", project, "--harness-session-file", refFile, "--worker-lease-file", leaseFile}, "")
			if mode == "success" && (code != 0 || claims != 1 || !strings.Contains(out, `"state":"working"`)) {
				t.Fatalf("claim failed %d %s %s", code, out, stderr)
			}
			if mode != "success" && code == 0 {
				t.Fatal("failed or unconfirmed claim reported success")
			}
			if mode == "no_person_confirmation" && claims != 0 {
				t.Fatal("claim bypassed person confirmation")
			}
			if strings.Contains(out+stderr, ref) || strings.Contains(out+stderr, lease) {
				t.Fatal("private proof appeared in output")
			}
			assertNoSecret(t, out+stderr)
		})
	}
}

// Risk: native coordinators register a different proof than session.ref, and
// claim must work while the heartbeat owns its lock without printing either proof.
func TestHarnessLeadClaimDerivesRegisteredReferenceFromState(t *testing.T) {
	for _, shape := range []string{"random", "native"} {
		for _, legacy := range []bool{false, true} {
			t.Run(shape+map[bool]string{false: "/saved", true: "/legacy"}[legacy], func(t *testing.T) {
				var calls []hbCall
				claims, metadataReads := 0, 0
				var expectedRef, expectedLease string
				srv := hbServer(t, &calls, func(r *http.Request, body map[string]any, w http.ResponseWriter) bool {
					switch r.URL.Path {
					case harnessPath(transcriptProjectID, transcriptSessionID):
						metadataReads++
						if r.Method != "GET" || r.Header.Get("X-Aeon-Worker-Lease") != "" {
							t.Error("metadata read mutated or leaked proof")
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"id": transcriptSessionID, "harness": "claude", "role": "coordinator"})
						return true
					case "/api/projects/" + transcriptProjectID + "/lead":
						if r.Method != "GET" || r.Header.Get("X-Aeon-Worker-Lease") != "" {
							t.Error("lead read mutated or leaked proof")
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"revision": 4, "reason": "adoption_pending"})
						return true
					case "/api/projects/" + transcriptProjectID + "/lead/claim":
						claims++
						if r.Method != "POST" || r.Header.Get("X-Aeon-Worker-Lease") != expectedLease || body["harness_session_ref"] != expectedRef || body["expected_revision"] != float64(4) || len(body) != 2 {
							t.Error("claim did not match registered proof and displayed revision")
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"project_id": transcriptProjectID, "revision": 5, "generation": 1, "state": "working"})
						return true
					}
					return false
				})
				defer srv.Close()
				rt, _, _ := heartbeatRuntime(t, srv)
				o := heartbeatTestOptions(t.TempDir())
				o.Role = "coordinator"
				if shape == "native" {
					o.SourceSession = strings.ToUpper(transcriptEntryID)
				}
				session, created, err := rt.openHeartbeatSession(t.Context(), o, heartbeatDeps{alive: func(int) bool { return true }})
				if err != nil || !created {
					t.Fatal("heartbeat registration did not create the fixture")
				}
				defer session.hold.release()
				regs := hbWhere(calls, "POST", "/harness-sessions")
				if len(regs) != 1 {
					t.Fatal("fixture did not retain registration")
				}
				expectedRef, expectedLease = regs[0].body["harness_session_ref"].(string), session.lease
				fallback, err := readStateSecret(&session.hold, "session.ref")
				if err != nil {
					t.Fatal("fixture reference unavailable")
				}
				if shape == "native" && (expectedRef != "claude:"+strings.ToLower(o.SourceSession) || expectedRef == fallback) || shape == "random" && expectedRef != fallback {
					t.Fatal("fixture did not establish both registration shapes")
				}
				if legacy {
					session.disk.RegisteredRef = ""
					if shape == "native" {
						if err := session.hold.writeFile("index.source", []byte(o.SourceSession+"\n")); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := saveHeartbeatSession(&session); err != nil {
					t.Fatal(err)
				}
				for _, jsonOut := range []bool{false, true} {
					args := []string{"aeon", "harness", "lead", "claim", "--state-dir", o.StateDir}
					if jsonOut {
						args = append(args, "--json")
					}
					code, out, stderr := runCLI(args, "")
					if code != 0 || !strings.Contains(out, "working") {
						t.Fatal("state directory claim failed")
					}
					if strings.Contains(out+stderr, expectedRef) || strings.Contains(out+stderr, fallback) || strings.Contains(out+stderr, expectedLease) {
						t.Fatal("claim printed private proof")
					}
					assertNoSecret(t, out+stderr)
				}
				wantReads := 0
				if legacy && shape == "native" {
					wantReads = 2
				}
				if claims != 2 || metadataReads != wantReads || len(hbWhere(calls, "POST", "/harness-sessions")) != 1 {
					t.Fatal("claim performed unexpected registration or metadata work")
				}
			})
		}
	}
	t.Run("help_and_exclusive_flags", func(t *testing.T) {
		isolate(t)
		code, out, stderr := runCLI([]string{"aeon", "harness", "lead", "claim", "--help"}, "")
		if code != 0 || !strings.Contains(out+stderr, "--state-dir") || !strings.Contains(out+stderr, "HARNESS:SOURCE-UUID") {
			t.Fatal("claim help does not explain state and native references")
		}
		code, _, stderr = runCLI([]string{"aeon", "harness", "lead", "claim", "--state-dir", "absent", "--harness-session-file", "absent"}, "")
		if code != 2 || !strings.Contains(stderr, "cannot be combined") {
			t.Fatal("ambiguous proof sources were accepted")
		}
	})
}
