// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/client"
)

type capacityTestAPI struct {
	API
	mu      sync.Mutex
	got     []capacity.Reading
	account string
	fail    bool
}

func (a *capacityTestAPI) ReportCapacity(_ context.Context, id string, rs []capacity.Reading) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return fmt.Errorf("synthetic unavailable")
	}
	a.account = id
	a.got = append(a.got, rs...)
	return nil
}

func TestManagedCapacityOutboxAndGenerationFence(t *testing.T) {
	api := &capacityTestAPI{}
	s := &Supervisor{api: api, generation: "g1"}
	e := &owned{record: Record{Generation: "g1", AccountID: "account", RunID: "run"}}
	now := time.Now().UTC()
	r := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 10, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "harness", Phase: "start"}
	s.observeCapacity(e, []capacity.Reading{r})
	r.UsedPercent = 12
	r.ReadAt = now.Add(time.Millisecond)
	r.Phase = "end"
	s.observeCapacity(e, []capacity.Reading{r})
	api.fail = true
	s.flushCapacity(t.Context(), e)
	if len(e.capacityPending) != 2 {
		t.Fatal("failed report discarded")
	}
	api.fail = false
	s.flushCapacity(t.Context(), e)
	if len(api.got) != 2 || api.account != "account" || len(e.capacityPending) != 0 {
		t.Fatal("missing start/end report")
	}
	for _, r := range api.got {
		if r.RunID != "run" {
			t.Fatal("wrong run attribution")
		}
	}
	e.harnessArchived = true
	s.observeCapacity(e, []capacity.Reading{r})
	s.flushCapacity(t.Context(), e)
	if len(api.got) != 2 {
		t.Fatal("archived reporter wrote")
	}
	e.harnessArchived = false
	e.record.Generation = "old"
	s.observeCapacity(e, []capacity.Reading{r})
	s.flushCapacity(t.Context(), e)
	if len(api.got) != 2 {
		t.Fatal("stale generation wrote")
	}
}
func TestCodexCapacityNotification(t *testing.T) {
	var got []capacity.Reading
	p := &codexProcess{wireProcess: &wireProcess{observe: func(e AdapterEvent) { got = append(got, e.Capacity...) }}}
	frame := fmt.Sprintf(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":21,"windowDurationMins":300,"resetsAt":%d}},"ordinaryUsageAllowed":false}}`, time.Now().Add(time.Hour).Unix())
	p.notification(json.RawMessage(frame))
	if len(got) != 1 || got[0].Phase != "update" || got[0].UsedPercent != 21 || got[0].OrdinaryUsageAllowed == nil || *got[0].OrdinaryUsageAllowed {
		t.Fatal(got)
	}
	p.notification(json.RawMessage(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":22}}}}`))
	if len(got) != 2 || *got[1].OrdinaryUsageAllowed {
		t.Fatal("sparse update lost denial")
	}
}
func TestCodexQuotaNeutralFallbackUsesOwnedFakeCLI(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCodexAdapter(fakeVendorPath(t, "codex_capacity"), map[string]string{"key": home})
	a.SetExpectedEmails(map[string]string{"key": "agent@example.test"})
	got := a.CaptureCapacity(t.Context(), "key")
	if len(got) != 1 || got[0].Source != "agentd" || got[0].UsedPercent != 31 || got[0].WindowKind != "weekly" {
		t.Fatal(got)
	}
	a.SetExpectedEmails(map[string]string{"key": "wrong@example.test"})
	if got := a.CaptureCapacity(t.Context(), "key"); len(got) != 0 {
		t.Fatal("mismatched identity reported quota")
	}
}

func TestManagedCodexCapacityAtStartAndEnd(t *testing.T) {
	req := adapterRequest(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCodexAdapter(fakeVendorPath(t, "codex_capacity"), map[string]string{"account": home})
	a.SetExpectedEmails(map[string]string{"account": "agent@example.test"})
	a.IdleTimeout = 10 * time.Millisecond
	var mu sync.Mutex
	var got []capacity.Reading
	proc, err := a.Start(t.Context(), req, func(e AdapterEvent) { mu.Lock(); defer mu.Unlock(); got = append(got, e.Capacity...) })
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Phase != "start" || got[1].Phase != "end" || got[0].UsedPercent != 31 || got[1].UsedPercent != 31 {
		t.Fatal(got)
	}
}

func TestClaudeBridgeCapacityProjectionAndEndTime(t *testing.T) {
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
	path := filepath.Join(root, "bridge.mjs")
	if err := os.WriteFile(path, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	sdk := `export function query(){ return { streamInput: async()=>{},interrupt:async()=>({still_queued:[]}),close:()=>{},async *[Symbol.asyncIterator](){
 yield {type:'system',subtype:'init',session_id:'fixture',model:'test-model',capabilities:['interrupt_receipt_v1']};
 yield {type:'rate_limit_event',session_id:'DO_NOT_FORWARD',rate_limit_info:{status:'allowed',rateLimitType:'five_hour',utilization:0.25,resetsAt:Math.floor(Date.now()/1000)+3600,extra:'DO_NOT_FORWARD'}};
 yield {type:'result'};
 } }; }`
	sdkPath := filepath.Join(root, "sdk.mjs")
	if err := os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, path, sdkPath, "/bin/true", root)
	cmd.Stdin = strings.NewReader(`{"op":"start","prompt":"fixture","model":"test-model","effort":"high"}` + "\n")
	output, _ := cmd.Output()
	if strings.Contains(string(output), "DO_NOT_FORWARD") {
		t.Fatal("bridge forwarded arbitrary fields")
	}
	var phases []string
	var times []time.Time
	for _, line := range strings.Split(string(output), "\n") {
		var frame struct {
			Kind   string          `json:"kind"`
			Phase  string          `json:"phase"`
			ReadAt time.Time       `json:"read_at"`
			Event  json.RawMessage `json:"event"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil || frame.Kind != "capacity" {
			continue
		}
		rs := capacity.Claude(frame.Event, frame.ReadAt)
		if len(rs) != 1 || rs[0].UsedPercent != 25 {
			t.Fatal("bad bridge capacity")
		}
		phases = append(phases, frame.Phase)
		times = append(times, frame.ReadAt)
	}
	if len(phases) != 2 || phases[1] != "end" || !times[0].Equal(times[1]) {
		t.Fatal("end snapshot changed observation time")
	}
}

type idleCapacityAdapter struct {
	*fakeAdapter
	calls   int
	at      time.Time
	entered chan struct{}
	release chan struct{}
}

func (a *idleCapacityAdapter) CaptureCapacity(ctx context.Context, _ string) []capacity.Reading {
	a.calls++
	if ctx.Err() != nil {
		return nil
	}
	if a.entered != nil {
		close(a.entered)
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil
		}
	}
	return []capacity.Reading{{ReadAt: a.at, WindowKind: "5h", WindowMinutes: 300, UsedPercent: 10, ResetsAt: a.at.Add(time.Hour), Source: "agentd"}}
}
func TestIdleCapacityCadenceAndLiveRunFence(t *testing.T) {
	s, api, _ := testSupervisor(t)
	reporter := &capacityTestAPI{API: api}
	s.api = reporter
	now := time.Now().UTC()
	a := &idleCapacityAdapter{fakeAdapter: s.adapters[Codex].(*fakeAdapter), at: now}
	s.adapters[Codex] = a
	s.probedAccounts["account"] = true
	s.captureIdleCapacity(t.Context(), now)
	for i := 1; i < 60; i++ {
		s.captureIdleCapacity(t.Context(), now.Add(time.Duration(i)*5*time.Second))
	}
	if a.calls != 1 {
		t.Fatal("captured every dispatch poll", a.calls)
	}
	a.at = now.Add(5 * time.Minute)
	s.captureIdleCapacity(t.Context(), a.at)
	if a.calls != 2 {
		t.Fatal("five-minute capture missing", a.calls)
	}
	s.runs["live"] = &owned{record: Record{AccountID: "account", State: "running", LaunchState: launchAttempted}}
	s.captureIdleCapacity(t.Context(), now.Add(10*time.Minute))
	if a.calls != 2 {
		t.Fatal("captured live managed account")
	}
	delete(s.runs, "live")
	e := &owned{record: Record{Generation: s.generation, AccountID: "account", RunID: "run"}}
	s.observeCapacity(e, []capacity.Reading{{ReadAt: now.Add(9 * time.Minute)}})
	s.captureIdleCapacity(t.Context(), now.Add(10*time.Minute))
	if a.calls != 2 {
		t.Fatal("ignored recent stream reading")
	}
	// Configuration is respected as well as the five-minute default.
	s.capacityInterval = time.Minute
	a.at = now.Add(11 * time.Minute)
	s.captureIdleCapacity(t.Context(), a.at)
	if a.calls != 3 {
		t.Fatal("configured cadence ignored")
	}
}
func TestCaptureDoesNotHoldDispatchBudget(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.api = &capacityTestAPI{API: api}
	a := &idleCapacityAdapter{fakeAdapter: s.adapters[Codex].(*fakeAdapter), at: time.Now(), entered: make(chan struct{}), release: make(chan struct{})}
	s.adapters[Codex] = a
	s.probedAccounts["account"] = true
	done := make(chan struct{})
	go func() { defer close(done); s.captureIdleCapacity(t.Context(), a.at) }()
	<-a.entered
	if status := s.Lifecycle(""); status.State != "capturing" || status.Ready {
		t.Fatal("idle quota capture reported draining or ready", status)
	}
	if !s.dispatchMu.TryLock() {
		t.Fatal("capture holds dispatch mutex")
	}
	s.dispatchMu.Unlock()
	if err := s.StartRun(t.Context(), api.run); err != ErrDraining {
		t.Fatal("dispatch must defer during capture", err)
	}
	status, err := s.Drain(DrainRequest{DaemonID: s.daemonID, AccountID: "account"})
	if err != nil || status.State != "draining" {
		t.Fatal("drain ignored capture process", status, err)
	}
	if err := s.Close(t.Context()); err != ErrDraining {
		t.Fatal("closed while capture still owned", err)
	}
	close(a.release)
	<-done
}
func TestCapacityOutboxReplacesMissingBuckets(t *testing.T) {
	s := &Supervisor{generation: "g"}
	e := &owned{record: Record{Generation: "g"}}
	now := time.Now()
	a := capacity.Reading{WindowKind: "5h", ReadAt: now, Source: "harness", Phase: "update"}
	b := a
	b.WindowKind = "weekly"
	s.observeCapacity(e, []capacity.Reading{a, b})
	a.ReadAt = now.Add(time.Second)
	s.observeCapacity(e, []capacity.Reading{a})
	if len(e.capacityPending) != 1 {
		t.Fatal("missing bucket remained pending", e.capacityPending)
	}
}

type latestCapacityTestAPI struct {
	*capacityTestAPI
	at      time.Time
	err     error
	timeout bool
}

func (a *latestCapacityTestAPI) LatestCapacityReadAt(ctx context.Context, _ string) (time.Time, error) {
	if a.timeout {
		<-ctx.Done()
		return time.Time{}, ctx.Err()
	}
	return a.at, a.err
}

func TestIdleCaptureLookupTimeoutLeavesCaptureBudget(t *testing.T) {
	s, api, _ := testSupervisor(t)
	reporter := &latestCapacityTestAPI{capacityTestAPI: &capacityTestAPI{API: api}, timeout: true}
	s.api = reporter
	a := &idleCapacityAdapter{fakeAdapter: s.adapters[Codex].(*fakeAdapter), at: time.Now()}
	s.adapters[Codex] = a
	s.probedAccounts["account"] = true
	s.captureIdleCapacity(t.Context(), a.at)
	if a.calls != 1 || len(reporter.got) != 1 {
		t.Fatal("freshness lookup exhausted the capture deadline")
	}
}

func TestIdleCaptureLookupFailureFallsBackAcrossRestart(t *testing.T) {
	for _, reportFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("report_failure=%v", reportFails), func(t *testing.T) {
			s, api, _ := testSupervisor(t)
			now := time.Now().UTC()
			reporter := &latestCapacityTestAPI{capacityTestAPI: &capacityTestAPI{API: api, fail: reportFails}, err: &client.StatusError{Status: 403, Message: "private response must not be logged"}}
			s.api = reporter
			a := &idleCapacityAdapter{fakeAdapter: s.adapters[Codex].(*fakeAdapter), at: now}
			s.adapters[Codex] = a
			s.probedAccounts["account"] = true
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			s.captureIdleCapacity(t.Context(), now)
			if a.calls != 1 {
				t.Fatal("lookup error suppressed first capture")
			}
			if !strings.Contains(logs.String(), "http_status=403") || strings.Contains(logs.String(), "private response") {
				t.Fatal("missing bounded lookup error log")
			}
			root, workspace, accounts := s.state.Path(), s.workspace, s.accounts
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			restarted, err := NewSupervisor(t.Context(), Config{API: reporter, StateRoot: root, DaemonID: "daemon", Workspace: workspace, Accounts: accounts, Adapters: []Adapter{a}})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close(t.Context())
			restarted.probedAccounts["account"] = true
			if !restarted.capacitySaved["account"].Equal(now) {
				t.Fatal("successful local capture did not survive restart")
			}
			restarted.captureIdleCapacity(t.Context(), now.Add(time.Minute))
			if a.calls != 1 {
				t.Fatal("restart ignored recent local capture")
			}
			a.at = now.Add(5 * time.Minute)
			restarted.captureIdleCapacity(t.Context(), a.at)
			if a.calls != 2 {
				t.Fatal("lookup error suppressed stale local capture")
			}
		})
	}
}
func TestIdleCaptureRespectsReadingFromPreviousDaemon(t *testing.T) {
	s, api, _ := testSupervisor(t)
	now := time.Now()
	s.api = &latestCapacityTestAPI{capacityTestAPI: &capacityTestAPI{API: api}, at: now.Add(-time.Minute)}
	a := &idleCapacityAdapter{fakeAdapter: s.adapters[Codex].(*fakeAdapter), at: now}
	s.adapters[Codex] = a
	s.probedAccounts["account"] = true
	s.captureIdleCapacity(t.Context(), now)
	if a.calls != 0 {
		t.Fatal("fresh server reading ignored")
	}
}
