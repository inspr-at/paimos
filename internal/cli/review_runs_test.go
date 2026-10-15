// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

// Risk: gate tooling leaks capabilities, submits the coordinator key, follows
// redirects, or prints success when the authenticated verdict was refused.
func TestReviewRunCommandsKeepCredentialsPrivateAndReportRefusals(t *testing.T) {
	isolate(t)
	ticket := "00000000-0000-4000-8000-000000000001"
	runID := "00000000-0000-4000-8000-000000000002"
	capability := "aer_00000000-0000-4000-8000-000000000003_" + strings.Repeat("A", 43)
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	binding := reviewgate.RunBinding{Ticket: ticket, Repository: "owner/repo", PR: 7, Target: "main", Base: base, Head: head, Scope: base + ".." + head}
	run := reviewgate.Run{ID: runID, RunBinding: binding, Kind: "review", Model: "claude-opus-4-6", Harness: "claude", ReviewerFamily: "anthropic"}
	claims, submissions, creates := 0, 0, 0
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/nodes/" + ticket:
			json.NewEncoder(w).Encode(apiNode{ID: ticket, Key: "RR-1"})
		case "/api/nodes/" + ticket + "/review-runs":
			creates++
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				t.Error("run creation lost dispatcher authentication")
			}
			var in reviewgate.RunCreate
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Scope != binding.Scope || in.Target != "main" {
				t.Error("creation lost exact scope")
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(run)
		case "/api/review-runs/" + runID + "/credential":
			claims++
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				t.Error("claim lost reviewer authentication")
			}
			json.NewEncoder(w).Encode(map[string]string{"credential": capability})
		case "/api/review-runs/" + runID + "/verdict":
			submissions++
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get(reviewgate.CredentialHeader) != capability {
				t.Error("submission did not use only the claimed capability")
			}
			var in reviewgate.RunSubmit
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Binding != binding {
				t.Error("submission lost exact run binding")
			}
			if status == 307 {
				w.Header().Set("Location", "/leaked")
				w.WriteHeader(status)
				return
			}
			w.WriteHeader(status)
			if status == 200 {
				json.NewEncoder(w).Encode(run)
			} else {
				json.NewEncoder(w).Encode(map[string]string{"error": "refused " + capability})
			}
		default:
			t.Error("unexpected or redirected request")
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "review", "run", "create", "--ticket", ticket, "--repo", "owner/repo", "--pr", "7", "--base", base, "--head", head, "--author-session", ticket, "--reviewer-session", runID, "--json"}
	code, out, stderr := runCLI(args, "")
	if code != 0 || stderr != "" || !strings.Contains(out, runID) || creates != 1 {
		t.Fatal("run create failed")
	}
	args = []string{"aeon", "review", "run", "submit", "--ticket", ticket, "--repo", "owner/repo", "--pr", "7", "--base", base, "--head", head, "--run", runID, "--verdict", "ok", "--visual-impact", "no", "--reason", "Exact range; no presentation change.", "--json"}
	for _, wantStatus := range []int{200, 403, 307} {
		status = wantStatus
		code, out, stderr = runCLI(args, "")
		if strings.Contains(out, capability) || strings.Contains(stderr, capability) {
			t.Fatal("private capability leaked to CLI output")
		}
		if wantStatus == 200 && (code != 0 || !strings.Contains(out, runID) || stderr != "") {
			t.Fatal("successful verdict not reported")
		}
		if wantStatus != 200 && (code == 0 || out != "") {
			t.Fatal("refused/redirected verdict reported success")
		}
	}
	t.Setenv("AEON_API_KEY", "")
	status = 200
	code, out, stderr = runCLI(append(args, "--credential-file", "-"), capability+"\n")
	if code != 0 || stderr != "" || !strings.Contains(out, runID) || strings.Contains(out, capability) {
		t.Fatal("claimed capability could not submit without an ordinary API key")
	}
	if claims != 3 || submissions != 4 {
		t.Fatal("claims or verdicts were lost or retried")
	}
}
