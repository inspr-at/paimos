// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/deploytarget"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/version"
)

func TestRemoteTelemetryErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		message  string
		protocol bool
	}{
		{"malformed", 400, "status report requires status", true},
		{"too_large", 413, "payload too large", true},
		{"unprocessable", 422, "invalid report", true},
		{"divergent_replay", 409, "divergent telemetry replay", true},
		{"sequence", 409, "telemetry sequence is not monotonic", true},
		{"state_transition", 409, "run cannot return to starting", true},
		{"unauthorized", 401, "invalid key", false},
		{"forbidden", 403, "enrollment revoked", false},
		{"missing", 404, "run not found", false},
		{"timeout", 408, "request timeout", false},
		{"generation", 409, "daemon generation conflict", false},
		{"not_live", 409, "run is not live", false},
		{"draining", 409, "enrollment_draining", false},
		{"pairing_revoked", 409, "pairing_revoked", false},
		{"unknown_conflict", 409, "unknown conflict", false},
		{"revoked", 410, "enrollment_revoked", false},
		{"rate_limited", 429, "slow down", false},
		{"unavailable", 503, "unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.message})
			}))
			defer server.Close()
			remote := NewRemote(server.URL, "test-key")
			remote.daemonID, remote.generation = "daemon", "generation"
			err := remote.Report(t.Context(), "run", Telemetry{Sequence: 1, Kind: "status"})
			if err == nil || errors.Is(err, ErrTelemetryProtocol) != tc.protocol {
				t.Fatalf("HTTP %d classified incorrectly: %v", tc.status, err)
			}
		})
	}
}

func TestRemoteLostClaimAndPerRunTelemetryBinding(t *testing.T) {
	var mu sync.Mutex
	var reported []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runs/run/claim" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/runs/run/telemetry" {
			mu.Lock()
			reported = append(reported, r.Header.Get("X-Aeon-Daemon-ID")+"/"+r.Header.Get("X-Aeon-Daemon-Generation"))
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "test-key")
	if err := remote.Claim(t.Context(), "run", "daemon", "original", []string{"reservation"}); err == nil {
		t.Fatal("claim failure hidden")
	}
	if err := remote.Report(t.Context(), "run", Telemetry{Sequence: 1, Kind: "finished", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := remote.Claim(t.Context(), "other-run", "daemon", "new-generation", []string{"other-reservation"}); err != nil {
		t.Fatal(err)
	}
	if err := remote.ReportForClaim(t.Context(), "run", "daemon", "original", Telemetry{Sequence: 1, Kind: "finished", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 2 || reported[0] != "daemon/original" || reported[1] != "daemon/original" {
		t.Fatal("terminal retry lost the exact prior claim generation")
	}
}

func TestRemoteHarnessMetadataOmitsUnknownAndUnchangedFields(t *testing.T) {
	var registration map[string]any
	var beats []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/api/projects/project/harness-sessions":
			registration = body
			_, _ = w.Write([]byte(`{"id":"session","project_id":"project"}`))
		case "/api/projects/project/harness-sessions/session/heartbeat":
			if r.Header.Get("X-Aeon-Worker-Lease") != "owned-lease" {
				t.Error("missing owned worker lease")
			}
			beats = append(beats, body)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected harness path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "test-key")
	session, err := remote.RegisterHarness(t.Context(), HarnessSession{ID: "reference", ProjectID: "project", Lease: "owned-lease", AccountLabel: "known account"},
		"agent", "run", "order", Codex, "host", []string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	for i, update := range []HarnessSession{
		{Model: "model-a", ReasoningEffort: "high"},
		{Model: "model-b"},
		{Activity: "idle"},
	} {
		update.ID, update.ProjectID, update.Lease, update.Harness = session.ID, session.ProjectID, session.Lease, session.Harness
		update.ActivitySequence = int64(i + 1)
		if err := remote.HeartbeatHarness(t.Context(), update, "working"); err != nil {
			t.Fatal(err)
		}
	}
	stopping := HarnessSession{ID: session.ID, ProjectID: session.ProjectID, Lease: session.Lease, Harness: session.Harness,
		ActivitySequence: 4, Model: "model-final", ReasoningEffort: "xhigh"}
	if err := remote.HeartbeatHarness(t.Context(), stopping, "stopping"); err != nil {
		t.Fatal(err)
	}
	if session.Harness != Codex || registration["max_session_file_bytes"] != float64(rules.SessionFileLimit(Codex)) || registration["rules_client_version"] != version.Version {
		t.Fatal("registration lacks rules capability")
	}
	if _, ok := registration["model"]; ok {
		t.Fatal("unknown registration model was sent")
	}
	if _, ok := registration["reasoning_effort"]; ok {
		t.Fatal("unknown registration effort was sent")
	}
	if len(beats) != 4 || beats[0]["model"] != "model-a" || beats[0]["reasoning_effort"] != "high" || beats[1]["model"] != "model-b" {
		t.Fatalf("verified metadata missing from heartbeat: %+v", beats)
	}
	for i, beat := range beats {
		if beat["max_session_file_bytes"] != float64(rules.SessionFileLimit(Codex)) || beat["rules_client_version"] != version.Version {
			t.Fatal("heartbeat lacks rules capability")
		}
		if beat["activity_sequence"] != float64(i+1) {
			t.Fatalf("heartbeat %d lost sequence: %+v", i, beat)
		}
	}
	if _, ok := beats[1]["reasoning_effort"]; ok {
		t.Fatal("unknown effort was sent")
	}
	if beats[0]["activity"] != "busy" || beats[2]["activity"] != "idle" || beats[2]["phase"] != "working" {
		t.Fatal("idle activity changed the live phase or was lost")
	}
	if _, ok := beats[2]["model"]; ok {
		t.Fatal("routine heartbeat repeated model")
	}
	if _, ok := beats[2]["reasoning_effort"]; ok {
		t.Fatal("routine heartbeat repeated effort")
	}
	if beats[3]["phase"] != "stopping" || beats[3]["activity"] != "idle" || beats[3]["model"] != "model-final" || beats[3]["reasoning_effort"] != "xhigh" || beats[3]["process_ownership"] != nil {
		t.Fatalf("exit metadata heartbeat claimed a live process: %+v", beats[3])
	}
}

func TestRemoteUsesAeonRunAndInboxContract(t *testing.T) {
	seen := map[string]bool{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/runs/run/telemetry" && (r.Header.Get("X-Aeon-Daemon-ID") != "daemon" || r.Header.Get("X-Aeon-Daemon-Generation") != "generation") {
			t.Errorf("telemetry lacks daemon fencing headers")
		}
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/me":
			_, _ = w.Write([]byte(`{"tenant":{"id":"tenant"},"principal":{"id":"agent","tenant_id":"tenant","kind":"agent"}}`))
		case "/api/runs/queued":
			_, _ = w.Write([]byte(`[{"id":"run","agent_principal_id":"agent"}]`))
		case "/api/agent-accounts/route":
			var body struct {
				DaemonID   string   `json:"daemon_id"`
				AccountIDs []string `json:"account_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DaemonID != "daemon" || len(body.AccountIDs) != 1 || body.AccountIDs[0] != "account" {
				t.Errorf("route request omitted daemon enrollment: %+v, %v", body, err)
			}
			_, _ = w.Write([]byte(`{"account_id":"account","account_key":"local","daemon_id":"daemon","reservations":[{"reservation_id":"reservation"}]}`))
		case "/api/inbox/messages":
			_, _ = w.Write([]byte(`{"items":[],"next_after":0}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	r := NewRemote(server.URL, "scoped-key")
	ctx := context.Background()
	if tenant, principal, err := r.Identity(ctx); err != nil || tenant != "tenant" || principal != "agent" {
		t.Fatalf("identity: %s %s %v", tenant, principal, err)
	}
	if runs, err := r.Queued(ctx); err != nil || len(runs) != 1 {
		t.Fatalf("queued: %#v %v", runs, err)
	}
	if _, err := r.Route(ctx, "run", "daemon", []string{"account"}, map[string]int64{"requests": 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.Claim(ctx, "run", "daemon", "generation", []string{"reservation"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Report(ctx, "run", Telemetry{Sequence: 1, Kind: "heartbeat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Inbox(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Ack(ctx, "message"); err != nil {
		t.Fatal(err)
	}
	if err := r.Probe(ctx, "account", "daemon", "generation", true); err != nil {
		t.Fatal(err)
	}
	if err := r.AddEvidence(ctx, "order", "run", "answer"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	for _, path := range []string{"POST /api/runs/run/claim", "POST /api/runs/run/telemetry", "GET /api/inbox/messages",
		"POST /api/inbox/messages/message/ack", "POST /api/agent-accounts/account/probe", "POST /api/work-orders/order/evidence"} {
		if !seen[path] {
			t.Errorf("missing %s", path)
		}
	}
	mu.Unlock()
	if err := ValidateBaseURL("http://example.com"); err == nil {
		t.Fatal("remote cleartext accepted")
	}
	if err := ValidateBaseURL("https://example.com"); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteRunToolsUseScopedExistingRoutes(t *testing.T) {
	seen := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-key" {
			t.Error("daemon key missing from Aeon request")
		}
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		seen[r.Method+" "+r.URL.Path] = body
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/work-orders/order" {
			_, _ = w.Write([]byte(`{"node_id":"order","status":"blocked","revision":5}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	r := NewRemote(server.URL, "scoped-key")
	ctx := t.Context()
	if err := r.Comment(ctx, "order", "progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetWorkStatus(ctx, "order", 4, "blocked"); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckCriterion(ctx, "order", "criterion", true); err != nil {
		t.Fatal(err)
	}
	if err := r.Evidence(ctx, "order", "run", "criterion", "tests pass"); err != nil {
		t.Fatal(err)
	}
	if err := r.RequestApproval(ctx, "run", ApprovalRequest{Scope: "git.push", Rationale: "reason", ExpiresAt: "2026-09-26T18:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := r.ReplyInbox(ctx, "message", "sender", "answer", "reply-1"); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"POST /api/nodes/order/comments", "PATCH /api/work-orders/order", "POST /api/work-orders/order/criteria/criterion/check", "POST /api/work-orders/order/evidence", "POST /api/approvals", "POST /api/inbox/messages"} {
		if _, ok := seen[route]; !ok {
			t.Errorf("missing %s", route)
		}
	}
	if seen["POST /api/work-orders/order/evidence"]["run_id"] != "run" || seen["POST /api/work-orders/order/evidence"]["criterion_id"] != "criterion" || seen["POST /api/inbox/messages"]["reply_to_id"] != "message" || seen["POST /api/inbox/messages"]["recipient_principal_id"] != "sender" {
		t.Fatal("run or inbox binding was lost")
	}
}

func TestRemoteManagedHarnessContract(t *testing.T) {
	seen := map[string]bool{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/nodes/lookup":
			if r.URL.Query().Get("keys") != "TASK-1" {
				t.Error("work order lookup key mismatch")
			}
			_, _ = w.Write([]byte(`{"items":[{"key":"TASK-1","project_id":"project"}]}`))
		case "/api/projects/project/harness-sessions":
			var body struct {
				RunID        string `json:"run_id"`
				TicketID     string `json:"ticket_node_id"`
				Management   string `json:"management_mode"`
				Lease        string `json:"worker_lease"`
				Model        string `json:"model"`
				Effort       string `json:"reasoning_effort"`
				AccountLabel string `json:"account_label"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.RunID != "run" || body.TicketID != "order" || body.Management != "managed" || body.Lease != "private-worker-lease-32-characters-minimum" || body.Model != "model" || body.Effort != "high" || body.AccountLabel != "Work subscription" {
				t.Error("managed registration body invalid")
			}
			_, _ = w.Write([]byte(`{"id":"session","project_id":"project"}`))
		default:
			if r.Header.Get("X-Aeon-Worker-Lease") != "private-worker-lease-32-characters-minimum" {
				t.Error("worker lease header missing")
			}
			switch r.URL.Path {
			case "/api/projects/project/harness-sessions/session/yield":
				_, _ = w.Write([]byte(`{"controls":[{"id":"control","kind":"interrupt"}]}`))
			case "/api/projects/project/harness-sessions/session/drain":
				_, _ = w.Write([]byte(`[{"delivery_id":"delivery","cursor":3,"body":"hello"}]`))
			default:
				_, _ = w.Write([]byte(`{}`))
			}
		}
	}))
	defer server.Close()
	r := NewRemote(server.URL, "scoped-key")
	ctx := context.Background()
	project, err := r.ProjectForNode(ctx, "TASK-1")
	if err != nil || project != "project" {
		t.Fatalf("project lookup: %v", err)
	}
	s, err := r.RegisterHarness(ctx, HarnessSession{ID: "generation/reference", ProjectID: project, Lease: "private-worker-lease-32-characters-minimum", Model: "model", ReasoningEffort: "high", AccountLabel: "Work subscription"},
		"agent", "run", "order", Codex, "host", []string{"status", "interrupt", "stop"})
	if err != nil || s.ID != "session" {
		t.Fatalf("registration: %v", err)
	}
	if err := r.HeartbeatHarness(ctx, s, "working"); err != nil {
		t.Fatal(err)
	}
	controls, err := r.YieldHarness(ctx, s)
	if err != nil || len(controls) != 1 || controls[0].Kind != "interrupt" {
		t.Fatalf("yield: %v", err)
	}
	deliveries, err := r.DrainHarness(ctx, s)
	if err != nil || len(deliveries) != 1 || deliveries[0].Cursor != 3 {
		t.Fatalf("drain: %v", err)
	}
	if err := r.CompleteHarnessControl(ctx, s, "control", "applied", "agentd_applied"); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteHarnessDelivery(ctx, s, deliveries[0]); err != nil {
		t.Fatal(err)
	}
	if err := r.StopHarness(ctx, s, "process_exited"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"GET /api/nodes/lookup", "POST /api/projects/project/harness-sessions",
		"POST /api/projects/project/harness-sessions/session/heartbeat", "POST /api/projects/project/harness-sessions/session/yield",
		"POST /api/projects/project/harness-sessions/session/drain", "POST /api/projects/project/harness-sessions/session/controls/control/complete",
		"POST /api/projects/project/harness-sessions/session/complete-delivery", "POST /api/projects/project/harness-sessions/session/stop"} {
		if !seen[path] {
			t.Errorf("missing %s", path)
		}
	}
}

func TestRemoteTerminalControlCompletion(t *testing.T) {
	for _, tc := range []struct {
		message  string
		terminal bool
	}{
		{"divergent control completion", true},
		{"setting authorization expired", true},
		{"control must be claimed", false},
		{"process generation changed; setting outcome is fenced", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				raw, err := json.Marshal(map[string]string{"error": tc.message})
				if err != nil {
					t.Error(err)
				}
				_, _ = w.Write(raw)
			}))
			t.Cleanup(server.Close)
			remote := NewRemote(server.URL, "scoped-key")
			err := remote.CompleteHarnessControl(t.Context(), HarnessSession{ID: "session", ProjectID: "project", Lease: "private-worker-lease-32-characters-minimum"}, "control", "applied", "setting_applied")
			if tc.terminal && !errors.Is(err, ErrControlTerminal) {
				t.Fatalf("got %v", err)
			}
			if !tc.terminal && (err == nil || errors.Is(err, ErrControlTerminal)) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRemoteDeployApprovalBinding(t *testing.T) {
	const release = "55555555-5555-4555-8555-555555555555"
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/approvals" {
			t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "scoped-key")
	for _, scope := range []string{"journey.deploy", "stage.deploy"} {
		in := ApprovalRequest{Scope: scope, Rationale: "upgrade", ExpiresAt: "2999-01-01T00:00:00Z", ReleaseNodeID: release, Target: &deploytarget.Target{Environment: "production", Service: "aeon", Change: "upgrade", Image: "aeon:v2"}}
		if err := remote.RequestApproval(t.Context(), "bound-run", in); err != nil {
			t.Fatal(err)
		}
		body := bodies[len(bodies)-1]
		target, ok := body["target"].(map[string]any)
		if !ok || target["environment"] != "production" || target["image"] != "aeon:v2" || body["run_id"] != "bound-run" || body["resource_kind"] != "node" || body["resource_id"] != release || body["scope"] != scope {
			t.Fatalf("deploy approval wire binding: %+v", body)
		}
		in.Target = nil
		in.ReleaseNodeID = ""
		if err := remote.RequestApproval(t.Context(), "bound-run", in); err != nil {
			t.Fatal("targetless approval rejected", err)
		}
		legacy := bodies[len(bodies)-1]
		if legacy["resource_kind"] != "run" || legacy["resource_id"] != "bound-run" || legacy["target"] != nil {
			t.Fatalf("legacy request changed: %+v", legacy)
		}
	}
	if len(bodies) != 4 {
		t.Fatalf("unexpected requests: %d", len(bodies))
	}
}
