// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxProviderEventBytes     = 256 << 10
	maxProviderStreamBytes    = 32 << 20
	maxProviderEvents         = 100_000
	maxVisibleOutputBytes     = 1 << 20
	maxCapturedLogBytes       = 8 << 20
	maxTelemetryActivityBytes = 280
	maxTelemetryBasisBytes    = 240
)

type supervisorOutcome string

const (
	outcomeNormalExit           supervisorOutcome = "normal_exit"
	outcomeSpawnFailure         supervisorOutcome = "spawn_failure"
	outcomeMalformedStream      supervisorOutcome = "malformed_stream"
	outcomeSilentChild          supervisorOutcome = "silent_child"
	outcomeTimeout              supervisorOutcome = "timeout"
	outcomeCancellation         supervisorOutcome = "cancellation"
	outcomeServerCancellation   supervisorOutcome = "server_cancellation"
	outcomeOperatorCancellation supervisorOutcome = "operator_cancellation"
	outcomeProviderFailure      supervisorOutcome = "provider_failure"
	outcomeRunnerDisappearance  supervisorOutcome = "runner_disappearance"
	outcomeReportFailure        supervisorOutcome = "report_failure"
	outcomeTerminationFailure   supervisorOutcome = "termination_failure"
)

type supervisorReport struct {
	Event              string
	Phase              string
	Outcome            supervisorOutcome
	Summary            string
	NeedsInput         bool
	BlockerState       string
	ProgressPercent    *float64
	ETASeconds         *int64
	ETAMinSeconds      *int64
	ETAMaxSeconds      *int64
	EstimateSource     string
	EstimateConfidence *float64
	EstimateBasis      string
}

type runnerReportTransport interface {
	Report(context.Context, int64, supervisorReport) error
}

var (
	errRunnerDisappeared = errors.New("runner disappeared")
	errRunCancelled      = errors.New("run cancelled")
	errRunStatusLost     = errors.New("run ownership or status lost")
)

type runnerTelemetryState struct {
	sequence         int64
	estimateRevision int64
	correlationID    string
	pending          *runnerPendingTelemetry
}

type runnerPendingTelemetry struct {
	fact runTelemetryReport
	body []byte
}

// httpRunnerReportTransport maps supervisor facts directly onto the PAI-799
// append-only wire contract. It owns correlation and sequence so provider
// callbacks never control ordering or inject arbitrary payloads.
type httpRunnerReportTransport struct {
	client   *Client
	provider string
	adapter  string
	mu       sync.Mutex
	states   map[int64]*runnerTelemetryState
}

func (h *httpRunnerReportTransport) Report(ctx context.Context, runID int64, report supervisorReport) error {
	hasEstimate := supervisorReportHasEstimate(report)
	if hasEstimate && (report.EstimateConfidence == nil || strings.TrimSpace(report.EstimateBasis) == "" || strings.TrimSpace(report.EstimateSource) == "") {
		return errors.New("supervisor estimate lacks source, confidence, or basis")
	}
	// Heartbeats and structured callbacks can originate in different goroutines.
	// Serialize allocation and delivery so the server observes sequence order.
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.stateLocked(runID)
	if err != nil {
		return err
	}
	// A prior call may have timed out after the server accepted its request but
	// before the response arrived. Flush that exact body first; never reuse its
	// sequence for a different semantic fact.
	if state.pending != nil {
		if err := h.deliverPendingLocked(ctx, runID, state); err != nil {
			return err
		}
	}
	nextSequence := state.sequence + 1
	nextEstimateRevision := state.estimateRevision
	fact := runTelemetryReport{
		Sequence: nextSequence, CorrelationID: state.correlationID, Provider: h.resolvedProvider(),
		Adapter: h.resolvedAdapter(), AgentReportedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Kind: "phase", Phase: allowedSupervisorPhase(report.Phase),
		Activity:   safeReportValue(report.Summary, maxTelemetryActivityBytes),
		NeedsInput: report.NeedsInput, BlockerState: report.BlockerState,
	}
	if fact.BlockerState == "" {
		fact.BlockerState = "none"
	}
	switch report.Event {
	case "liveness", "heartbeat":
		fact.Kind = "heartbeat"
		fact.Heartbeat = true
		fact.Activity = ""
	case "needs_input":
		fact.Kind = "needs_input"
		fact.Phase = "waiting"
		fact.NeedsInput = true
		fact.BlockerState = "input"
	case "result":
		if report.Outcome == outcomeNormalExit {
			fact.Kind = "phase"
			fact.Phase = "reviewing"
			fact.Activity = safeReportValue(report.Summary, maxTelemetryActivityBytes)
		} else {
			fact.Kind = "blocker"
			fact.Phase = "unknown"
			fact.BlockerState = supervisorOutcomeBlocker(report.Outcome)
		}
	}
	if hasEstimate {
		nextEstimateRevision++
		fact.Kind = "progress"
		fact.EstimateRevision = &nextEstimateRevision
		fact.ProgressPercent = report.ProgressPercent
		fact.ETASeconds = report.ETASeconds
		fact.ETAMinSeconds = report.ETAMinSeconds
		fact.ETAMaxSeconds = report.ETAMaxSeconds
		fact.EstimateSource = report.EstimateSource
		fact.EstimateConfidence = report.EstimateConfidence
		fact.EstimateBasis = safeReportValue(report.EstimateBasis, maxTelemetryBasisBytes)
	}
	body, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("encode telemetry: %w", err)
	}
	state.pending = &runnerPendingTelemetry{fact: fact, body: body}
	return h.deliverPendingLocked(ctx, runID, state)
}

func (h *httpRunnerReportTransport) Identity(runID int64) (string, string, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.stateLocked(runID)
	if err != nil {
		return "", "", "", err
	}
	return state.correlationID, h.resolvedProvider(), h.resolvedAdapter(), nil
}

func (h *httpRunnerReportTransport) stateLocked(runID int64) (*runnerTelemetryState, error) {
	if h.states == nil {
		h.states = map[int64]*runnerTelemetryState{}
	}
	state := h.states[runID]
	if state == nil {
		correlationID, err := newRunCorrelationID()
		if err != nil {
			return nil, err
		}
		state = &runnerTelemetryState{correlationID: correlationID}
		h.states[runID] = state
	}
	return state, nil
}

func newRunCorrelationID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate run correlation id: %w", err)
	}
	return id.String(), nil
}

func (h *httpRunnerReportTransport) deliverPendingLocked(ctx context.Context, runID int64, state *runnerTelemetryState) error {
	for state.pending != nil {
		err := postRunTelemetryBody(ctx, h.client, runID, state.pending.body)
		if err == nil {
			state.sequence = state.pending.fact.Sequence
			if state.pending.fact.EstimateRevision != nil {
				state.estimateRevision = *state.pending.fact.EstimateRevision
			}
			state.pending = nil
			return nil
		}
		var httpErr *httpError
		if errors.As(err, &httpErr) {
			switch {
			case httpErr.Code == http.StatusConflict:
				return h.classifyConflict(ctx, runID)
			case httpErr.Code == http.StatusNotFound || httpErr.Code == http.StatusGone:
				return fmt.Errorf("%w: run no longer exists", errRunnerDisappeared)
			case httpErr.Code < 500:
				return err
			}
		}
		// Network errors, unreadable responses, and 5xx responses are ambiguous:
		// the server may have appended the fact. Retry the immutable body until
		// acceptance/duplicate confirmation or the caller's deadline.
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("telemetry delivery remained ambiguous: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return nil
}

func (h *httpRunnerReportTransport) classifyConflict(ctx context.Context, runID int64) error {
	status, err := fetchRunStatusContext(ctx, h.client, runID)
	if err != nil {
		var httpErr *httpError
		if errors.As(err, &httpErr) && (httpErr.Code == http.StatusNotFound || httpErr.Code == http.StatusGone) {
			return fmt.Errorf("%w: run disappeared after telemetry conflict", errRunnerDisappeared)
		}
		return fmt.Errorf("refetch run after telemetry conflict: %w", err)
	}
	if status == "cancelled" {
		return fmt.Errorf("%w: server reports cancelled", errRunCancelled)
	}
	return fmt.Errorf("%w: server reports %s", errRunStatusLost, status)
}

func supervisorReportHasEstimate(report supervisorReport) bool {
	return report.ProgressPercent != nil || report.ETASeconds != nil ||
		report.ETAMinSeconds != nil || report.ETAMaxSeconds != nil
}

func (h *httpRunnerReportTransport) resolvedProvider() string {
	if strings.TrimSpace(h.provider) == "" {
		return "paimos"
	}
	return h.provider
}

func (h *httpRunnerReportTransport) resolvedAdapter() string {
	if strings.TrimSpace(h.adapter) == "" {
		return "run-agent"
	}
	return h.adapter
}

func allowedSupervisorPhase(phase string) string {
	switch phase {
	case "starting", "planning", "implementing", "testing", "reviewing", "deploying", "waiting", "completed":
		return phase
	default:
		return "unknown"
	}
}

func supervisorOutcomeBlocker(outcome supervisorOutcome) string {
	switch outcome {
	case outcomeCancellation, outcomeServerCancellation, outcomeOperatorCancellation:
		return "external"
	case outcomeRunnerDisappearance, outcomeReportFailure:
		return "external"
	case outcomeSpawnFailure, outcomeSilentChild, outcomeTimeout:
		return "environment"
	default:
		return "unknown"
	}
}

func safeReportValue(value string, max int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if value == "" {
		return ""
	}
	return truncateUTF8Bytes(value, max)
}

func truncateUTF8Bytes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	const suffix = "..."
	if max <= len(suffix) {
		cut := max
		for cut > 0 && !utf8.RuneStart(value[cut]) {
			cut--
		}
		return value[:cut]
	}
	cut := max - len(suffix)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + suffix
}

type supervisorRequest struct {
	RunID             int64
	RepoRoot          string
	ExecCmd           string
	ExecArgv          []string
	Env               []string
	StructuredClaude  bool
	ExecutionTimeout  time.Duration
	SilenceTimeout    time.Duration
	HeartbeatInterval time.Duration
	LogSink           io.Writer
	Reporter          runnerReportTransport
	InitialPhase      string
	StartSummary      string
	// OwnedProcessStarted runs only after Start and an exact process-group
	// ownership proof. The bool is false on unsupported platforms, where the
	// production runner must advertise no executor action and issue no lease.
	OwnedProcessStarted func(context.Context, bool) error
	ControlRequests     <-chan runnerClaimedCancellation
	ControlResult       func(context.Context, runnerClaimedCancellation, string, string) error
}

type supervisorResult struct {
	Outcome supervisorOutcome
	Summary string
}

type safeProviderProgress struct {
	phase        string
	summary      string
	needsInput   bool
	blockerState string
}

type providerStreamAdapter interface {
	Consume([]byte) (*safeProviderProgress, error)
	Result() (seen bool, failed bool)
}

type claudeStreamAdapter struct {
	seenResult   bool
	resultFailed bool
}

type claudeStreamEnvelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
}

func (a *claudeStreamAdapter) Consume(line []byte) (*safeProviderProgress, error) {
	var event claudeStreamEnvelope
	if err := json.Unmarshal(line, &event); err != nil {
		return nil, errors.New("invalid provider JSON event")
	}
	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			return &safeProviderProgress{phase: "starting", summary: "provider session started"}, nil
		}
	case "assistant":
		for _, block := range event.Message.Content {
			if block.Type != "tool_use" {
				continue
			}
			switch block.Name {
			case "Read", "Glob", "Grep":
				return &safeProviderProgress{phase: "planning", summary: "Provider is inspecting the repository"}, nil
			case "Edit", "Write", "NotebookEdit":
				return &safeProviderProgress{phase: "implementing", summary: "Provider is editing the repository"}, nil
			case "Bash":
				return &safeProviderProgress{phase: "testing", summary: "Provider is running an allowlisted command step"}, nil
			case "AskUserQuestion", "SendUserMessage":
				return &safeProviderProgress{phase: "waiting", summary: "Provider requested operator input", needsInput: true, blockerState: "input"}, nil
			default:
				return &safeProviderProgress{phase: "implementing", summary: "Provider is working"}, nil
			}
		}
	case "result":
		a.seenResult = true
		a.resultFailed = event.IsError || (event.Subtype != "" && event.Subtype != "success")
		if a.resultFailed {
			return &safeProviderProgress{phase: "unknown", summary: "Provider reported failure", blockerState: "unknown"}, nil
		}
		return &safeProviderProgress{phase: "reviewing", summary: "Provider exited normally; verification is pending"}, nil
	case "user", "stream_event", "rate_limit_event", "tool_progress", "tool_use_summary", "auth_status", "prompt_suggestion":
		// These event bodies can contain prompts, tool arguments, command output,
		// source text, or provider details. Activity is observed, but no body is
		// translated into telemetry.
	default:
		return nil, fmt.Errorf("unknown provider event type %q", truncateRunes(event.Type, 32))
	}
	return nil, nil
}

func (a *claudeStreamAdapter) Result() (bool, bool) {
	return a.seenResult, a.resultFailed
}

type outputBudgetWriter struct {
	mu        sync.Mutex
	dst       io.Writer
	remaining int64
	truncated bool
}

func newOutputBudgetWriter(dst io.Writer, max int64) *outputBudgetWriter {
	return &outputBudgetWriter{dst: dst, remaining: max}
}

func (w *outputBudgetWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dst == nil || w.remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	n := int64(len(p))
	if n > w.remaining {
		n = w.remaining
		w.truncated = true
	}
	if n > 0 {
		_, _ = w.dst.Write(p[:n])
		w.remaining -= n
	}
	return len(p), nil
}

func superviseAgentProcess(ctx context.Context, req supervisorRequest) supervisorResult {
	if strings.TrimSpace(req.ExecCmd) == "" && len(req.ExecArgv) == 0 {
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider command is empty"}
	}
	var cmd *exec.Cmd
	if len(req.ExecArgv) > 0 {
		if strings.TrimSpace(req.ExecArgv[0]) == "" {
			return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider command is empty"}
		}
		// Built-in adapters execute their validated argv directly. Only the
		// deliberate raw --exec escape hatch is interpreted by a shell.
		cmd = exec.Command(req.ExecArgv[0], req.ExecArgv[1:]...) // #nosec G204 -- validated built-in argv
	} else {
		cmd = exec.Command("sh", "-c", req.ExecCmd) // #nosec G204 -- operator-owned raw command
	}
	cmd.Dir = req.RepoRoot
	cmd.Env = append(os.Environ(), req.Env...)
	groupConfigured := configureProcessGroup(cmd)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider stdout could not be opened"}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider stderr could not be opened"}
	}
	promptCommand := req.ExecCmd
	if len(req.ExecArgv) > 0 {
		promptCommand = strings.Join(req.ExecArgv, " ")
	}
	if prompt, promptErr := promptForCommand(promptCommand, req.Env); promptErr != nil {
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider prompt could not be read"}
	} else if prompt != "" {
		cmd.Stdin = strings.NewReader(prompt)
	}
	if err := cmd.Start(); err != nil {
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider process could not be started"}
	}

	activity := make(chan struct{}, 1)
	progress := make(chan safeProviderProgress, 16)
	streamErrors := make(chan error, 2)
	waited := make(chan error, 1)
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	visibleStdout := newOutputBudgetWriter(stdout, maxVisibleOutputBytes)
	visibleStderr := newOutputBudgetWriter(stderr, maxVisibleOutputBytes)
	logWriter := newOutputBudgetWriter(req.LogSink, maxCapturedLogBytes)
	var adapter providerStreamAdapter
	if req.StructuredClaude {
		adapter = &claudeStreamAdapter{}
	}
	go func() {
		consumeProviderStdout(stdoutPipe, adapter, visibleStdout, logWriter, activity, progress, streamErrors)
		close(stdoutDone)
	}()
	go func() {
		consumeProviderStderr(stderrPipe, visibleStderr, logWriter, activity, streamErrors)
		close(stderrDone)
	}()
	go func() {
		// StdoutPipe and StderrPipe require every read to finish before Wait:
		// Wait closes both descriptors after observing process exit. Starting it
		// concurrently with the scanners can therefore discard the child's final
		// structured line. Pipe EOF is delivered by the kernel when the process
		// exits, so waiting for both consumers first has no polling or sleep and
		// leaves exactly one goroutine responsible for reaping the child.
		<-stdoutDone
		<-stderrDone
		waited <- cmd.Wait()
	}()
	owned := verifyOwnedProcess(cmd, groupConfigured) == nil
	if groupConfigured && !owned {
		_ = signalOwnedProcess(cmd, true)
		<-waited
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider process ownership could not be verified"}
	}
	if req.OwnedProcessStarted != nil {
		startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		startErr := req.OwnedProcessStarted(startCtx, owned)
		cancel()
		if startErr != nil {
			if stopErr := terminateSupervisedProcess(cmd, waited); stopErr != nil {
				return supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"}
			}
			return supervisorResult{Outcome: outcomeReportFailure, Summary: "runner control could not start"}
		}
	}

	currentPhase := allowedSupervisorPhase(req.InitialPhase)
	if currentPhase == "unknown" {
		currentPhase = "starting"
	}
	report := func(fact supervisorReport) error {
		if req.Reporter == nil {
			return nil
		}
		reportCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return req.Reporter.Report(reportCtx, req.RunID, fact)
	}
	if strings.TrimSpace(req.StartSummary) != "" {
		if err := report(supervisorReport{Event: "progress", Phase: currentPhase, Summary: req.StartSummary}); err != nil {
			if stopErr := terminateSupervisedProcess(cmd, waited); stopErr != nil {
				return supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"}
			}
			return reportErrorResult(err)
		}
	}
	if err := report(supervisorReport{Event: "liveness", Phase: currentPhase}); err != nil {
		if stopErr := terminateSupervisedProcess(cmd, waited); stopErr != nil {
			return supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"}
		}
		return reportErrorResult(err)
	}

	executionTimer := time.NewTimer(req.ExecutionTimeout)
	defer executionTimer.Stop()
	silenceTimer := time.NewTimer(req.SilenceTimeout)
	defer silenceTimer.Stop()
	heartbeat := time.NewTicker(req.HeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case cancellation := <-req.ControlRequests:
			stopErr := terminateSupervisedProcess(cmd, waited)
			resultCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if stopErr != nil {
				resultErr := error(nil)
				if req.ControlResult != nil {
					resultErr = req.ControlResult(resultCtx, cancellation, "rejected", "process_termination_failed")
				}
				cancel()
				if resultErr != nil {
					return supervisorResult{Outcome: outcomeReportFailure, Summary: "runner control result could not be recorded"}
				}
				return supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"}
			}
			resultErr := error(nil)
			if req.ControlResult != nil {
				resultErr = req.ControlResult(resultCtx, cancellation, "applied", "")
			}
			cancel()
			if resultErr != nil {
				return supervisorResult{Outcome: outcomeReportFailure, Summary: "runner control result could not be recorded"}
			}
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeOperatorCancellation, Summary: "operator cancelled the running process"})
		case <-ctx.Done():
			if err := terminateSupervisedProcess(cmd, waited); err != nil {
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"})
			}
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeCancellation, Summary: "runner cancelled the child process"})
		case <-executionTimer.C:
			if err := terminateSupervisedProcess(cmd, waited); err != nil {
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"})
			}
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeTimeout, Summary: "provider exceeded the execution timeout"})
		case <-silenceTimer.C:
			if err := terminateSupervisedProcess(cmd, waited); err != nil {
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"})
			}
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeSilentChild, Summary: "provider produced no activity before the heartbeat timeout"})
		case <-activity:
			resetTimer(silenceTimer, req.SilenceTimeout)
		case p := <-progress:
			currentPhase = p.phase
			event := "progress"
			if p.needsInput {
				event = "needs_input"
			}
			if err := report(supervisorReport{Event: event, Phase: p.phase, Summary: p.summary, NeedsInput: p.needsInput, BlockerState: p.blockerState}); err != nil {
				if stopErr := terminateSupervisedProcess(cmd, waited); stopErr != nil {
					return supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"}
				}
				return reportErrorResult(err)
			}
			if p.needsInput {
				if err := terminateSupervisedProcess(cmd, waited); err != nil {
					return finishSupervisorResult(req, supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"})
				}
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeProviderFailure, Summary: "provider requested input; this runner is one-shot"})
			}
		case err := <-streamErrors:
			if stopErr := terminateSupervisedProcess(cmd, waited); stopErr != nil {
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"})
			}
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeMalformedStream, Summary: safeStreamErrorSummary(err)})
		case <-heartbeat.C:
			if err := report(supervisorReport{Event: "heartbeat", Phase: currentPhase}); err != nil {
				if stopErr := terminateSupervisedProcess(cmd, waited); stopErr != nil {
					return supervisorResult{Outcome: outcomeTerminationFailure, Summary: "provider process could not be terminated"}
				}
				return reportErrorResult(err)
			}
		case waitErr := <-waited:
			// Both stream consumers have reached EOF before Wait is allowed to run.
			// The scanner gives a final needs-input fact priority over ordinary
			// progress; drain it before classifying the exit so the blocker cannot
			// disappear in the wait/progress select race.
			select {
			case p := <-progress:
				if p.needsInput {
					if err := report(supervisorReport{Event: "needs_input", Phase: p.phase, Summary: p.summary, NeedsInput: true, BlockerState: p.blockerState}); err != nil {
						return reportErrorResult(err)
					}
					return finishSupervisorResult(req, supervisorResult{Outcome: outcomeProviderFailure, Summary: "provider requested input; this runner is one-shot"})
				}
			default:
			}
			if ctx.Err() != nil {
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeCancellation, Summary: "runner cancelled the child process"})
			}
			select {
			case <-executionTimer.C:
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeTimeout, Summary: "provider exceeded the execution timeout"})
			default:
			}
			select {
			case <-silenceTimer.C:
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeSilentChild, Summary: "provider produced no activity before the heartbeat timeout"})
			default:
			}
			select {
			case streamErr := <-streamErrors:
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeMalformedStream, Summary: safeStreamErrorSummary(streamErr)})
			default:
			}
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) && (exitErr.ExitCode() == 126 || exitErr.ExitCode() == 127) {
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider command could not be executed"})
			}
			if adapter != nil {
				seen, failed := adapter.Result()
				if !seen {
					return finishSupervisorResult(req, supervisorResult{Outcome: outcomeMalformedStream, Summary: "provider stream ended without a result event"})
				}
				if failed {
					return finishSupervisorResult(req, supervisorResult{Outcome: outcomeProviderFailure, Summary: "provider reported failure"})
				}
			}
			if waitErr != nil {
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeProviderFailure, Summary: "provider process exited unsuccessfully"})
			}
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeNormalExit, Summary: "provider process exited normally"})
		}
	}
}

const (
	processGracefulStopTimeout = 2 * time.Second
	processForcedStopTimeout   = 5 * time.Second
)

// terminateSupervisedProcess accounts for every signal and always waits for
// the process-tree owner to be reaped. It has no unconditional sleep: an early
// TERM exit wins immediately, otherwise the bounded grace period advances to
// KILL and a second bounded wait.
func terminateSupervisedProcess(cmd *exec.Cmd, waited <-chan error) error {
	select {
	case <-waited:
		return nil
	default:
	}
	termErr := signalOwnedProcess(cmd, false)
	grace := time.NewTimer(processGracefulStopTimeout)
	defer grace.Stop()
	select {
	case <-waited:
		return nil
	case <-grace.C:
	}
	if err := signalOwnedProcess(cmd, true); err != nil {
		if termErr != nil {
			return errors.Join(termErr, err)
		}
		return err
	}
	forced := time.NewTimer(processForcedStopTimeout)
	defer forced.Stop()
	select {
	case <-waited:
		return nil
	case <-forced.C:
		return errors.New("owned process did not exit after forced termination")
	}
}

func finishSupervisorResult(req supervisorRequest, result supervisorResult) supervisorResult {
	if req.Reporter == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := req.Reporter.Report(ctx, req.RunID, supervisorReport{
		Event: "result", Phase: map[bool]string{true: "reviewing", false: "unknown"}[result.Outcome == outcomeNormalExit], Outcome: result.Outcome, Summary: result.Summary,
	})
	if err != nil {
		return reportErrorResult(err)
	}
	return result
}

func consumeProviderStdout(src io.Reader, adapter providerStreamAdapter, visible, log io.Writer, activity chan<- struct{}, progress chan safeProviderProgress, failures chan<- error) {
	if adapter == nil {
		consumeRawProviderOutput(src, io.MultiWriter(visible, log), activity, failures)
		return
	}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64<<10), maxProviderEventBytes)
	var total, events int
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		total += len(line)
		events++
		notifyActivity(activity)
		_, _ = log.Write(line)
		_, _ = log.Write([]byte("\n"))
		if total > maxProviderStreamBytes || events > maxProviderEvents {
			notifyFailure(failures, errors.New("provider stream exceeded its bounded budget"))
			return
		}
		p, err := adapter.Consume(line)
		if err != nil {
			notifyFailure(failures, err)
			return
		}
		if p != nil {
			enqueueProviderProgress(progress, *p)
			if p.needsInput {
				// A one-shot runner cannot continue past a human/permission prompt.
				// Stop consuming after publishing the lossless priority fact; the
				// supervisor will terminate and reap the owned process tree.
				return
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) && !strings.Contains(err.Error(), "file already closed") {
		notifyFailure(failures, errors.New("provider event exceeded its bounded size"))
	}
}

// enqueueProviderProgress keeps ordinary high-volume phase chatter lossy, but
// makes the safety-critical needs-input fact lossless. It evicts queued
// ordinary progress before inserting that single priority fact, so a provider
// burst cannot hide a human/permission blocker or deadlock its stdout pipe.
func enqueueProviderProgress(progress chan safeProviderProgress, p safeProviderProgress) {
	if !p.needsInput {
		select {
		case progress <- p:
		default:
		}
		return
	}
	for {
		select {
		case <-progress:
			continue
		default:
			progress <- p
			return
		}
	}
}

func consumeProviderStderr(src io.Reader, visible, log io.Writer, activity chan<- struct{}, failures chan<- error) {
	consumeRawProviderOutput(src, io.MultiWriter(visible, log), activity, failures)
}

func consumeRawProviderOutput(src io.Reader, dst io.Writer, activity chan<- struct{}, failures chan<- error) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			notifyActivity(activity)
			_, _ = dst.Write(buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				notifyFailure(failures, errors.New("provider output read failed"))
			}
			return
		}
	}
}

func notifyActivity(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func notifyFailure(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func safeStreamErrorSummary(err error) string {
	if err != nil && strings.Contains(err.Error(), "budget") {
		return "provider stream exceeded its bounded budget"
	}
	if err != nil && strings.Contains(err.Error(), "size") {
		return "provider event exceeded its bounded size"
	}
	return "provider emitted a malformed structured stream"
}

func reportErrorResult(err error) supervisorResult {
	if errors.Is(err, errRunCancelled) {
		return supervisorResult{Outcome: outcomeServerCancellation, Summary: "server cancelled the run while child process was alive"}
	}
	if errors.Is(err, errRunnerDisappeared) {
		return supervisorResult{Outcome: outcomeRunnerDisappearance, Summary: "run disappeared while child process was alive"}
	}
	if errors.Is(err, errRunStatusLost) {
		return supervisorResult{Outcome: outcomeRunnerDisappearance, Summary: "run ownership or status changed while child process was alive"}
	}
	return supervisorResult{Outcome: outcomeReportFailure, Summary: "supervisor could not report liveness"}
}
