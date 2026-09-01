// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func runnerControlTargetFixture(runID int64) runnerControlTarget {
	return runnerControlTarget{DeliveryID: 1, DeliveryKey: "delivery:opaque", DeliveryRevision: 2,
		RootIssueID: 3, IssueRevision: 4, AttemptID: 5, AttemptNumber: 1, PlanRevision: 6,
		StageKey: "implementation", ExecutionNumber: 1, ExecutionStartStageEventID: 7,
		AuthorityEpoch: 8, AuthorityStageEventID: 9, ReporterID: 10, RunID: runID}
}

func TestRunnerControlTargetRequiresEveryClosedBindingField(t *testing.T) {
	valid := runnerControlTargetFixture(17)
	if !valid.validForRun(17) {
		t.Fatal("complete target was rejected")
	}
	mutations := []func(*runnerControlTarget){
		func(v *runnerControlTarget) { v.DeliveryID = 0 },
		func(v *runnerControlTarget) { v.DeliveryKey = "bad key" },
		func(v *runnerControlTarget) { v.DeliveryRevision = 0 },
		func(v *runnerControlTarget) { v.RootIssueID = 0 },
		func(v *runnerControlTarget) { v.IssueRevision = 0 },
		func(v *runnerControlTarget) { v.AttemptID = 0 },
		func(v *runnerControlTarget) { v.AttemptNumber = 0 },
		func(v *runnerControlTarget) { v.PlanRevision = 0 },
		func(v *runnerControlTarget) { v.StageKey = "provider-defined" },
		func(v *runnerControlTarget) { v.ExecutionNumber = 0 },
		func(v *runnerControlTarget) { v.ExecutionStartStageEventID = 0 },
		func(v *runnerControlTarget) { v.AuthorityEpoch = 0 },
		func(v *runnerControlTarget) { v.AuthorityStageEventID = 0 },
		func(v *runnerControlTarget) { v.ReporterID = 0 },
		func(v *runnerControlTarget) { v.RunID = 18 },
	}
	for index, mutate := range mutations {
		candidate := valid
		mutate(&candidate)
		if candidate.validForRun(17) {
			t.Fatalf("mutation %d left target valid: %+v", index, candidate)
		}
	}
}

func TestRunnerControlHTTPRetriesImmutableRequestAndStableKey(t *testing.T) {
	var mu sync.Mutex
	var keys, bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		keys = append(keys, r.Header.Get(idempotencyHeader))
		bodies = append(bodies, string(raw))
		attempt := len(keys)
		mu.Unlock()
		if attempt == 1 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runnerControlLease{LeaseID: "lease-opaque", Revision: 1,
			DeliveryKey: "delivery:opaque", IssueKey: "PAI-809",
			Actions: []string{"run.cancel.running"}, ExpiresAt: time.Now().Add(runnerControlLeaseTTL),
			Target: runnerControlTargetFixture(17)})
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, apiKey: "test-key", http: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lease, err := (runnerControlHTTP{client: client}).issueLease(ctx, 17, "runner-device", "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if lease.Target.RunID != 17 {
		t.Fatalf("lease=%+v", lease)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] || bodies[0] != bodies[1] {
		t.Fatalf("keys=%v bodies=%v", keys, bodies)
	}
	if !strings.Contains(bodies[0], `"supported_actions":["run.cancel.running"]`) ||
		strings.Contains(bodies[0], "input.respond") || strings.Contains(bodies[0], "run.pause") ||
		strings.Contains(bodies[0], "run.resume") {
		t.Fatalf("one-shot lease advertised unsupported actions: %s", bodies[0])
	}
	if strings.Contains(strings.Join(bodies, ""), "test-key") {
		t.Fatal("bearer key entered the request body")
	}
}

func TestRunnerControlRenewKeepsRevisionForUnchangedBinding(t *testing.T) {
	target := runnerControlTargetFixture(17)
	oldExpiry := time.Now().Add(time.Minute)
	lease := runnerControlLease{LeaseID: "lease-opaque", Revision: 4, DeliveryKey: target.DeliveryKey,
		IssueKey: "PAI-809", Actions: []string{"run.cancel.running"}, ExpiresAt: oldExpiry, Target: target}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Operation string `json:"operation"`
			Revision  int64  `json:"revision"`
			DeviceID  string `json:"device_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Operation != "renew" || body.Revision != 4 || body.DeviceID != "device-a" {
			t.Errorf("renew body=%+v", body)
		}
		renewed := lease
		renewed.ExpiresAt = oldExpiry.Add(time.Minute)
		_ = json.NewEncoder(w).Encode(renewed)
	}))
	defer server.Close()
	httpControl := runnerControlHTTP{client: &Client{baseURL: server.URL, apiKey: "test", http: server.Client()}}
	renewed, err := httpControl.renewLease(context.Background(), lease, "device-a", "renew-attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Revision != lease.Revision || !renewed.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("renewed=%+v lease=%+v", renewed, lease)
	}
}

func TestRunnerControlRenewUsesFreshKeyPerLogicalAttempt(t *testing.T) {
	target := runnerControlTargetFixture(17)
	lease := runnerControlLease{LeaseID: "lease-opaque", Revision: 4, DeliveryKey: target.DeliveryKey,
		IssueKey: "PAI-809", Actions: []string{"run.cancel.running"}, ExpiresAt: time.Now().Add(time.Minute), Target: target}
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get(idempotencyHeader))
		renewed := lease
		renewed.ExpiresAt = lease.ExpiresAt.Add(time.Duration(len(keys)) * time.Minute)
		_ = json.NewEncoder(w).Encode(renewed)
	}))
	defer server.Close()
	httpControl := runnerControlHTTP{client: &Client{baseURL: server.URL, apiKey: "test", http: server.Client()}}
	if _, err := httpControl.renewLease(context.Background(), lease, "device-a", "renew-attempt-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := httpControl.renewLease(context.Background(), lease, "device-a", "renew-attempt-b"); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] == keys[1] {
		t.Fatalf("renewal operation keys=%v", keys)
	}
}

func TestRunnerControlPumpRecoversTransientRenewal(t *testing.T) {
	target := runnerControlTargetFixture(17)
	var mu sync.Mutex
	renewCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/runs/") && strings.Contains(r.URL.Path, "control-capability-leases") {
			_ = json.NewEncoder(w).Encode(runnerControlLease{LeaseID: "lease-opaque", Revision: 1,
				DeliveryKey: target.DeliveryKey, IssueKey: "PAI-809", Actions: []string{"run.cancel.running"},
				ExpiresAt: time.Now().Add(time.Second), Target: target})
			return
		}
		var operation struct {
			Operation string `json:"operation"`
		}
		_ = json.NewDecoder(r.Body).Decode(&operation)
		if operation.Operation == "renew" {
			mu.Lock()
			renewCalls++
			call := renewCalls
			mu.Unlock()
			if call == 1 {
				http.Error(w, "retry", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(runnerControlLease{LeaseID: "lease-opaque", Revision: 1,
				DeliveryKey: target.DeliveryKey, IssueKey: "PAI-809", Actions: []string{"run.cancel.running"},
				ExpiresAt: time.Now().Add(2 * time.Second), Target: target})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	arbiter := newRunControlArbiter(&Client{baseURL: server.URL, apiKey: "test", http: server.Client()}, 17, "device-a", nil)
	arbiter.renewEvery, arbiter.pullInterval = 5*time.Millisecond, time.Hour
	arbiter.operationTime, arbiter.retryMin, arbiter.retryMax = 15*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := arbiter.start(ctx, true); err != nil {
		t.Fatal(err)
	}
	arbiter.mu.Lock()
	initialExpiry := arbiter.lease.ExpiresAt
	arbiter.mu.Unlock()
	waitForRunnerControl(t, ctx, func() bool {
		arbiter.mu.Lock()
		renewed := arbiter.lease.ExpiresAt.After(initialExpiry)
		arbiter.mu.Unlock()
		mu.Lock()
		defer mu.Unlock()
		return renewCalls >= 2 && renewed
	})
	arbiter.stop(ctx)
	arbiter.mu.Lock()
	renewedLease := arbiter.lease
	arbiter.mu.Unlock()
	if !renewedLease.ExpiresAt.After(initialExpiry) {
		t.Fatalf("renewed lease was not installed: %+v", renewedLease)
	}
}

func TestRunnerControlPumpIssuesFreshLineageAfterExpiry(t *testing.T) {
	target := runnerControlTargetFixture(17)
	var mu sync.Mutex
	var issueKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/runs/") && strings.Contains(r.URL.Path, "control-capability-leases") {
			mu.Lock()
			issueKeys = append(issueKeys, r.Header.Get(idempotencyHeader))
			issueNumber := len(issueKeys)
			mu.Unlock()
			leaseID, expiry := "lease-old", time.Now().Add(100*time.Millisecond)
			if issueNumber > 1 {
				leaseID, expiry = "lease-fresh", time.Now().Add(time.Second)
			}
			_ = json.NewEncoder(w).Encode(runnerControlLease{LeaseID: leaseID, Revision: 1,
				DeliveryKey: target.DeliveryKey, IssueKey: "PAI-809", Actions: []string{"run.cancel.running"},
				ExpiresAt: expiry, Target: target})
			return
		}
		var operation struct {
			Operation string `json:"operation"`
		}
		_ = json.NewDecoder(r.Body).Decode(&operation)
		if operation.Operation == "renew" {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	arbiter := newRunControlArbiter(&Client{baseURL: server.URL, apiKey: "test", http: server.Client()}, 17, "device-a", nil)
	arbiter.renewEvery, arbiter.pullInterval = 5*time.Millisecond, time.Hour
	arbiter.operationTime, arbiter.retryMin, arbiter.retryMax = 5*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := arbiter.start(ctx, true); err != nil {
		t.Fatal(err)
	}
	waitForRunnerControl(t, ctx, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(issueKeys) >= 2
	})
	arbiter.stop(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(issueKeys) < 2 || issueKeys[0] == "" || issueKeys[0] == issueKeys[1] {
		t.Fatalf("issue attempt keys=%v", issueKeys)
	}
}

func waitForRunnerControl(t *testing.T, ctx context.Context, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("runner control condition did not become true")
		case <-ticker.C:
		}
	}
}

func TestRunnerControlPullAndRevokeBindExactDevice(t *testing.T) {
	lease := runnerControlLease{LeaseID: "lease-opaque", Revision: 3, DeliveryKey: "delivery:opaque",
		IssueKey: "PAI-809", Actions: []string{"run.cancel.running"}, ExpiresAt: time.Now().Add(time.Minute),
		Target: runnerControlTargetFixture(17)}
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Operation string `json:"operation"`
			DeviceID  string `json:"device_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.DeviceID != "device-a" {
			t.Errorf("device_id=%q", body.DeviceID)
		}
		operations = append(operations, body.Operation)
		if strings.Contains(r.URL.Path, "/runs/") {
			_ = json.NewEncoder(w).Encode(runnerControlPull{Effects: []runnerControlEffect{}})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	controlHTTP := runnerControlHTTP{client: &Client{baseURL: server.URL, apiKey: "test", http: server.Client()}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := controlHTTP.pull(ctx, lease, 0, "device-a"); err != nil {
		t.Fatal(err)
	}
	if err := controlHTTP.revokeLease(ctx, lease, "device-a"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(operations, []string{"", "revoke"}) {
		t.Fatalf("operations=%v", operations)
	}
}

func TestRunnerControlClaimRejectsTypedEffectDrift(t *testing.T) {
	target := runnerControlTargetFixture(17)
	effect := runnerControlEffect{OutboxID: 1, CommandID: "command-input", Action: "input.respond",
		EffectSequence: 1, LeaseID: "lease-opaque", LeaseRevision: 3, Target: target,
		InputRequestID: "request-opaque", InputRevision: 2, InputResponse: "choice", ChoiceOrdinal: 1,
		ChoiceCode: "choice_1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		drifted := effect
		drifted.ChoiceOrdinal, drifted.ChoiceCode = 2, "choice_2"
		_ = json.NewEncoder(w).Encode(drifted)
	}))
	defer server.Close()
	controlHTTP := runnerControlHTTP{client: &Client{baseURL: server.URL, apiKey: "test", http: server.Client()},
		supportedActions: []string{"input.respond"}}
	if _, err := controlHTTP.claim(context.Background(), effect, "device-a"); err == nil {
		t.Fatal("claim accepted a response that changed the pulled typed input")
	}
}

func TestStableRunnerControlKeyIsCanonicalAndDecisionScoped(t *testing.T) {
	one := stableRunnerControlKey("command.claim", "command-a", "1")
	replay := stableRunnerControlKey("command.claim", "command-a", "1")
	other := stableRunnerControlKey("command.claim", "command-b", "1")
	canonical := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if one != replay || one == other || !canonical.MatchString(one) {
		t.Fatalf("one=%q replay=%q other=%q", one, replay, other)
	}
}

func TestSupervisorCancellationCausesRemainDistinct(t *testing.T) {
	for _, tc := range []struct {
		outcome supervisorOutcome
		cause   string
	}{
		{outcome: outcomeTimeout, cause: "execution_timeout"},
		{outcome: outcomeSilentChild, cause: "silence_timeout"},
		{outcome: outcomeCancellation, cause: "runner_shutdown"},
		{outcome: outcomeServerCancellation, cause: "server_cancel"},
	} {
		fields := supervisorFailureFields(supervisorResult{Outcome: tc.outcome, Summary: "bounded"})
		if fields["status"] != "cancelled" || fields["cancellation_cause"] != tc.cause {
			t.Fatalf("outcome=%s fields=%v want cause=%s", tc.outcome, fields, tc.cause)
		}
	}
}

func TestSupervisorIssuesCapabilityOnlyAfterOwnedSpawnProof(t *testing.T) {
	var started int
	req := supervisorFixture("sleep 0.05")
	req.OwnedProcessStarted = func(_ context.Context, owned bool) error {
		if !owned {
			t.Fatal("supported Unix process lacked ownership proof")
		}
		started++
		return nil
	}
	result := superviseAgentProcess(context.Background(), req)
	if result.Outcome != outcomeNormalExit || started != 1 {
		t.Fatalf("outcome=%s started=%d", result.Outcome, started)
	}
	if runnerControlLeaseTTL != 90*time.Second || runnerControlRenewEvery != 30*time.Second {
		t.Fatalf("lease cadence ttl=%s renew=%s", runnerControlLeaseTTL, runnerControlRenewEvery)
	}
}

func TestSupervisorOperatorCancellationRecordsAppliedAfterReap(t *testing.T) {
	requests := make(chan runnerClaimedCancellation, 1)
	requests <- runnerClaimedCancellation{Effect: runnerControlEffect{CommandID: "command-opaque",
		LeaseID: "lease-opaque", LeaseRevision: 1, EffectSequence: 1, Target: runnerControlTargetFixture(7)}}
	var outcome, reason string
	req := supervisorFixture("sleep 30")
	req.ControlRequests = requests
	req.ControlResult = func(_ context.Context, _ runnerClaimedCancellation, gotOutcome, gotReason string) error {
		outcome, reason = gotOutcome, gotReason
		return nil
	}
	result := superviseAgentProcess(context.Background(), req)
	if result.Outcome != outcomeOperatorCancellation || outcome != "applied" || reason != "" {
		t.Fatalf("result=%+v control=%s/%s", result, outcome, reason)
	}
}

func TestRunnerControlRestartReplaysOnlyCompletedExactClaim(t *testing.T) {
	target := runnerControlTargetFixture(17)
	var mu sync.Mutex
	operations := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Operation string `json:"operation"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		operation := body.Operation
		if strings.Contains(r.URL.Path, "control-capability-leases") && operation == "" {
			operation = "issue"
		}
		mu.Lock()
		operations[operation]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch operation {
		case "issue":
			_ = json.NewEncoder(w).Encode(runnerControlLease{LeaseID: "lease-opaque", Revision: 1,
				DeliveryKey: "delivery:opaque", IssueKey: "PAI-809",
				Actions: []string{"run.cancel.running"}, ExpiresAt: time.Now().Add(runnerControlLeaseTTL), Target: target})
		case "claim":
			_ = json.NewEncoder(w).Encode(runnerControlEffect{CommandID: "command-completed", Action: "run.cancel.running",
				EffectSequence: 1, LeaseID: "lease-opaque", LeaseRevision: 1, Target: target})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	journal, err := openRunnerControlJournal(filepath.Join(t.TempDir(), "runner-state"))
	if err != nil {
		t.Fatal(err)
	}
	completedDigest := sha256.Sum256([]byte("command-completed\x00applied\x00"))
	claimedDigest := sha256.Sum256([]byte("command-claimed\x00claimed"))
	for _, record := range []runnerControlJournalRecord{
		{CommandID: "command-completed", LeaseID: "lease-opaque", LeaseRevision: 1, EffectSequence: 1,
			ClaimSequence: 1, ResultSequence: 1, RequestDigest: hex.EncodeToString(completedDigest[:]), Outcome: "applied", State: "completed"},
		{CommandID: "command-claimed", LeaseID: "lease-opaque", LeaseRevision: 1, EffectSequence: 1,
			ClaimSequence: 1, ResultSequence: 1, RequestDigest: hex.EncodeToString(claimedDigest[:]), Outcome: "outcome_unknown", State: "claimed"},
	} {
		if err := journal.put(record); err != nil {
			t.Fatal(err)
		}
	}
	client := &Client{baseURL: server.URL, apiKey: "test", http: server.Client()}
	arbiter := newRunControlArbiter(client, 17, "device-a", journal)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := arbiter.start(ctx, true); err != nil {
		t.Fatal(err)
	}
	arbiter.stop(ctx)
	records := journal.snapshot()
	if len(records) != 1 || records[0].CommandID != "command-claimed" || records[0].State != "claimed" {
		t.Fatalf("records=%+v", records)
	}
	mu.Lock()
	defer mu.Unlock()
	if operations["claim"] != 1 || operations["result"] != 1 {
		t.Fatalf("operations=%v", operations)
	}
}

func TestRunnerControlQuiesceConfirmationFailsClosed(t *testing.T) {
	stuck := &runControlArbiter{cancel: func() {}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if stuck.quiesceConfirmed(ctx) {
		t.Fatal("timed-out control pump reported quiesced")
	}
	close(stuck.done)
	if !stuck.quiesceConfirmed(context.Background()) {
		t.Fatal("closed control pump did not report quiesced")
	}
	failedDone := make(chan struct{})
	close(failedDone)
	failed := &runControlArbiter{done: failedDone, cancel: func() {}, pumpErr: errors.New("journal failed")}
	if failed.quiesceConfirmed(context.Background()) {
		t.Fatal("failed control pump reported healthy quiescence")
	}
}

func TestRunnerControlNaturalExitRejectsEveryQuiescedClaim(t *testing.T) {
	var mu sync.Mutex
	resultCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Operation      string `json:"operation"`
			ClaimSequence  int64  `json:"claim_sequence"`
			ResultSequence int64  `json:"result_sequence"`
			Outcome        string `json:"outcome"`
			Reason         string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Operation != "result" || body.ClaimSequence != 1 || body.ResultSequence != 1 ||
			body.Outcome != "rejected" || body.Reason != "natural_exit" {
			t.Errorf("body=%+v", body)
		}
		mu.Lock()
		resultCount++
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, apiKey: "test", http: server.Client()}
	arbiter := newRunControlArbiter(client, 17, "device-a", nil)
	for index := int64(1); index <= 3; index++ {
		arbiter.requests <- runnerClaimedCancellation{Effect: runnerControlEffect{CommandID: "command-" + strconv.FormatInt(index, 10),
			LeaseID: "lease-opaque", LeaseRevision: 1, EffectSequence: 1, Target: runnerControlTargetFixture(17)}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := arbiter.rejectNaturalExit(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if resultCount != 3 {
		t.Fatalf("results=%d want=3", resultCount)
	}
}
