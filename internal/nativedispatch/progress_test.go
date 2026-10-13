// SPDX-License-Identifier: AGPL-3.0-only

package nativedispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func planFrame(provider, first, second string) []byte {
	if provider == "claude" {
		return []byte(`{"type":"assistant","session_id":"native-conversation","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","name":"TodoWrite","id":"tool-1","input":{"todos":[{"content":"Build","activeForm":"Building","status":"` + first + `"},{"content":"Check","status":"` + second + `"}]}}]}}`)
	}
	if first == "in_progress" {
		first = "inProgress"
	}
	return []byte(`{"method":"turn/plan/updated","params":{"threadId":"native-thread","turnId":"native-turn","explanation":"Private explanation is not exported","plan":[{"step":"Build","status":"` + first + `"},{"step":"Check","status":"` + second + `"}]}}`)
}

// Risk: protocol plans or agent tool arguments can fabricate target identity,
// refresh stale estimates, or claim delivery to the server before a heartbeat.
func TestNativeProgressPlansToolAndHeartbeat(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			b := testBinding()
			identity := nativecontract.NativeIdentity{Provider: provider, NativeSessionRef: "native-conversation", NativeThreadRef: "native-thread", NativeTurnRef: "native-turn", RuntimeEpoch: b.RuntimeEpoch, ProcessIdentityDigest: strings.Repeat("a", 64)}
			lifetime, stop := context.WithCancel(t.Context())
			defer stop()
			p, err := NewProgress(b, identity, lifetime, now.Add(24*time.Hour), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if err = p.ObservePlan(b, identity, 1, planFrame(provider, "in_progress", "pending")); err != nil {
				t.Fatal(err)
			}
			initial := p.Heartbeat()
			if initial.ProgressPct == nil || *initial.ProgressPct != 0 || initial.ETAReadyAt != nil {
				t.Fatal("unobserved task rate invented an ETA", initial)
			}
			now = now.Add(4 * time.Minute)
			if err = p.ObservePlan(b, identity, 2, planFrame(provider, "completed", "in_progress")); err != nil {
				t.Fatal(err)
			}
			derived := p.Heartbeat()
			if derived.ProgressPct == nil || *derived.ProgressPct != 50 || derived.ETAReadyAt == nil || !derived.ETAReadyAt.Equal(now.Add(4*time.Minute)) {
				t.Fatal("protocol completion/rate were not derived", derived)
			}
			raw, _ := json.Marshal(derived)
			if strings.Contains(string(raw), "Private") || strings.Contains(string(raw), "Build") || strings.Contains(string(raw), "native-thread") {
				t.Fatal("private plan contents/identity leaked", string(raw))
			}
			wrong := b
			wrong.RuntimeEpoch = parentID
			requireReason(t, p.ObservePlan(wrong, identity, 3, planFrame(provider, "completed", "completed")), nativecontract.ReasonWrongGeneration)
			foreign := identity
			foreign.ProcessIdentityDigest = strings.Repeat("b", 64)
			requireReason(t, p.ObservePlan(b, foreign, 3, planFrame(provider, "completed", "completed")), nativecontract.ReasonIdentityMismatch)
			badSession := strings.ReplaceAll(string(planFrame(provider, "completed", "pending")), "native-conversation", "foreign-conversation")
			if provider == "codex" {
				badSession = strings.ReplaceAll(badSession, "native-thread", "foreign-thread")
			}
			requireReason(t, p.ObservePlan(b, identity, 3, []byte(badSession)), nativecontract.ReasonIdentityMismatch)
			requireReason(t, p.ObservePlan(b, identity, 2, planFrame(provider, "completed", "completed")), nativecontract.ReasonStaleRevision)
			if provider == "claude" {
				subagent := strings.Replace(string(planFrame(provider, "completed", "pending")), `"parent_tool_use_id":null`, `"parent_tool_use_id":"child-tool"`, 1)
				requireReason(t, p.ObservePlan(b, identity, 3, []byte(subagent)), nativecontract.ReasonIdentityMismatch)
			} else {
				next := identity
				next.NativeTurnRef = "next-turn"
				nextPlan := strings.ReplaceAll(string(planFrame(provider, "completed", "in_progress")), "native-turn", "next-turn")
				if err = p.ObservePlan(b, next, 3, []byte(nextPlan)); err != nil {
					t.Fatal("qualified source could not advance turn", err)
				}
			}
			// Exercise the actual MCP schema and transport, not just a direct Go
			// helper. Only the daemon closure chooses this session's binding.
			server := mcp.NewServer(&mcp.Implementation{Name: "owned-worker-test", Version: "1"}, nil)
			p.AddTool(server)
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			serverSession, err := server.Connect(t.Context(), serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer serverSession.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "progress-test", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
			if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "report_progress" {
				t.Fatal("progress tool absent", tools, err)
			}
			schema, _ := json.Marshal(tools.Tools[0].InputSchema)
			for _, forbidden := range []string{"binding", "session_id", "credential", "token", "tenant"} {
				if strings.Contains(string(schema), forbidden) {
					t.Fatal("tool accepts target or credential", string(schema))
				}
			}
			for _, args := range []any{
				map[string]any{"pct": 101, "remaining_min": 12, "note": "invalid percent"},
				map[string]any{"pct": 20, "remaining_min": -1, "note": "invalid ETA"},
				map[string]any{"pct": 20, "remaining_min": 524161, "note": "invalid ETA ceiling"},
				map[string]any{"pct": 20, "note": "missing ETA"},
				map[string]any{"pct": 20, "remaining_min": 12, "note": "valid", "session_id": parentID},
			} {
				result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "report_progress", Arguments: args})
				if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, string(nativecontract.ReasonInvalidInput)) {
					t.Fatal("tool accepted malformed input or wrong failure", result, err)
				}
			}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "report_progress", Arguments: ProgressReport{37, 12.5, "Checks in progress"}})
			if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "accepted locally") {
				t.Fatal("valid local report rejected or overclaimed", result, err)
			}
			beat := p.Heartbeat()
			if beat.ProgressPct == nil || *beat.ProgressPct != 37 || beat.ETAReadyAt == nil || !beat.ETAReadyAt.Equal(now.Add(12*time.Minute+30*time.Second)) || beat.ActivityNote == nil || *beat.ActivityNote != "Checks in progress" {
				t.Fatal("heartbeat lost explicit report", beat)
			}
			eta := *beat.ETAReadyAt
			now = now.Add(2 * time.Minute)
			if !p.Heartbeat().ETAReadyAt.Equal(eta) {
				t.Fatal("heartbeat moved ETA without a new report")
			}
			requireReason(t, p.Report(wrong, []byte(`{"pct":10,"remaining_min":3,"note":"wrong generation"}`)), nativecontract.ReasonWrongGeneration)
			requireReason(t, p.Report(b, []byte(`{"pct":10,"pct":80,"remaining_min":3,"note":"duplicate key"}`)), nativecontract.ReasonInvalidInput)
			requireReason(t, p.Report(b, []byte(strings.Repeat(" ", 4097))), nativecontract.ReasonBufferFull)
			now = now.Add(8 * time.Minute)
			if beat := p.Heartbeat(); beat.ProgressPct != nil || beat.ETAReadyAt != nil {
				t.Fatal("stale source was repeatedly reported as fresh", beat)
			}
			stop()
			result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "report_progress", Arguments: ProgressReport{100, 0, "Closed runtime"}})
			if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, string(nativecontract.ReasonOwnershipUnconfirmed)) {
				t.Fatal("old runtime tool still reported", result, err)
			}
		})
	}
}

// Risk: repeated cached heartbeats, scouts or legacy sessions can suppress or
// misroute attention intended for an owned build/ship child after ten minutes.
func TestOwnedWorkerMissingProgressAttentionClock(t *testing.T) {
	started := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	state := AttentionState{Binding: testBinding(), ParentHarnessSessionID: parentID, Role: "worker", WorkShape: "ship", Owned: true, StartedAt: started}
	if MissingProgressAttention(state, started.Add(10*time.Minute-time.Nanosecond)) != nil {
		t.Fatal("attention fired before grace")
	}
	attention := MissingProgressAttention(state, started.Add(10*time.Minute))
	if attention == nil || attention.ParentHarnessSessionID != parentID || attention.ChildHarnessSessionID != childID || attention.Code != "native_missing_progress_eta" || !attention.MissingProgress || !attention.MissingETA {
		t.Fatal("attention did not target the parent coordinator", attention)
	}
	pct := 0
	eta := started.Add(time.Hour)
	state.ProgressPct, state.ETAReadyAt = &pct, &eta
	state.ProgressReportedAt, state.ETAReportedAt = started.Add(9*time.Minute), started.Add(9*time.Minute)
	if MissingProgressAttention(state, started.Add(10*time.Minute)) != nil {
		t.Fatal("fresh zero progress with ETA was treated as missing")
	}
	state.ETAReadyAt = nil
	attention = MissingProgressAttention(state, started.Add(10*time.Minute))
	if attention == nil || attention.MissingProgress || !attention.MissingETA {
		t.Fatal("partial report suppressed attention", attention)
	}
	state.ETAReadyAt = &eta
	if MissingProgressAttention(state, started.Add(19*time.Minute)) == nil {
		t.Fatal("stale progress/ETA suppressed attention")
	}
	for _, excluded := range []string{"legacy", "scout", "coordinator", "stopped", "no parent", "parent is child"} {
		copy := state
		switch excluded {
		case "legacy":
			copy.Owned = false
		case "scout":
			copy.WorkShape = "scout"
		case "coordinator":
			copy.Role = "coordinator"
		case "stopped":
			copy.Stopped = true
		case "no parent":
			copy.ParentHarnessSessionID = ""
		case "parent is child":
			copy.ParentHarnessSessionID = childID
		}
		if MissingProgressAttention(copy, started.Add(time.Hour)) != nil {
			t.Fatal("attention included", excluded)
		}
	}
}
