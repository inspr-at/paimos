// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/conversationturns"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

const syntheticConversationAnswer = "Synthetic integration answer."

const syntheticUnderstanding = `{"summary":"Synthetic integration understanding","facts":[],"open_questions":[],"next_question":"","candidate_requirements":[],"project_kinds":["integration"]}`

type integrationConversationLauncher struct {
	mu     sync.Mutex
	claims []lifecycleclient.ConversationClaim
}

func (l *integrationConversationLauncher) LaunchConversation(_ context.Context, claim lifecycleclient.ConversationClaim) (lifecycleclient.ConversationProcess, error) {
	l.mu.Lock()
	l.claims = append(l.claims, claim)
	index := len(l.claims)
	l.mu.Unlock()
	answer := syntheticConversationAnswer
	if claim.Purpose == "understand" || claim.Purpose == "interpret" {
		answer = syntheticUnderstanding
	}
	return integrationConversationProcess{
		threadID: fmt.Sprintf("synthetic-thread-%d", index),
		turnID:   fmt.Sprintf("synthetic-turn-%d", index),
		answer:   answer,
	}, nil
}

func (l *integrationConversationLauncher) snapshot() []lifecycleclient.ConversationClaim {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]lifecycleclient.ConversationClaim(nil), l.claims...)
}

type integrationConversationProcess struct {
	threadID string
	turnID   string
	answer   string
}

func (p integrationConversationProcess) Identity() (string, string, error) {
	return p.threadID, p.turnID, nil
}

func (p integrationConversationProcess) Wait(context.Context) (lifecycleclient.ConversationExecutionResult, error) {
	return lifecycleclient.ConversationExecutionResult{
		Outcome: "completed", ThreadID: p.threadID, TurnID: p.turnID, Text: p.answer,
	}, nil
}

func (integrationConversationProcess) Stop(context.Context) error { return nil }

type scriptedIntegrationLauncher struct {
	mu        sync.Mutex
	result    lifecycleclient.ConversationExecutionResult
	launchErr error
	launches  int
	waits     int
	stops     int
	claim     lifecycleclient.ConversationClaim
}

func (l *scriptedIntegrationLauncher) LaunchConversation(_ context.Context, claim lifecycleclient.ConversationClaim) (lifecycleclient.ConversationProcess, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.launches++
	l.claim = claim
	if l.launchErr != nil {
		return nil, l.launchErr
	}
	return &scriptedIntegrationProcess{launcher: l}, nil
}

func (l *scriptedIntegrationLauncher) snapshot() (int, int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.launches, l.waits, l.stops
}

func (l *scriptedIntegrationLauncher) claimSnapshot() lifecycleclient.ConversationClaim {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.claim
}

type scriptedIntegrationProcess struct {
	launcher *scriptedIntegrationLauncher
}

func (p *scriptedIntegrationProcess) Identity() (string, string, error) {
	return p.launcher.result.ThreadID, p.launcher.result.TurnID, nil
}

func (p *scriptedIntegrationProcess) Wait(context.Context) (lifecycleclient.ConversationExecutionResult, error) {
	p.launcher.mu.Lock()
	p.launcher.waits++
	result := p.launcher.result
	p.launcher.mu.Unlock()
	return result, nil
}

func (p *scriptedIntegrationProcess) Stop(context.Context) error {
	p.launcher.mu.Lock()
	p.launcher.stops++
	p.launcher.mu.Unlock()
	return nil
}

type cancelAfterFinalControlAuthority struct {
	base   *lifecycleclient.HTTP
	cancel func()
	mu     sync.Mutex
	count  int
}

func (a *cancelAfterFinalControlAuthority) ClaimConversation(ctx context.Context, runtime, generation string) (*lifecycleclient.ConversationClaim, error) {
	return a.base.ClaimConversation(ctx, runtime, generation)
}

func (a *cancelAfterFinalControlAuthority) ConversationControl(ctx context.Context, runtime, callID, execution string) (lifecycleclient.ConversationControl, error) {
	control, err := a.base.ConversationControl(ctx, runtime, callID, execution)
	a.mu.Lock()
	a.count++
	count := a.count
	a.mu.Unlock()
	if err == nil && count == 3 {
		// The returned control is deliberately stale: cancellation commits at
		// the server after this final check and before the completion report.
		a.cancel()
	}
	return control, err
}

func (a *cancelAfterFinalControlAuthority) ReportConversationEvent(ctx context.Context, runtime, generation, callID, execution string, event lifecycleclient.ConversationEvent) (lifecycleclient.ConversationCall, error) {
	return a.base.ReportConversationEvent(ctx, runtime, generation, callID, execution, event)
}

type blockingIntegrationLauncher struct {
	launched chan struct{}
	process  *blockingIntegrationProcess
	mu       sync.Mutex
	claim    lifecycleclient.ConversationClaim
}

func (l *blockingIntegrationLauncher) LaunchConversation(_ context.Context, claim lifecycleclient.ConversationClaim) (lifecycleclient.ConversationProcess, error) {
	l.mu.Lock()
	l.claim = claim
	l.mu.Unlock()
	close(l.launched)
	return l.process, nil
}

func (l *blockingIntegrationLauncher) snapshot() lifecycleclient.ConversationClaim {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.claim
}

type blockingIntegrationProcess struct {
	stopped chan struct{}
	once    sync.Once
}

func (*blockingIntegrationProcess) Identity() (string, string, error) {
	return "synthetic-blocking-thread", "synthetic-blocking-turn", nil
}

func (p *blockingIntegrationProcess) Wait(ctx context.Context) (lifecycleclient.ConversationExecutionResult, error) {
	select {
	case <-p.stopped:
		return lifecycleclient.ConversationExecutionResult{
			Outcome: "cancelled", Failure: "cancelled", ThreadID: "synthetic-blocking-thread", TurnID: "synthetic-blocking-turn",
		}, nil
	case <-ctx.Done():
		return lifecycleclient.ConversationExecutionResult{
			Outcome: "cancelled", Failure: "deadline_exceeded", ThreadID: "synthetic-blocking-thread", TurnID: "synthetic-blocking-turn",
		}, ctx.Err()
	}
}

func (p *blockingIntegrationProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.stopped) })
	return nil
}

func newIntegrationConversationRunner(t *testing.T, f *conversationRouteFixture, origin string) (*lifecycleclient.ConversationRunner, *integrationConversationLauncher) {
	t.Helper()
	authority, err := lifecycleclient.NewHTTP(origin, f.projectID, conversationTestLease, func() (string, error) {
		return f.runnerKey, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := &integrationConversationLauncher{}
	runner, err := lifecycleclient.NewConversationRunner(directory, f.runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	return runner, launcher
}

func integrationConversationAuthority(t *testing.T, f *conversationRouteFixture, origin string) *lifecycleclient.HTTP {
	t.Helper()
	authority, err := lifecycleclient.NewHTTP(origin, f.projectID, conversationTestLease, func() (string, error) {
		return f.runnerKey, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func submitIntegrationConversation(t *testing.T, client *http.Client, origin string, f *conversationRouteFixture, request conversationturns.Request) conversationturns.Call {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("%s/api/projects/%d/conversation/v1/calls", origin, f.projectID)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.serviceKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(conversationturns.ActorIssuerHeader, f.actor.Issuer)
	req.Header.Set(conversationturns.ActorSubjectHeader, f.actor.Subject)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", response.StatusCode, body)
	}
	var call conversationturns.Call
	if err := json.Unmarshal(body, &call); err != nil {
		t.Fatalf("decode admitted call: %v", err)
	}
	return call
}

func TestConversationLifecycleClientAgainstProductionRouter(t *testing.T) {
	f := openConversationRouteFixture(t)
	server := httptest.NewServer(f.router)
	defer server.Close()
	runner, launcher := newIntegrationConversationRunner(t, f, server.URL)

	for _, purpose := range []string{"chat", "understand"} {
		request := f.callRequest("integration-"+purpose, "integration-conversation-"+purpose, purpose)
		call := submitIntegrationConversation(t, server.Client(), server.URL, f, request)
		if err := runner.Step(context.Background(), f.runtime); err != nil {
			t.Fatalf("run %s conversation: %v", purpose, err)
		}
		base := fmt.Sprintf("/api/projects/%d/conversation/v1", f.projectID)
		response := f.request(t, http.MethodGet, base+"/calls/"+call.CallID, nil, f.serviceKey, true)
		completed := decodeResponse[conversationturns.Call](t, response)
		want := syntheticConversationAnswer
		if purpose == "understand" {
			want = syntheticUnderstanding
		}
		if response.Code != http.StatusOK || completed.State != "completed" || completed.OutputText != want || completed.LastSequence != 3 {
			t.Fatalf("%s completion status=%d call=%+v", purpose, response.Code, completed)
		}
		wrongActor, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf("%s/api/projects/%d/conversation/v1/calls/%s", server.URL, f.projectID, call.CallID), nil)
		if err != nil {
			t.Fatal(err)
		}
		wrongActor.Header.Set("Authorization", "Bearer "+f.serviceKey)
		wrongActor.Header.Set(conversationturns.ActorIssuerHeader, f.actor.Issuer)
		wrongActor.Header.Set(conversationturns.ActorSubjectHeader, "synthetic-wrong-actor")
		wrongActorResponse, err := server.Client().Do(wrongActor)
		if err != nil {
			t.Fatal(err)
		}
		_ = wrongActorResponse.Body.Close()
		if wrongActorResponse.StatusCode != http.StatusForbidden {
			t.Fatalf("%s actor mismatch status=%d", purpose, wrongActorResponse.StatusCode)
		}
	}

	claims := launcher.snapshot()
	if len(claims) != 2 || claims[0].Purpose != "chat" || len(claims[0].OutputSchema) != 0 ||
		claims[1].Purpose != "understand" || len(claims[1].OutputSchema) == 0 {
		t.Fatalf("runner received incompatible claims: %+v", claims)
	}
	last := claims[1]
	digest := sha256.Sum256([]byte(syntheticUnderstanding))
	replay := lifecycleclient.ConversationEvent{
		Sequence: 3, Kind: "completed", ThreadID: "synthetic-thread-2", TurnID: "synthetic-turn-2",
		OutputSHA256: hex.EncodeToString(digest[:]),
	}
	authority := integrationConversationAuthority(t, f, server.URL)
	if _, err := authority.ReportConversationEvent(context.Background(), f.runtime.ID, f.runtime.Generation,
		last.Call.CallID, last.ExecutionGeneration, replay); err != nil {
		t.Fatalf("exact terminal replay: %v", err)
	}
	// The runner's serialized terminal event is immutable: changed bytes at the
	// same sequence and a skipped sequence must conflict at the real boundary.
	replay.OutputSHA256 = strings.Repeat("0", 64)
	_, err := authority.ReportConversationEvent(context.Background(), f.runtime.ID, f.runtime.Generation,
		last.Call.CallID, last.ExecutionGeneration, replay)
	if !errors.Is(err, lifecycleintents.ErrConflict) {
		t.Fatalf("changed terminal replay err=%v", err)
	}
	_, err = authority.ReportConversationEvent(context.Background(), f.runtime.ID, f.runtime.Generation,
		last.Call.CallID, last.ExecutionGeneration, lifecycleclient.ConversationEvent{
			Sequence: 5, Kind: "assistant_delta", Text: "skipped", ThreadID: "synthetic-thread-2", TurnID: "synthetic-turn-2",
		})
	if !errors.Is(err, lifecycleintents.ErrConflict) {
		t.Fatalf("skipped event sequence err=%v", err)
	}
}

func TestConversationLifecycleClientCancellationAndDeadlineAgainstProductionRouter(t *testing.T) {
	for _, test := range []struct {
		name      string
		timeoutMS int64
		cancel    bool
	}{
		{name: "cancel", timeoutMS: 3_000, cancel: true},
		{name: "deadline", timeoutMS: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openConversationRouteFixture(t)
			server := httptest.NewServer(f.router)
			defer server.Close()
			authority := integrationConversationAuthority(t, f, server.URL)
			process := &blockingIntegrationProcess{stopped: make(chan struct{})}
			launcher := &blockingIntegrationLauncher{launched: make(chan struct{}), process: process}
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			runner, err := lifecycleclient.NewConversationRunner(directory, f.runtime.Generation, authority, launcher)
			if err != nil {
				t.Fatal(err)
			}
			request := f.callRequest("integration-"+test.name, "integration-conversation-"+test.name, "chat")
			request.TimeoutMS = test.timeoutMS
			call := submitIntegrationConversation(t, server.Client(), server.URL, f, request)
			runDone := make(chan error, 1)
			go func() { runDone <- runner.Step(context.Background(), f.runtime) }()
			select {
			case <-launcher.launched:
			case <-time.After(time.Second):
				t.Fatal("conversation process did not launch")
			}
			if test.cancel {
				response := f.request(t, http.MethodPost,
					fmt.Sprintf("/api/projects/%d/conversation/v1/calls/%s/cancel", f.projectID, call.CallID),
					struct{}{}, f.serviceKey, true)
				if response.Code != http.StatusOK || decodeResponse[conversationturns.Call](t, response).State != "cancel_requested" {
					t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
				}
			}
			select {
			case err := <-runDone:
				if err != nil {
					t.Fatalf("runner result: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("conversation runner did not terminate")
			}
			base := fmt.Sprintf("/api/projects/%d/conversation/v1", f.projectID)
			response := f.request(t, http.MethodGet, base+"/calls/"+call.CallID, nil, f.serviceKey, true)
			completed := decodeResponse[conversationturns.Call](t, response)
			if response.Code != http.StatusOK || completed.State != "cancelled" || completed.OutputText != "" {
				t.Fatalf("%s terminal status=%d call=%+v", test.name, response.Code, completed)
			}
			pageResponse := f.request(t, http.MethodGet, base+"/calls/"+call.CallID+"/events?after=0", nil, f.serviceKey, true)
			page := decodeResponse[conversationturns.EventsPage](t, pageResponse)
			if pageResponse.Code != http.StatusOK || len(page.Events) != 2 || page.Events[0].Kind != "started" || page.Events[1].Kind != "cancelled" {
				t.Fatalf("%s terminal events status=%d page=%+v", test.name, pageResponse.Code, page)
			}
			claim := launcher.snapshot()
			terminal := page.Events[1]
			if _, err := authority.ReportConversationEvent(context.Background(), f.runtime.ID, f.runtime.Generation,
				call.CallID, claim.ExecutionGeneration, lifecycleclient.ConversationEvent{
					Sequence: int(terminal.Sequence), Kind: terminal.Kind, ThreadID: terminal.ThreadID,
					TurnID: terminal.TurnID, ErrorCode: terminal.ErrorCode,
				}); err != nil {
				t.Fatalf("%s terminal replay: %v", test.name, err)
			}
			var released int
			if err := db.DB.QueryRow(`SELECT released_at IS NOT NULL FROM conversation_account_slots WHERE call_id=?`, call.CallID).Scan(&released); err != nil || released != 1 {
				t.Fatalf("%s slot release=%d err=%v", test.name, released, err)
			}
			next := f.callRequest("integration-next-"+test.name, "integration-next-"+test.name, "chat")
			nextCall := submitIntegrationConversation(t, server.Client(), server.URL, f, next)
			cleanup := f.request(t, http.MethodPost, base+"/calls/"+nextCall.CallID+"/cancel", struct{}{}, f.serviceKey, true)
			if cleanup.Code != http.StatusOK || decodeResponse[conversationturns.Call](t, cleanup).State != "cancelled" {
				t.Fatalf("%s account reuse cleanup status=%d body=%s", test.name, cleanup.Code, cleanup.Body.String())
			}
		})
	}
}

func TestConversationLifecycleClientFailureVocabularyAgainstProductionRouter(t *testing.T) {
	tests := []struct {
		name       string
		failure    string
		wantState  string
		wantCode   string
		launchFail bool
	}{
		{name: "protocol", failure: "protocol_error", wantState: "failed", wantCode: "malformed_completion"},
		{name: "output_bound", failure: "output_bound", wantState: "failed", wantCode: "output_limit"},
		{name: "event_bound", failure: "event_bound", wantState: "failed", wantCode: "event_limit"},
		{name: "transport_ended", failure: "transport_ended", wantState: "failed", wantCode: "execution_failed"},
		{name: "turn_failed", failure: "turn_failed", wantState: "failed", wantCode: "execution_failed"},
		{name: "ownership_lost", failure: "ownership_lost", wantState: "failed", wantCode: "authority_revoked"},
		{name: "unknown_native", failure: "private_native_detail", wantState: "failed", wantCode: "execution_failed"},
		{name: "deadline_after_start", failure: "deadline_exceeded", wantState: "cancelled", wantCode: "deadline_exceeded"},
		{name: "preflight_without_process", wantState: "failed", wantCode: "runtime_unavailable", launchFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := openConversationRouteFixture(t)
			server := httptest.NewServer(f.router)
			defer server.Close()
			authority := integrationConversationAuthority(t, f, server.URL)
			launcher := &scriptedIntegrationLauncher{result: lifecycleclient.ConversationExecutionResult{
				Outcome: "failed", Failure: test.failure, ThreadID: "failure-thread", TurnID: "failure-turn",
			}}
			if test.launchFail {
				launcher.launchErr = errors.New("synthetic preflight failure")
			}
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			runner, err := lifecycleclient.NewConversationRunner(directory, f.runtime.Generation, authority, launcher)
			if err != nil {
				t.Fatal(err)
			}
			request := f.callRequest("failure-"+test.name, "failure-"+test.name, "chat")
			call := submitIntegrationConversation(t, server.Client(), server.URL, f, request)
			if err := runner.Step(context.Background(), f.runtime); err != nil {
				t.Fatalf("runner terminal report: %v", err)
			}

			base := fmt.Sprintf("/api/projects/%d/conversation/v1", f.projectID)
			response := f.request(t, http.MethodGet, base+"/calls/"+call.CallID, nil, f.serviceKey, true)
			terminalCall := decodeResponse[conversationturns.Call](t, response)
			if response.Code != http.StatusOK || terminalCall.State != test.wantState {
				t.Fatalf("terminal status=%d call=%+v", response.Code, terminalCall)
			}
			if test.wantState == "failed" && terminalCall.ErrorCode != test.wantCode {
				t.Fatalf("terminal error_code=%q want=%q", terminalCall.ErrorCode, test.wantCode)
			}
			pageResponse := f.request(t, http.MethodGet, base+"/calls/"+call.CallID+"/events?after=0", nil, f.serviceKey, true)
			page := decodeResponse[conversationturns.EventsPage](t, pageResponse)
			if pageResponse.Code != http.StatusOK || len(page.Events) == 0 {
				t.Fatalf("terminal events status=%d page=%+v", pageResponse.Code, page)
			}
			last := page.Events[len(page.Events)-1]
			if last.Kind != test.wantState || last.ErrorCode != test.wantCode {
				t.Fatalf("terminal events status=%d page=%+v", pageResponse.Code, page)
			}
			launches, waits, _ := launcher.snapshot()
			if launches != 1 || test.launchFail && waits != 0 || !test.launchFail && waits != 1 {
				t.Fatalf("process proof launches=%d waits=%d launch_fail=%t", launches, waits, test.launchFail)
			}
			var released int
			if err := db.DB.QueryRow(`SELECT released_at IS NOT NULL FROM conversation_account_slots WHERE call_id=?`, call.CallID).Scan(&released); err != nil || released != 1 {
				t.Fatalf("slot release=%d err=%v", released, err)
			}
			next := f.callRequest("failure-next-"+test.name, "failure-next-"+test.name, "chat")
			nextCall := submitIntegrationConversation(t, server.Client(), server.URL, f, next)
			cleanup := f.request(t, http.MethodPost, base+"/calls/"+nextCall.CallID+"/cancel", struct{}{}, f.serviceKey, true)
			if cleanup.Code != http.StatusOK || decodeResponse[conversationturns.Call](t, cleanup).State != "cancelled" {
				t.Fatalf("account reuse cleanup status=%d body=%s", cleanup.Code, cleanup.Body.String())
			}
		})
	}
}

func TestConversationLifecycleClientServerCancelWinsAfterFinalControl(t *testing.T) {
	f := openConversationRouteFixture(t)
	server := httptest.NewServer(f.router)
	defer server.Close()
	baseAuthority := integrationConversationAuthority(t, f, server.URL)
	launcher := &scriptedIntegrationLauncher{result: lifecycleclient.ConversationExecutionResult{
		Outcome: "completed", ThreadID: "boundary-thread", TurnID: "boundary-turn",
	}}
	request := f.callRequest("cancel-final-boundary", "cancel-final-boundary", "chat")
	call := submitIntegrationConversation(t, server.Client(), server.URL, f, request)
	base := fmt.Sprintf("/api/projects/%d/conversation/v1", f.projectID)
	authority := &cancelAfterFinalControlAuthority{base: baseAuthority}
	authority.cancel = func() {
		response := f.request(t, http.MethodPost, base+"/calls/"+call.CallID+"/cancel", struct{}{}, f.serviceKey, true)
		if response.Code != http.StatusOK || decodeResponse[conversationturns.Call](t, response).State != "cancel_requested" {
			t.Fatalf("boundary cancel status=%d body=%s", response.Code, response.Body.String())
		}
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := lifecycleclient.NewConversationRunner(directory, f.runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Step(context.Background(), f.runtime); err != nil {
		t.Fatalf("runner cancellation reconciliation: %v", err)
	}

	response := f.request(t, http.MethodGet, base+"/calls/"+call.CallID, nil, f.serviceKey, true)
	terminalCall := decodeResponse[conversationturns.Call](t, response)
	if response.Code != http.StatusOK || terminalCall.State != "cancelled" || terminalCall.OutputText != "" {
		t.Fatalf("terminal status=%d call=%+v", response.Code, terminalCall)
	}
	pageResponse := f.request(t, http.MethodGet, base+"/calls/"+call.CallID+"/events?after=0", nil, f.serviceKey, true)
	page := decodeResponse[conversationturns.EventsPage](t, pageResponse)
	if pageResponse.Code != http.StatusOK || len(page.Events) != 2 || page.Events[0].Kind != "started" || page.Events[1].Kind != "cancelled" {
		t.Fatalf("immutable cancellation sequence status=%d page=%+v", pageResponse.Code, page)
	}
	claim := page.Events[0]
	replay := lifecycleclient.ConversationEvent{
		Sequence: 2, Kind: "cancelled", ThreadID: claim.ThreadID, TurnID: claim.TurnID, ErrorCode: "cancelled",
	}
	if _, err := baseAuthority.ReportConversationEvent(context.Background(), f.runtime.ID, f.runtime.Generation,
		call.CallID, launcher.claimSnapshot().ExecutionGeneration, replay); err != nil {
		t.Fatalf("cancelled replay: %v", err)
	}
	emptyDigest := sha256.Sum256(nil)
	if _, err := baseAuthority.ReportConversationEvent(context.Background(), f.runtime.ID, f.runtime.Generation,
		call.CallID, launcher.claimSnapshot().ExecutionGeneration, lifecycleclient.ConversationEvent{
			Sequence: 2, Kind: "completed", ThreadID: claim.ThreadID, TurnID: claim.TurnID,
			OutputSHA256: hex.EncodeToString(emptyDigest[:]),
		}); !errors.Is(err, lifecycleintents.ErrConflict) {
		t.Fatalf("completion replaced accepted cancellation err=%v", err)
	}
	launches, waits, _ := launcher.snapshot()
	if launches != 1 || waits != 1 {
		t.Fatalf("unexpected relaunch/re-adoption launches=%d waits=%d", launches, waits)
	}
	var released int
	if err := db.DB.QueryRow(`SELECT released_at IS NOT NULL FROM conversation_account_slots WHERE call_id=?`, call.CallID).Scan(&released); err != nil || released != 1 {
		t.Fatalf("slot release=%d err=%v", released, err)
	}
	next := f.callRequest("cancel-final-boundary-next", "cancel-final-boundary-next", "chat")
	nextCall := submitIntegrationConversation(t, server.Client(), server.URL, f, next)
	cleanup := f.request(t, http.MethodPost, base+"/calls/"+nextCall.CallID+"/cancel", struct{}{}, f.serviceKey, true)
	if cleanup.Code != http.StatusOK || decodeResponse[conversationturns.Call](t, cleanup).State != "cancelled" {
		t.Fatalf("account reuse cleanup status=%d body=%s", cleanup.Code, cleanup.Body.String())
	}
}

func TestAithemaPaimosProviderAgainstProductionRouter(t *testing.T) {
	source := os.Getenv("AITHEMA_CONTRACT_SOURCE")
	if source == "" {
		t.Skip("set AITHEMA_CONTRACT_SOURCE to an absolute Aithema checkout for the cross-language oracle")
	}
	if !filepath.IsAbs(source) {
		t.Fatalf("AITHEMA_CONTRACT_SOURCE must be absolute: %q", source)
	}
	providerSource := filepath.Join(source, "runtime", "paimos-provider.js")
	if info, err := os.Stat(providerSource); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("Aithema provider source unavailable: %v", err)
	}

	f := openConversationRouteFixture(t)
	server := httptest.NewServer(f.router)
	defer server.Close()
	runner, launcher := newIntegrationConversationRunner(t, f, server.URL)
	workerContext, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() {
		for {
			if workerContext.Err() != nil {
				workerDone <- nil
				return
			}
			if err := runner.Step(workerContext, f.runtime); err != nil && workerContext.Err() == nil {
				workerDone <- err
				return
			}
			if len(launcher.snapshot()) >= 2 {
				workerDone <- nil
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	defer stopWorker()

	credentialFile := filepath.Join(t.TempDir(), "conversation.key")
	if err := os.WriteFile(credentialFile, []byte(f.serviceKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `
import { pathToFileURL } from 'node:url';
import { join } from 'node:path';
const [source, origin, credentialFile, projectID, bindingID, issuer, subject] = process.argv.slice(2);
const { PaimosHarnessProvider } = await import(pathToFileURL(join(source, 'runtime', 'paimos-provider.js')).href);
const provider = new PaimosHarnessProvider({
  id: 'paimos-contract', origin, credentialFile, projectID, bindingID,
  bindingRevision: 1, trustedIssuer: issuer, modelId: 'codex-sol-high',
  allowedModels: ['codex-sol-high'], executionLocation: 'cloud',
  allowedDataClasses: ['confidential'], mode: 'test', pollIntervalMs: 5,
  retryDelayMs: 5, cleanupTimeoutMs: 200, limits: { maxDurationMs: 3000 },
});
const actor = Object.freeze({
  party_ref: 'party:synthetic', actor_kind: 'human',
  roles: Object.freeze(['requirements_approver']), subject, projects: Object.freeze([]),
});
const request = (purpose) => ({
  system: 'Synthetic integration context.',
  messages: [{ role: 'user', content: 'Return the synthetic oracle response.' }],
  model: 'codex-sol-high',
  executionContext: {
    actor, projectRef: 'aithema-project-1', conversationId: 'cross-language-' + purpose,
    turnId: 'turn-' + purpose, purpose, requestId: 'cross-language-' + purpose,
  },
});
let chat = '';
for await (const chunk of provider.streamChat(request('chat'))) chat += chunk;
if (chat !== 'Synthetic integration answer.') throw new Error('chat oracle mismatch');
const understood = await provider.understand(request('understand'));
if (understood.summary !== 'Synthetic integration understanding') throw new Error('understanding oracle mismatch');
`
	commandContext, cancelCommand := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelCommand()
	command := exec.CommandContext(commandContext, "node", "--input-type=module", "-",
		source, server.URL, credentialFile, strconv.FormatInt(f.projectID, 10), f.bindingID, f.actor.Issuer, f.actor.Subject)
	command.Stdin = strings.NewReader(script)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Aithema provider oracle: %v\n%s", err, output)
	}
	stopWorker()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("conversation runner: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("conversation runner did not finish")
	}
	claims := launcher.snapshot()
	if len(claims) != 2 || claims[0].Purpose != "chat" || claims[1].Purpose != "understand" {
		t.Fatalf("cross-language claims=%+v", claims)
	}
}
