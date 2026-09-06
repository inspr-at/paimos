// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/models"
)

// Exercise the shipped Cobra tree, not a runner accepting arbitrary argv.
// Status is read-only; drain and completion must retain private worker attribution.
func TestNativeConsumerRealCLIStatusAndAttributedDelivery(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "paimos")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "../paimos")
	build.Env = append(os.Environ(), "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shipped CLI: %v\n%s", err, out)
	}
	process := &nativeFixtureProcess{done: make(chan struct{})}
	controller, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []agentd.Adapter{nativeFixtureAdapter{process}}})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())
	session, err := controller.Start(context.Background(), agentd.StartRequest{Adapter: "codex", Identity: "codex:worker", ProjectID: 42, Workspace: t.TempDir(), Prompt: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	public, target, delivery := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := controller.CheckpointReporter(context.Background(), session.ID, agentd.ControlRequest{Instance: "fixture", ProjectID: 42, Identity: "codex:worker"}, agentd.ReporterState{PublicSessionID: public, Capabilities: []agentd.Capability{agentd.CapabilityInbox}}); err != nil {
		t.Fatal(err)
	}
	remote := models.HarnessSession{ID: public, ProjectID: 42, AgentName: "worker", Harness: "codex", Host: "fixture-host", ManagementMode: "managed", MessageTargetID: target, Phase: "working", Capabilities: models.HarnessCapabilities{Inbox: true}}
	message := agentmessage.Envelope{Cursor: 1, MessageID: uuid.NewString(), To: "codex:worker", Parts: []agentmessage.TextPart{{Kind: "text", Text: "fixture followup"}}, DeliveryWork: &agentmessage.DeliveryWork{DeliveryID: delivery, Instance: "fixture", ProjectID: 42, State: "leased", Adapter: agentmessage.AdapterManagedHarness, RequestedLevel: "simple", MaximumLevel: "simple"}}
	leases := newMemoryReporterLeaseStore()
	lease, err := leases.GetOrCreate(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	const apiKey = "fixture-private-api-key"
	keyFile := filepath.Join(root, "api-key")
	if err := os.WriteFile(keyFile, []byte(apiKey), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	statusReads, drains, completions := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Error("CLI lost protected API authentication")
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) {
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/projects":
			write([]map[string]any{{"id": 42, "key": "FIX"}})
		case "GET /api/projects/42/harness-sessions/" + public:
			if r.Header.Get("X-Paimos-Harness-Worker-Lease") != "" || r.Header.Get("X-Paimos-Agent-Name") != "" {
				t.Error("read-only status unexpectedly used worker proof")
			}
			statusReads++
			write(remote)
		case "GET /api/projects/42/message-targets":
			if r.URL.Query().Get("address") != "codex:worker" {
				t.Error("target inventory lost receiver")
			}
			write(map[string]any{"targets": []agentmessage.Target{{ID: target, Instance: "fixture", ProjectID: 42, Address: "codex:worker", Adapter: agentmessage.AdapterManagedHarness, Enabled: true, Role: "primary", Version: 1}}})
		case "POST /api/projects/42/harness-sessions/" + public + "/drain", "POST /api/projects/42/harness-sessions/" + public + "/complete-delivery":
			if r.Header.Get("X-Paimos-Harness-Worker-Lease") != lease || r.Header.Get("X-Paimos-Agent-Name") != "worker" {
				t.Error("worker command lost exact lease or agent")
				http.Error(w, "forbidden", 403)
				return
			}
			if r.URL.Path == "/api/projects/42/harness-sessions/"+public+"/drain" {
				drains++
				messages := []agentmessage.Envelope{}
				if completions == 0 {
					messages = append(messages, message)
				}
				write(agentmessage.InboxPage{Address: "codex:worker", Messages: messages})
				return
			}
			var body struct {
				Cursor     int64  `json:"cursor"`
				DeliveryID string `json:"delivery_id"`
				Level      string `json:"effective_level"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Cursor != 1 || body.DeliveryID != delivery || body.Level != "simple" {
				t.Error("completion changed leased identity or outcome")
				http.Error(w, "invalid", 400)
				return
			}
			completions++
			write(agentmessage.CursorState{Address: "codex:worker", Cursor: 1})
		default:
			t.Errorf("unexpected CLI HTTP route: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	reporter, err := newCLIReporterWithRunner("fixture", "fixture-host", binary, reporterEnvironment([]string{"HOME=" + root}, server.URL, keyFile), runReporterCommand, leases)
	if err != nil {
		t.Fatal(err)
	}
	consumers, err := newNativeConsumers(root, "fixture", reporter)
	if err != nil {
		t.Fatal(err)
	}
	defer consumers.supervisor.Stop()
	consumers.controller = controller
	consumers.reconcile(context.Background())
	consumers.reconcile(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if statusReads < 2 || drains != 2 || completions != 1 || process.inboxes != 1 {
		t.Fatalf("real CLI did not verify/drain/complete once: status=%d drains=%d completions=%d effects=%d", statusReads, drains, completions, process.inboxes)
	}
}
