// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

// Risk: content is read before Prepare acquires its proof, sibling calls use
// the last prepared run's credential, or a credential reaches a durable record.
func TestPrepareRunContentUsesOnlyItsServerCapability(t *testing.T) {
	const tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const tokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	proofs := map[string]string{"a": tokenA, "b": tokenB}
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer synthetic-transport-key" {
			t.Error("transport credential missing")
		}
		parts := strings.Split(r.URL.Path, "/")
		id := parts[len(parts)-1]
		if strings.HasSuffix(r.URL.Path, "/capability") {
			id = parts[len(parts)-2]
			if r.Header.Get(db.RunCapabilityPolicyHeader) != db.RunCapabilityPolicy || r.Header.Get(db.RunCapabilityHeader) != "" {
				t.Error("wrong capability negotiation")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"capability": proofs[id], "run_id": id, "acting_person_id": "person-" + id, "daemon_id": "daemon", "daemon_generation": "generation"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/runs/") {
			if r.Header.Get(db.RunCapabilityHeader) != proofs[id] {
				t.Error("run content fetched without its proof")
			}
			_ = json.NewEncoder(w).Encode(Run{ID: id, WorkOrderID: "order-" + id, AgentPrincipalID: "agent", ActingPersonID: "person-" + id, Status: "queued", RecoveryBrief: "private recovery context"})
			return
		}
		id = strings.TrimPrefix(id, "order-")
		if r.Header.Get(db.RunCapabilityHeader) != proofs[id] {
			t.Error("work-order proof crossed runs")
		}
		if strings.HasPrefix(r.URL.Path, "/api/nodes/") {
			_ = json.NewEncoder(w).Encode(Node{ID: "order-" + id, Key: "WORK-1", Title: "Task", Body: "Private body"})
		} else {
			_ = json.NewEncoder(w).Encode(WorkOrder{NodeID: "order-" + id, Status: "ready"})
		}
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "synthetic-transport-key")
	for _, id := range []string{"a", "b"} {
		run, node, order, err := remote.PrepareRunContent(t.Context(), Run{ID: id, WorkOrderID: "order-" + id, AgentPrincipalID: "agent", ActingPersonID: "person-" + id, Status: "queued", RunCapabilityRequired: true}, "daemon", "generation")
		if err != nil || node.Body != "Private body" || order.NodeID != node.ID || run.RecoveryBrief != "private recovery context" {
			t.Fatalf("prepare failed: %v", err)
		}
		b, _ := json.Marshal(run)
		if strings.Contains(string(b), proofs[id]) {
			t.Fatal("credential serialized in run")
		}
	}
	want := []string{"POST /api/runs/a/capability", "GET /api/runs/a", "GET /api/nodes/order-a", "GET /api/work-orders/order-a", "POST /api/runs/b/capability", "GET /api/runs/b", "GET /api/nodes/order-b", "GET /api/work-orders/order-b"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("content read outside Prepare order: %v", calls)
	}
	// Tool callbacks have fresh contexts; their identity remains the captured
	// run rather than the most recent preparation on the shared Remote.
	_, err := remote.RunToolsFor("a").WorkOrder(context.Background(), "order-a")
	if err != nil {
		t.Fatal(err)
	}
}

// Risk: native harnesses inheriting the daemon environment expose its runtime
// key, including an aliased variable, while selecting their provider account.
func TestRunChildrenScrubTransportEnvironment(t *testing.T) {
	t.Setenv("AEON_RUNTIME_KEY", "synthetic-transport-secret")
	t.Setenv("AEON_TOKEN", "synthetic-transport-secret")
	t.Setenv("PAIMOS_API_KEY", "synthetic-transport-secret")
	t.Setenv("ALIASED_CREDENTIAL", "aeon_synthetic_transport_credential")
	for _, env := range [][]string{
		withEnv("CODEX_HOME", "/fixture/account"),
		harnesslaunch.Environment([]string{"AEON_RUNTIME_KEY=synthetic-transport-secret", "PAIMOS_TOKEN=synthetic-transport-secret", "ALIASED_CREDENTIAL=aeon_synthetic_transport_credential", "OPENAI_API_KEY=provider-account", "HOME=/fixture/account"}, "/fixture/bin/node"),
	} {
		for _, entry := range env {
			if strings.Contains(entry, "synthetic-transport") || strings.Contains(entry, "aeon_synthetic_transport") {
				t.Fatal("transport secret inherited")
			}
		}
	}
	provider := harnesslaunch.WithoutTransportCredentials([]string{"OPENAI_API_KEY=provider-account", "HOME=/fixture/account"})
	if len(provider) != 2 {
		t.Fatal("provider account environment lost")
	}
}
