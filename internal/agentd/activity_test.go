// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentactivity"
)

func TestActivityHeartbeatHonorsReturnedPolicy(t *testing.T) {
	modes := []string{agentactivity.Summary, agentactivity.Tool, agentactivity.Off, agentactivity.Off}
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]string{"agent_activity_mode": modes[len(bodies)-1]})
	}))
	defer srv.Close()
	remote := NewRemote(srv.URL, "fixture-key")
	s := HarnessSession{ID: "session", ProjectID: "project", Lease: "fixture-lease", Harness: Codex, Doing: "Implementing activity", DoingAt: time.Now().UTC(), ToolActivity: &agentactivity.Activity{Text: "Running Go tests", Source: "auto", At: time.Now().UTC()}}
	for i := 0; i < 4; i++ {
		if err := remote.HeartbeatHarness(t.Context(), s, "working"); err != nil {
			t.Fatal(err)
		}
	}
	if bodies[0]["doing"] != nil || bodies[1]["doing"] != "Implementing activity" || bodies[2]["doing"] != nil || bodies[3]["doing"] != nil || bodies[3]["tool_activity"] != nil {
		t.Fatal("reporter ignored workspace policy")
	}
	if remote.SessionActivityMode("session") != agentactivity.Off {
		t.Fatal("mode not retained")
	}
}

func TestCodexToolActivityIsOwnedAndSanitized(t *testing.T) {
	var events []AdapterEvent
	p := &codexProcess{wireProcess: &wireProcess{threadID: "owned", turnID: "turn", observe: func(ev AdapterEvent) { events = append(events, ev) }}}
	for _, thread := range []string{"foreign", "owned"} {
		raw, _ := json.Marshal(map[string]any{"method": "item/started", "params": map[string]any{"threadId": thread, "turnId": "turn", "item": map[string]any{"type": "commandExecution", "command": "go test ./... -args PRIVATE_ARGUMENT"}}})
		p.notification(raw)
	}
	if len(events) != 2 || events[0].ToolActivity != nil || events[1].ToolActivity == nil || events[1].ToolActivity.Text != "Running Go tests" {
		t.Fatal("unowned or unsanitized activity projected")
	}
}
