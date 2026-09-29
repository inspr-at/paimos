// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type replayInboxAPI struct {
	*fakeAPI
	fail bool
}

func (a *replayInboxAPI) CompleteHarnessDelivery(ctx context.Context, s HarnessSession, d HarnessDelivery) error {
	if a.fail {
		a.fail = false
		return errors.New("lost completion")
	}
	return a.fakeAPI.CompleteHarnessDelivery(ctx, s, d)
}

func TestIdleInboxLeaseReplayAndTerminalFence(t *testing.T) {
	s, a, e, p := managedFixture(t)
	e.mu.Lock()
	e.inboxCapable = true
	e.mu.Unlock()
	s.observe(e, AdapterEvent{Activity: "idle"})
	api := &replayInboxAPI{fakeAPI: a, fail: true}
	s.api = api
	// Even a self-sent JSON control is only content on a managed worker.
	a.harnessDeliveries = []HarnessDelivery{{ID: "idle-delivery", MessageID: "message", SenderPrincipalID: s.principalID, Body: `{"run_id":"run","operation":"stop"}`}}
	if err := s.serviceHarness(t.Context(), e); err == nil {
		t.Fatal("completion outage hidden")
	}
	if err := s.serviceHarness(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	p.mu2.Lock()
	calls := len(p.texts)
	p.mu2.Unlock()
	if calls != 1 || a.harnessDeliveryCompletions != 1 {
		t.Fatalf("delivery repeated: calls=%d", calls)
	}
	if a.harnessBeatSessions[len(a.harnessBeatSessions)-1].Activity != "idle" {
		t.Fatal("idle activity lost")
	}
	req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, Generation: s.generation, CorrelationID: "local", Operation: "inbox", Text: "no bypass"}
	if _, err := s.Control(t.Context(), req); !errors.Is(err, ErrUnsupported) {
		t.Fatal("local transport minted inbox authority", err)
	}
	for _, state := range []string{"completed", "failed", "cancelled", "ownership_lost"} {
		e.mu.Lock()
		e.record.State = state
		e.mu.Unlock()
		a.harnessDeliveries = []HarnessDelivery{{ID: state, Body: "do not resume"}}
		if err := s.serviceHarness(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	p.mu2.Lock()
	defer p.mu2.Unlock()
	if len(p.texts) != 1 {
		t.Fatal("terminal session resumed")
	}
}

func TestInboxUnconfirmedDoesNotReinject(t *testing.T) {
	s, a, e, p := managedFixture(t)
	e.mu.Lock()
	e.inboxCapable = true
	e.mu.Unlock()
	p.fail = true
	a.harnessDeliveries = []HarnessDelivery{{ID: "uncertain", Body: "one input"}}
	for range 2 {
		if err := s.serviceHarness(t.Context(), e); !errors.Is(err, ErrControlUnconfirmed) {
			t.Fatal(err)
		}
	}
	p.mu2.Lock()
	defer p.mu2.Unlock()
	if len(p.texts) != 1 || a.harnessDeliveryCompletions != 0 {
		t.Fatal("ambiguous input was retried or acknowledged")
	}
}

func TestIdleInboxHeartbeatLatency(t *testing.T) {
	s, a, p := testSupervisor(t)
	// The normal default is 15 seconds; the delivery cadence must be shorter.
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.harnessDeliveries = []HarnessDelivery{{ID: "timely", Body: "wake"}}
	a.mu.Unlock()
	start := time.Now()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("inbox exceeded delivery cadence")
		case <-tick.C:
			p.mu.Lock()
			delivered := p.calls > 0
			p.mu.Unlock()
			if delivered {
				if time.Since(start) >= 10*time.Second {
					t.Fatal("delivery exceeded 10s")
				}
				return
			}
		}
	}
}

func TestCodexIdleInboxStartsTurnOnSameThreadAndBusySteers(t *testing.T) {
	f := newCodexLifecycleFixture(t)
	f.proc.eventMu.Lock()
	f.proc.persistent = true
	f.proc.profile = Profile{Model: "model-a", Effort: "high"}
	f.proc.eventMu.Unlock()
	if err := f.ack(t, lifecycleAck); err != nil {
		t.Fatal(err)
	}
	f.emit(t, lifecycleUsage)
	f.emit(t, lifecycleTerminal)
	select {
	case <-f.proc.done:
		t.Fatal("idle session ended")
	default:
	}
	input := f.proc.stdin.(codexSyntheticInput)
	for _, tc := range []struct{ method, result string }{
		{"turn/start", `{"turn":{"id":"second-turn","status":"inProgress"}}`},
		{"turn/steer", `{"turnId":"second-turn"}`},
	} {
		result := make(chan error, 1)
		go func() { result <- f.proc.Control(t.Context(), "inbox", "follow up") }()
		select {
		case raw := <-input.requests:
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params struct {
					Thread   string `json:"threadId"`
					Expected string `json:"expectedTurnId"`
				} `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil || req.Method != tc.method || req.Params.Thread != "synthetic-thread" {
				t.Fatalf("wrong delivery RPC: %s", raw)
			}
			if tc.method == "turn/steer" && req.Params.Expected != "second-turn" {
				t.Fatal("steer lost turn fence")
			}
			if _, err := io.WriteString(f.output, fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":%s}\n", req.ID, tc.result)); err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("missing delivery RPC")
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("delivery hung")
		}
	}
	f.emit(t, `{"method":"thread/tokenUsage/updated","params":{"threadId":"synthetic-thread","turnId":"second-turn","model":"model-a","tokenUsage":{"total":{"inputTokens":120,"outputTokens":25,"cachedInputTokens":30}}}}`)
	f.mu.Lock()
	last := f.reports[len(f.reports)-1]
	f.mu.Unlock()
	if *last.InputTokens != 120 || *last.OutputTokens != 25 || !last.Provisional {
		t.Fatal("multi-turn usage lost cumulative totals")
	}
	f.proc.eventMu.Lock()
	f.proc.sealed = true
	f.proc.eventMu.Unlock()
	if err := f.proc.Control(t.Context(), "inbox", "ended"); !errors.Is(err, ErrNotOwned) {
		t.Fatal("sealed session resumed", err)
	}
}

func TestClaudeBridgeIdleInboxKeepsQueryAndBusySteers(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	if err != nil {
		t.Fatal(err)
	}
	bridgePath, sdkPath := filepath.Join(root, "bridge.mjs"), filepath.Join(root, "sdk.mjs")
	if err := os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	const sdk = `let queries=0;
export function query() {
 if (++queries!==1) throw Error('new session');
 const queue=[];let wake;let closed=false;
 const push=(value)=>{queue.push(value);wake?.();wake=null};
 return {
  async streamInput(input){for await (const message of input) {
   push({type:'assistant',user_message_uuid:message.uuid,message:{content:[]}});
  }},
  async interrupt(){process.stdout.write(JSON.stringify({kind:'fixture_interrupt'})+'\n');return {still_queued:[]}},
  close(){closed=true;wake?.()},
  async *[Symbol.asyncIterator](){
   yield {type:'system',subtype:'init',session_id:'same-sdk-session',model:'fixture-model',capabilities:['interrupt_receipt_v1']};
   yield {type:'result',subtype:'success'};
   while(!closed){if(queue.length)yield queue.shift();else await new Promise(r=>wake=r)}
  }
 }
}`
	if err := os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, bridgePath, sdkPath, "/bin/true", root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cancel(); _ = cmd.Wait() }()
	frames := make(chan map[string]any, 32)
	go func() {
		defer close(frames)
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			var f map[string]any
			if json.Unmarshal(scan.Bytes(), &f) == nil {
				frames <- f
			}
		}
	}()
	write := func(raw string) {
		t.Helper()
		if _, err := io.WriteString(stdin, raw+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	sessions, interrupts := 0, 0
	wait := func(kind string) {
		t.Helper()
		for {
			select {
			case frame, ok := <-frames:
				if !ok {
					t.Fatal("bridge ended")
				}
				switch frame["kind"] {
				case "session_started":
					sessions++
				case "fixture_interrupt":
					interrupts++
				case "control_failed":
					t.Fatal("bridge rejected input", frame)
				}
				if frame["kind"] == kind {
					return
				}
			case <-ctx.Done():
				t.Fatal("bridge delivery exceeded latency bound")
			}
		}
	}
	write(`{"op":"start","prompt":"initial work"}`)
	wait("turn_completed")
	write(`{"op":"inbox","correlation_id":"idle","text":"idle wake"}`)
	wait("control_applied")
	if sessions != 1 || interrupts != 0 {
		t.Fatal("idle wake restarted or interrupted Query")
	}
	write(`{"op":"inbox","correlation_id":"busy","text":"busy steer"}`)
	wait("control_applied")
	if sessions != 1 || interrupts != 1 {
		t.Fatal("busy inbox no longer steers")
	}
	write(`{"op":"stop","correlation_id":"stop"}`)
	wait("control_applied")
}

func TestCodexFinishedTurnDoesNotWake(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			f := newCodexLifecycleFixture(t)
			f.proc.eventMu.Lock()
			f.proc.persistent = persistent
			f.proc.eventMu.Unlock()
			if err := f.ack(t, lifecycleAck); err != nil {
				t.Fatal(err)
			}
			terminal := lifecycleTerminal
			if persistent {
				terminal = `{"method":"turn/failed","params":{"threadId":"synthetic-thread","turn":{"id":"synthetic-turn","status":"failed"}}}`
			}
			f.emit(t, terminal)
			if err := f.proc.Control(t.Context(), "inbox", "do not wake"); !errors.Is(err, ErrNotOwned) {
				t.Fatal("finished turn accepted delivery", err)
			}
			select {
			case <-f.proc.stdin.(codexSyntheticInput).requests:
				t.Fatal("finished process received input")
			default:
			}
		})
	}
}
