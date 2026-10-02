// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedSettingsFencedReceiptsAndHeartbeat(t *testing.T) {
	s, a, e, p := managedFixture(t)
	identity := p.identity
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, Generation: s.generation, CorrelationID: "setting-1", Operation: "model", Value: "changed-model", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry}
	if _, err := s.Control(t.Context(), req); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local bypass: %v", err)
	}
	e.managedPolicy = false
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unqualified: %v", err)
	}
	e.managedPolicy = true
	identity.Generation = "stale"
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrGeneration) {
		t.Fatalf("generation: %v", err)
	}
	identity.Generation = s.generation
	p.observe = func(AdapterEvent) { s.observe(e, AdapterEvent{HarnessModel: "changed-model", HarnessEffort: "low"}) }
	if _, err := s.control(t.Context(), req, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.control(t.Context(), req, true); err != nil {
		t.Fatal(err)
	}
	if len(p.texts) != 1 {
		t.Fatal("setting replayed")
	}
	if err := s.heartbeatHarnessPhase(t.Context(), e, "working"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	beats := append([]HarnessSession(nil), a.harnessBeatSessions...)
	a.mu.Unlock()
	found := false
	for _, b := range beats {
		if b.Model == "changed-model" && b.ReasoningEffort == "low" {
			found = true
		}
	}
	if !found {
		t.Fatal("applied settings missing from heartbeat")
	}
	p.fail = true
	req.CorrelationID = "setting-2"
	req.Operation = "effort"
	req.Value = "high"
	if _, err := s.control(t.Context(), req, true); err == nil {
		t.Fatal("failure hidden")
	}
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatal(err)
	}
}

func TestClaudeBridgeSettingsUseOfficialSetters(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge, _ := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	bridgePath, sdkPath := filepath.Join(root, "bridge.mjs"), filepath.Join(root, "sdk.mjs")
	if err = os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	sdk := `export function query(){
 let close;const closed=new Promise(r=>close=r);
 return {close,streamInput:async()=>{},interrupt:async()=>({still_queued:[]}),
 supportedModels:async()=>[{value:'fixture-model',supportsEffort:true,supportedEffortLevels:['low','high']},{value:'other-model',supportsEffort:true,supportedEffortLevels:['low','high']}],
 setModel:async value=>{if(value!=='other-model')throw Error('rejected')},
 applyFlagSettings:async settings=>{if(JSON.stringify(settings)!=='{"effortLevel":"low"}')throw Error('rejected')},
 async *[Symbol.asyncIterator](){yield {type:'system',subtype:'init',session_id:'fixture',model:'fixture-model',capabilities:['interrupt_receipt_v1']};await closed;}}
 }`
	if err = os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, bridgePath, sdkPath, "/bin/true", root)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = cmd.Wait() }()
	enc := json.NewEncoder(in)
	scan := bufio.NewScanner(out)
	_ = enc.Encode(map[string]any{"op": "start", "prompt": "fixture", "model": "fixture-model", "effort": "high"})
	for scan.Scan() {
		if strings.Contains(scan.Text(), `"kind":"session_started"`) {
			break
		}
	}
	for _, tc := range []struct{ op, value, outcome string }{{"model", "other-model", "control_applied"}, {"effort", "low", "control_applied"}, {"effort", "high", "control_failed"}, {"model", "not-in-vendor-catalog", "control_failed"}, {"effort", "ultra", "control_failed"}} {
		id := tc.op + tc.value
		_ = enc.Encode(map[string]string{"op": tc.op, "value": tc.value, "correlation_id": id})
		changed := false
		found := false
		for scan.Scan() {
			var frame map[string]any
			_ = json.Unmarshal(scan.Bytes(), &frame)
			if frame["kind"] == "settings_changed" {
				changed = true
			}
			if frame["correlation_id"] == id {
				if frame["kind"] != tc.outcome {
					t.Fatalf("%s: %s", id, scan.Text())
				}
				found = true
				break
			}
		}
		if !found || changed != (tc.outcome == "control_applied") {
			t.Fatalf("%s: receipt or telemetry mismatch", id)
		}
	}
	_ = enc.Encode(map[string]string{"op": "stop", "correlation_id": "stop"})
}

// A lost completion response must replay the same rejection, without a second
// vendor call or changing a definite rejection into an uncertain outcome.
type rejectingSettingProcess struct {
	*managedFake
	calls int
}

func (p *rejectingSettingProcess) Control(context.Context, string, string) error {
	p.calls++
	return ErrSettingRejected
}
func TestManagedSettingRejectionCompletionRetry(t *testing.T) {
	s, a, e, p := managedFixture(t)
	rejecting := &rejectingSettingProcess{managedFake: p}
	// monitor reads entry.process under entry.mu (Supervisor.monitor).
	e.mu.Lock()
	e.process = rejecting
	e.mu.Unlock()
	identity := p.identity
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	a.harnessCompletionFailures = 1
	a.harnessControls = []HarnessControl{{ID: "rejected-setting", Kind: "model", Value: "fixture-model", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry}}
	if err := s.serviceHarness(t.Context(), e); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatal(err)
	}
	if err := s.serviceHarness(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if rejecting.calls != 1 || len(a.harnessCompletions) != 1 || a.harnessCompletions[0] != "rejected-setting:rejected:setting_rejected" {
		t.Fatalf("calls=%d completions=%v", rejecting.calls, a.harnessCompletions)
	}
}

func TestClaudeTierConfirmsFreshVendorState(t *testing.T) {
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
	sdk := `export function query({options}){
 if(JSON.parse(options.extraArgs.settings).fastMode!==false)throw Error('inherited paid tier');
 let close,mode='off',count=0;const closed=new Promise(r=>close=r);
 return {close,streamInput:async()=>{},interrupt:async()=>({still_queued:[]}),
 applyFlagSettings:async settings=>{mode=settings.fastMode?'on':'off';count++},
 reinitialize:async()=>({fast_mode_state:count===3?'cooldown':mode}),
 async *[Symbol.asyncIterator](){yield {type:'system',subtype:'init',session_id:'fixture',model:'claude-opus-5-5',capabilities:['interrupt_receipt_v1']};await closed;}}
 }`
	if err = os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, bridgePath, sdkPath, "/bin/true", root)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = cmd.Wait() }()
	enc, scan := json.NewEncoder(in), bufio.NewScanner(out)
	_ = enc.Encode(map[string]any{"op": "start", "prompt": "fixture", "model": "claude-opus-5-5", "effort": "high"})
	started := false
	for scan.Scan() {
		if strings.Contains(scan.Text(), `"kind":"session_started"`) {
			started = true
			break
		}
	}
	if !started {
		t.Fatal("bridge did not initialize")
	}
	for i, tc := range []struct{ tier, outcome string }{{"fast", "control_applied"}, {"default", "control_applied"}, {"fast", "control_failed"}} {
		id := string(rune('a' + i))
		_ = enc.Encode(map[string]string{"op": "tier", "value": tc.tier, "correlation_id": id})
		changed, found := false, false
		for scan.Scan() {
			var frame map[string]any
			_ = json.Unmarshal(scan.Bytes(), &frame)
			if frame["kind"] == "settings_changed" {
				changed = frame["service_tier"] == tc.tier
			}
			if frame["correlation_id"] == id {
				if frame["kind"] != tc.outcome {
					t.Fatal(scan.Text())
				}
				found = true
				break
			}
		}
		if !found || changed != (tc.outcome == "control_applied") {
			t.Fatalf("vendor state not enforced for %s", tc.tier)
		}
	}
	_ = enc.Encode(map[string]string{"op": "stop", "correlation_id": "stop"})
}
