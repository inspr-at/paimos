// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/rules"
)

// No vendor executable or process signal: ownership and outcomes are fixtures.
type managedFake struct {
	*fakeProcess
	identity ownedprocess.Identity
	observe  func(AdapterEvent)
	fail     bool
	texts    []string
	mu2      sync.Mutex
}

func (p *managedFake) Ownership() (ownedprocess.Identity, error) { return p.identity, nil }
func (p *managedFake) ForceStop(context.Context, ownedprocess.Identity, time.Time) error {
	return ErrUnsupported
}
func (p *managedFake) Control(_ context.Context, op, text string) error {
	p.mu2.Lock()
	p.texts = append(p.texts, text)
	p.mu2.Unlock()
	if p.observe != nil {
		p.observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
	}
	if p.fail {
		return errors.New("fixture outcome lost")
	}
	return nil
}
func managedFixture(t *testing.T) (*Supervisor, *fakeAPI, *owned, *managedFake) {
	t.Helper()
	s, a, p := testSupervisor(t)
	if err := s.StartRun(t.Context(), a.run); err != nil {
		t.Fatal(err)
	}
	e := s.runs[a.run.ID]
	proc := &managedFake{fakeProcess: p, identity: ownedprocess.Identity{ProcessID: strings.Repeat("b", 32), RootPID: 3456, GroupID: 3456, StartedAt: time.Now().UTC()}}
	e.mu.Lock()
	e.process = proc
	e.managedPolicy = true
	e.inboxCapable = false
	e.mu.Unlock()
	e.budgetMu.Lock()
	e.budgetProcess = proc
	e.tokenBudget, e.turnBudget = defaultTokenBudget, defaultTurnBudget
	e.budgetMu.Unlock()
	return s, a, e, proc
}
func TestManagedControlFencesAndNoReinjection(t *testing.T) {
	s, a, e, p := managedFixture(t)
	identity := p.identity
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, Generation: s.generation, CorrelationID: "managed-1", Operation: "steer", Text: "private steer", ExpectedOwnership: &identity, ExpiresAt: &expiry}
	if _, err := s.Control(t.Context(), req); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local bypass: %v", err)
	}
	wrong := identity
	wrong.ProcessID = strings.Repeat("c", 32)
	req.ExpectedOwnership = &wrong
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("wrong identity: %v", err)
	}
	req.ExpectedOwnership = &identity
	expired := time.Now().Add(-time.Second)
	req.ExpiresAt = &expired
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrControlExpired) {
		t.Fatalf("expired: %v", err)
	}
	req.ExpiresAt = &expiry
	// Synchronous usage callbacks used to deadlock under entry.mu.
	p.observe = func(ev AdapterEvent) { s.observe(e, ev) }
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.control(ctx, req, true); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("control callback deadlock")
	}
	if _, err := s.control(t.Context(), req, true); err != nil {
		t.Fatal(err)
	}
	req.Text = "different"
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrReplay) {
		t.Fatal(err)
	}
	req.CorrelationID = "managed-2"
	p.fail = true
	if _, err := s.control(t.Context(), req, true); err == nil {
		t.Fatal("missing ambiguous failure")
	}
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatal(err)
	}
	p.mu2.Lock()
	count := len(p.texts)
	p.mu2.Unlock()
	if count != 2 {
		t.Fatalf("reinjection: %d", count)
	}
	raw, err := os.ReadFile(s.journal.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private steer") || strings.Contains(string(raw), "different") {
		t.Fatal("journal stored steer text")
	}
}
func TestManagedBudgetStopsWithoutTelemetryLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ev     AdapterEvent
		reason string
	}{
		{"tokens", AdapterEvent{Kind: "usage", InputTokensDelta: 90, OutputTokensDelta: 10}, "token_budget_exhausted"},
		{"turns", AdapterEvent{BudgetTurnsDelta: 2}, "turn_budget_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, e, p := managedFixture(t)
			e.budgetMu.Lock()
			e.tokenBudget = 100
			e.turnBudget = 2
			e.budgetMu.Unlock()
			// Simulate telemetry holding its mutex; budget enforcement must still close.
			e.mu.Lock()
			s.observeBudget(e, tc.ev)
			select {
			case <-p.stopped:
			case <-time.After(time.Second):
				e.mu.Unlock()
				t.Fatal("budget waited for telemetry")
			}
			e.mu.Unlock()
			awaitTelemetryMonitor(t, e)
			if e.budgetStopReason() != tc.reason {
				t.Fatal(e.budgetStopReason())
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			if len(a.harnessStops) == 0 || a.harnessStops[len(a.harnessStops)-1] != tc.reason {
				t.Fatal(a.harnessStops)
			}
			if s.Status()[0].BudgetStopReason != tc.reason {
				t.Fatal("budget reason not retained")
			}
		})
	}
}
func TestManagedControlCompletionRetryDoesNotRepeatAdapter(t *testing.T) {
	s, a, e, p := managedFixture(t)
	identity := p.identity
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	a.harnessCompletionFailures = 1
	a.harnessControls = []HarnessControl{{ID: "queue-control", Kind: "steer", Text: "queue fixture", ExpectedOwnership: &identity, ExpiresAt: &expiry}}
	if err := s.serviceHarness(t.Context(), e); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatalf("missing completion uncertainty: %v", err)
	}
	if err := s.serviceHarness(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	p.mu2.Lock()
	defer p.mu2.Unlock()
	if len(p.texts) != 1 {
		t.Fatal("completion retry reinjected control")
	}
	if len(a.harnessCompletions) != 1 || a.harnessCompletions[0] != "queue-control:applied:queued_next_turn" {
		t.Fatal(a.harnessCompletions)
	}
}

type managedRulesFixture struct {
	*fakeAPI
	bundle   rules.Merged
	receipts int
}

func (a *managedRulesFixture) ManagedContext(context.Context, HarnessSession) (rules.Merged, error) {
	return a.bundle, nil
}
func (a *managedRulesFixture) RecordManagedRules(context.Context, HarnessSession, rules.Merged) error {
	a.receipts++
	return nil
}
func TestManagedRulesValidateExactEphemeralContext(t *testing.T) {
	id := func(n string) string { return "00000000-0000-4000-8000-00000000000" + n }
	c := rules.Context{TenantID: id("1"), ProjectID: id("2"), PersonID: id("3"), AgentID: id("4"), Role: "builder", Harness: "claude-code", TaskID: id("5")}
	snap := rules.Snapshot{SetID: id("6"), Scope: rules.Scope{Layer: "company"}, Name: "Safety", Version: "260929000000.0.0", Rules: []rules.Rule{{Identity: "safe", Text: "Keep credentials private.", Why: "Safety.", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "AEON-260"}}}}
	snap.SHA256 = rules.SnapshotDigest(snap)
	merged, err := rules.Merge(c, []rules.Snapshot{snap}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	api := &managedRulesFixture{bundle: merged}
	s := &Supervisor{api: api, tenantID: c.TenantID, principalID: c.AgentID}
	e := &owned{harness: HarnessSession{ID: id("7"), ProjectID: c.ProjectID}}
	run := Run{WorkOrderID: c.TaskID}
	profile := Profile{Harness: Claude}
	body, err := s.managedRules(t.Context(), e, run, profile)
	if err != nil || body != merged.Body || api.receipts != 1 {
		t.Fatalf("rules handoff: %v", err)
	}
	api.bundle.Context.ProjectID = id("8")
	if _, err = s.managedRules(t.Context(), e, run, profile); err == nil {
		t.Fatal("cross-project rules accepted")
	}
	api.bundle = merged
	api.bundle.Body = "changed bytes"
	if _, err = s.managedRules(t.Context(), e, run, profile); err == nil {
		t.Fatal("changed rules accepted")
	}
	if api.receipts != 1 {
		t.Fatal("invalid rules receipted")
	}
}

func TestClaudeBridgeEphemeralRulesBudgetAndNativeClose(t *testing.T) {
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
	if err = os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	const sdk = `export function query({options}) {
 if(options.systemPrompt.append !== 'Ephemeral safety fixture' || options.maxTurns !== 3 || options.persistSession !== false || options.settingSources.length || options.plugins.length || !options.strictMcpConfig || options.tools.includes('Bash')) throw Error('unsafe options');
 let resolveClosed; const closed=new Promise(r=>resolveClosed=r);
 return {streamInput:async()=>{},interrupt:async()=>({still_queued:[]}),close:()=>resolveClosed(),async *[Symbol.asyncIterator](){
 yield {type:'system',subtype:'init',session_id:'fixture',model:'fixture-model',capabilities:['interrupt_receipt_v1']};await closed;
 }};
}`
	if err = os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, result, reason string }{
		{"native_close", "", ""},
		{"tokens", `yield {type:'result',subtype:'success',modelUsage:{fixture:{inputTokens:90,outputTokens:10}}};`, "token_budget_exhausted"},
		{"sdk_turns", `yield {type:'result',subtype:'error_max_turns'};`, "turn_budget_exhausted"},
		{"completed_turns", `for(let i=0;i<3;i++) yield {type:'result',subtype:'success'};`, "turn_budget_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := strings.Replace(sdk, "await closed;", tc.result+"await closed;", 1)
			if err := os.WriteFile(sdkPath, []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(node, bridgePath, sdkPath, "/bin/true", root)
			input := `{"op":"start","prompt":"fixture","rules":"Ephemeral safety fixture","max_turns":3,"max_tokens":100}` + "\n"
			if tc.reason == "" {
				input += `{"op":"stop","correlation_id":"close-fixture"}` + "\n"
			}
			cmd.Stdin = strings.NewReader(input)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("bridge: %v (%s)", err, out)
			}
			if strings.Contains(string(out), "Ephemeral safety fixture") {
				t.Fatal("leaked rules")
			}
			if tc.reason == "" && !strings.Contains(string(out), `"kind":"control_applied"`) {
				t.Fatal("missing close receipt")
			}
			if tc.reason != "" && !strings.Contains(string(out), `"reason":"`+tc.reason+`"`) {
				t.Fatalf("missing budget receipt: %s", out)
			}
		})
	}

	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("instruction file written")
		}
	}
}

type managedFixtureWriter func([]byte) (int, error)

func (w managedFixtureWriter) Write(b []byte) (int, error) { return w(b) }
func (w managedFixtureWriter) Close() error                { return nil }
func TestClaudeNativeClosePreservesReceiptAtEOF(t *testing.T) {
	for i := 0; i < 100; i++ {
		p := &claudeProcess{wireProcess: &wireProcess{readDone: make(chan struct{}), waitDone: make(chan struct{})}, controls: map[string]chan bool{}}
		p.stdin = managedFixtureWriter(func(b []byte) (int, error) {
			var frame struct {
				ID string `json:"correlation_id"`
			}
			if err := json.Unmarshal(b, &frame); err != nil {
				return 0, err
			}
			p.controls[frame.ID] <- true
			close(p.readDone)
			close(p.waitDone)
			return len(b), nil
		})
		if err := p.GracefulStop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
