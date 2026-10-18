// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeployRecordCommandAndFailures(t *testing.T) {
	// Risk: OPS's --version is parsed as the global version switch, the
	// confirmer can be forged, or API failure is reported as recorded.
	for _, scenario := range []string{"record", "json", "no version", "api failure", "missing env", "short SHA", "help"} {
		t.Run(scenario, func(t *testing.T) {
			isolate(t)
			calls := 0
			sha := strings.Repeat("a", 40)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/me":
					fmt.Fprint(w, `{"principal":{"id":"44444444-4444-4444-8444-444444444444","name":"worker"}}`)
				case "/api/projects/lookup":
					if r.URL.Query().Get("ref") != "AIT" {
						t.Error("project key was not resolved")
					}
					fmt.Fprint(w, `{"id":"`+transcriptProjectID+`"}`)
				case "/api/projects/" + transcriptProjectID + "/deploys":
					calls++
					var body map[string]string
					if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Error("invalid deploy request")
					}
					if body["sha"] != sha || body["environment"] != "production" || body["confirmed_by"] != "" {
						t.Errorf("wrong confirmation request: %v", body)
					}
					if scenario == "no version" {
						if _, ok := body["version"]; ok {
							t.Error("omitted version sent a label")
						}
					} else if body["version"] != "26.10.17" {
						t.Error("--version label was lost")
					}
					if scenario == "api failure" {
						w.WriteHeader(422)
						fmt.Fprint(w, `{"error":"deployed SHA is unknown to the project repository"}`)
						return
					}
					fmt.Fprintf(w, `{"event_id":7,"project_id":"%s","repository":"example/ait","environment":"production","sha":"%s","version":"26.10.17","confirmed_at":"2026-10-17T10:00:00Z","confirmed_by":"44444444-4444-4444-8444-444444444444","tickets":[{"node_id":"%s","result":"delivered"},{"node_id":"%s","result":"skipped","reason":"Needs a human check"}]}`, transcriptProjectID, sha, rootIssueID, rootHandoffID)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", testKey)
			args := []string{"aeon", "deploy", "record", "--project", "AIT", "--env", "production", "--sha", strings.ToUpper(sha)}
			if scenario != "no version" {
				args = append(args, "--version", "26.10.17")
			}
			switch scenario {
			case "json":
				args = append(args, "--json")
			case "missing env":
				args = []string{"aeon", "deploy", "record", "--project", "AIT", "--sha", sha}
			case "short SHA":
				args = []string{"aeon", "deploy", "record", "--project", "AIT", "--env", "production", "--sha", "abc123"}
			case "help":
				args = []string{"aeon", "deploy", "record", "--help"}
			}
			code, out, stderr := runCLI(args, "")
			switch scenario {
			case "missing env", "short SHA":
				if code != 2 || calls != 0 {
					t.Fatalf("invalid input wrote: code=%d calls=%d %s", code, calls, stderr)
				}
			case "api failure":
				if code != 1 || strings.Contains(out, "Deploy recorded") || !strings.Contains(stderr, "unknown") {
					t.Fatalf("failure reported success: code=%d %s %s", code, out, stderr)
				}
			case "help":
				if code != 0 || calls != 0 || !strings.Contains(out, "releases.deploy") || !strings.Contains(out, "--version") {
					t.Fatalf("help missing confirmation contract: %s %s", out, stderr)
				}
			case "json":
				var result map[string]any
				if code != 0 || calls != 1 || json.Unmarshal([]byte(out), &result) != nil || result["event_id"] != float64(7) {
					t.Fatalf("JSON result: code=%d %s %s", code, out, stderr)
				}
			default:
				if code != 0 || calls != 1 || !strings.Contains(out, "Delivered: 1; unchanged: 0; skipped: 1") || !strings.Contains(out, "Needs a human check") {
					t.Fatalf("confirmation result: code=%d %s %s", code, out, stderr)
				}
			}
			assertNoSecret(t, out+stderr)
		})
	}
}
