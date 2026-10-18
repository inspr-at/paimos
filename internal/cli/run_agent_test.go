// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/client"
)

func TestLocalRunnerFakeCommandReportsBack(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	var out, errOut bytes.Buffer
	adapter := &localClaudeAdapter{opts: runAgentOptions{Exec: `test -f "$PAIMOS_PROMPT_FILE" && cat "$PAIMOS_PROMPT_FILE"`, Yes: true},
		homes: map[string]string{"local": ""}, stdout: &out, stderr: &errOut, projectID: "project", idle: time.Minute, maxRun: time.Minute}
	request := agentd.StartRequest{Run: agentd.Run{ID: "run-1"}, AccountKey: "local", Workspace: workspace, StateRoot: state, Prompt: "Implement the work order"}
	proc, err := adapter.Start(t.Context(), request, func(agentd.AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if proc.PID() < 1 || proc.Wait() != nil || !strings.Contains(out.String(), request.Prompt) {
		t.Fatalf("fake runner pid=%d out=%q err=%q", proc.PID(), out.String(), errOut.String())
	}
	if evidence := proc.(agentd.EvidenceProcess).Evidence(); !strings.Contains(evidence, "remain for review") {
		t.Fatalf("report-back evidence %q", evidence)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("prompt cleanup: %v, entries %d", err, len(entries))
	}
	adapter.opts.Exec = "exit 7"
	proc, err = adapter.Start(t.Context(), request, func(agentd.AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err == nil {
		t.Fatal("failed fake runner reported success")
	}
}

func TestRunAgentReadsSharedAccountRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	raw := `{"accounts":[{"harness":"claude","key":"local","account_id":"11111111-1111-4111-8111-111111111111","home":"/tmp/claude","identity":"worker@example.invalid"},{"harness":"grok","key":"other","account_id":"22222222-2222-4222-8222-222222222222","grok":{"source":"test"}}]}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	accounts, err := loadLocalAccounts(path)
	if err != nil || len(accounts) != 2 || accounts[0].Key != "local" {
		t.Fatalf("account registry %+v: %v", accounts, err)
	}
}

type queuedProjectAPI struct {
	agentd.API
	runs     []agentd.Run
	profiles []agentd.Profile
}

func (f queuedProjectAPI) Queued(context.Context) ([]agentd.Run, error)       { return f.runs, nil }
func (f queuedProjectAPI) Profiles(context.Context) ([]agentd.Profile, error) { return f.profiles, nil }

func TestRunAgentScopesQueuedWorkOrdersToProject(t *testing.T) {
	project := "11111111-1111-4111-8111-111111111111"
	other := "22222222-2222-4222-8222-222222222222"
	inside := "33333333-3333-4333-8333-333333333333"
	outside := "44444444-4444-4444-8444-444444444444"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case inside:
			_, _ = w.Write([]byte(`{"parent_id":"` + project + `"}`))
		case outside:
			_, _ = w.Write([]byte(`{"parent_id":"` + other + `"}`))
		case other:
			_, _ = w.Write([]byte(`{"parent_id":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base := queuedProjectAPI{runs: []agentd.Run{{ID: "one", WorkOrderID: inside, ModelProfileID: "claude"}, {ID: "two", WorkOrderID: outside, ModelProfileID: "claude"}, {ID: "three", WorkOrderID: inside, ModelProfileID: "codex"}},
		profiles: []agentd.Profile{{ID: "claude", Harness: agentd.Claude}, {ID: "codex", Harness: agentd.Codex}}}
	scoped := &projectRunAPI{API: base, client: client.New(srv.URL, ""), projectID: project}
	runs, err := scoped.Queued(t.Context())
	if err != nil || len(runs) != 1 || runs[0].ID != "one" {
		t.Fatalf("scoped runs %+v err %v", runs, err)
	}
}

func TestRunAgentClaimsAssignedRunThroughProjectAPI(t *testing.T) {
	pickup := agentd.WorkerPickup{TicketRevision: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), WorkOrderRevision: 3, BriefSHA256: strings.Repeat("a", 64), WorktreeID: strings.Repeat("b", 64)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/runs/assigned/claim" {
			t.Error("wrong assignment claim route")
		}
		var body struct {
			Daemon       string              `json:"daemon_id"`
			Generation   string              `json:"daemon_generation"`
			Reservations []string            `json:"reservation_ids"`
			Handoff      agentd.WorkerPickup `json:"handoff"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Daemon != "daemon" || body.Generation != "generation" || !reflect.DeepEqual(body.Reservations, []string{"reservation"}) || !reflect.DeepEqual(body.Handoff, pickup) {
			t.Error("project runner changed assignment pickup")
		}
		// A failed claim must remain an error through the wrapper.
		if calls == 2 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	remote := agentd.NewRemote(server.URL, "test-key")
	// Construct exactly the API used by run-agent watch and check the same
	// optional capability that Supervisor.claimWorker checks.
	scoped := &projectRunAPI{API: remote, client: remote.Client, projectID: "project"}
	claim, ok := any(scoped).(interface {
		ClaimHandoff(context.Context, string, string, string, []string, agentd.WorkerPickup) error
	})
	if !ok {
		t.Fatal("project runner drops assignment pickup capability")
	}
	if err := claim.ClaimHandoff(t.Context(), "assigned", "daemon", "generation", []string{"reservation"}, pickup); err != nil {
		t.Fatal(err)
	}
	if err := claim.ClaimHandoff(t.Context(), "assigned", "daemon", "generation", []string{"reservation"}, pickup); err == nil {
		t.Fatal("project runner hid rejected claim")
	}
	if calls != 2 {
		t.Fatalf("claim requests repeated or missing: %d", calls)
	}
	unavailable := &projectRunAPI{API: queuedProjectAPI{}}
	claim = any(unavailable).(interface {
		ClaimHandoff(context.Context, string, string, string, []string, agentd.WorkerPickup) error
	})
	if err := claim.ClaimHandoff(t.Context(), "assigned", "daemon", "generation", nil, pickup); !errors.Is(err, agentd.ErrUnsupported) {
		t.Fatalf("missing capability did not fail closed: %v", err)
	}
}
