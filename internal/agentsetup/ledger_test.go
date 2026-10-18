// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Risk: an old server must never be guessed ledger-capable, and enrollment must
// retain the paired computer's existing runtime authority and generation.
func TestLedgerServerNegotiationAndEnrollmentClient(t *testing.T) {
	for _, view := range []View{{}, {ServerCapabilities: []string{"pairing-v1"}}} {
		if RequireLedgerServer(view) == nil {
			t.Fatal("old server admitted shared mode")
		}
	}
	if RequireLedgerServer(View{ServerCapabilities: []string{LedgerCapability}}) != nil {
		t.Fatal("ledger-capable server refused")
	}
	const generation = "01234567-89ab-4cde-8fab-0123456789ab"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/agent-pairing/self/ledger" || r.Header.Get("Authorization") != "Bearer fixture-runtime" {
			t.Error("wrong enrollment authority or destination")
		}
		var in struct {
			Generation string `json:"generation"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Generation != generation {
			t.Error("generation changed")
		}
		g := generation
		_ = json.NewEncoder(w).Encode(View{ServerCapabilities: []string{LedgerCapability}, LedgerMode: true, LedgerGeneration: &g})
	}))
	defer server.Close()
	client := HTTPClient{Origin: server.URL, HTTP: server.Client()}
	v, err := client.EnrollLedger(t.Context(), "fixture-runtime", generation)
	if err != nil || !v.LedgerMode || v.LedgerGeneration == nil || *v.LedgerGeneration != generation {
		t.Fatal("client lost ledger lifecycle response")
	}
}
