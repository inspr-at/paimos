// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/agentcompat"
)

func TestAgentReportNegotiatesThroughExistingLifecycleResponse(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "window"}[modern], func(t *testing.T) {
			view := View{}
			if modern {
				view.AgentCompatibility = &agentcompat.Result{Status: "unknown"}
			}
			progress := reportRelease(view, &SetupProgress{State: "connected"})
			if (progress.AgentRelease != nil) != modern {
				t.Fatal("additive identity was not negotiated")
			}
			if modern && *progress.AgentRelease != agentcompat.Current() {
				t.Fatal("wrong helper identity")
			}
		})
	}
}

func TestAgentReportSurvivesServerDowngrade(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-pairing/reconcile" || r.Header.Get("Authorization") != "" {
			t.Error("lifecycle made an unrelated request")
		}
		var proof ProofRequest
		if err := json.NewDecoder(r.Body).Decode(&proof); err != nil {
			t.Error(err)
			return
		}
		calls++
		if proof.Progress.AgentRelease != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "invalid_request"})
			return
		}
		if proof.Progress.State != "connected" {
			t.Error("fallback changed lifecycle state")
		}
		_ = json.NewEncoder(w).Encode(View{SetupState: "connected"})
	}))
	defer server.Close()
	client := HTTPClient{Origin: server.URL, HTTP: server.Client()}
	progress := reportRelease(View{AgentCompatibility: &agentcompat.Result{Status: "unknown"}}, &SetupProgress{State: "connected"})
	view, err := client.Reconcile(context.Background(), ProofRequest{Progress: progress})
	if err != nil || view.SetupState != "connected" || calls != 2 || progress.AgentRelease == nil {
		t.Fatal("downgrade changed caller or blocked lifecycle")
	}
}
