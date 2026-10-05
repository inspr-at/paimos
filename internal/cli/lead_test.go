// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLeadCLIContractAndProofPrivacy(t *testing.T) {
	const project = "11111111-1111-1111-1111-111111111111"
	const session = "22222222-2222-2222-2222-222222222222"
	const proof = "fixture-lead-lease-private-000000000000"
	for _, action := range []string{"status", "start", "claim", "pause"} {
		t.Run(action, func(t *testing.T) {
			isolate(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/nodes/"+project {
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
				if action == "claim" || action == "pause" {
					path += "/" + action
				}
				if r.URL.Path != path || r.Method != method {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
					http.Error(w, "wrong request", 400)
					return
				}
				calls++
				if action == "claim" && r.Header.Get("X-Aeon-Worker-Lease") != proof {
					t.Error("lease not bound to claim")
				}
				if action != "claim" && r.Header.Get("X-Aeon-Worker-Lease") != "" {
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
					if action == "pause" && body["generation"] != float64(2) {
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
			if action == "pause" {
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
