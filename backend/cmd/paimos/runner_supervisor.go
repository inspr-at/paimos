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
)

const (
	maxProviderEventBytes  = 256 << 10
	maxProviderStreamBytes = 32 << 20
	maxProviderEvents      = 100_000
	maxVisibleOutputBytes  = 1 << 20
	maxCapturedLogBytes    = 8 << 20
)

type supervisorOutcome string

const (
	outcomeNormalExit          supervisorOutcome = "normal_exit"
	outcomeSpawnFailure        supervisorOutcome = "spawn_failure"
	outcomeMalformedStream     supervisorOutcome = "malformed_stream"
	outcomeSilentChild         supervisorOutcome = "silent_child"
	outcomeTimeout             supervisorOutcome = "timeout"
	outcomeCancellation        supervisorOutcome = "cancellation"
	outcomeProviderFailure     supervisorOutcome = "provider_failure"
	outcomeRunnerDisappearance supervisorOutcome = "runner_disappearance"
	outcomeReportFailure       supervisorOutcome = "report_failure"
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
)

type runnerTelemetryState struct {
	sequence         int64
	estimateRevision int64
	correlationID    string
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
	state := h.stateLocked(runID)
	state.sequence++
	fact := runTelemetryReport{
		Sequence: state.sequence, CorrelationID: state.correlationID, Provider: h.resolvedProvider(),
		Adapter: h.resolvedAdapter(), AgentReportedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Kind: "phase", Phase: allowedSupervisorPhase(report.Phase),
		Activity:   safeReportValue(report.Summary, 160),
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
			fact.Phase = "completed"
			fact.Activity = "Provider process exited normally"
		} else {
			fact.Kind = "blocker"
			fact.Phase = "unknown"
			fact.BlockerState = supervisorOutcomeBlocker(report.Outcome)
		}
	}
	if hasEstimate {
		state.estimateRevision++
		fact.Kind = "progress"
		fact.EstimateRevision = &state.estimateRevision
		fact.ProgressPercent = report.ProgressPercent
		fact.ETASeconds = report.ETASeconds
		fact.ETAMinSeconds = report.ETAMinSeconds
		fact.ETAMaxSeconds = report.ETAMaxSeconds
		fact.EstimateSource = report.EstimateSource
		fact.EstimateConfidence = report.EstimateConfidence
		fact.EstimateBasis = safeReportValue(report.EstimateBasis, 240)
	}
	err := reportRunTelemetryContext(ctx, h.client, runID, fact)
	if err != nil {
		var httpErr *httpError
		if errors.As(err, &httpErr) && httpErr.Code == http.StatusConflict {
			return fmt.Errorf("%w: run status changed", errRunCancelled)
		}
		if errors.As(err, &httpErr) && (httpErr.Code == http.StatusNotFound || httpErr.Code == http.StatusGone) {
			return fmt.Errorf("%w: run no longer owned", errRunnerDisappeared)
		}
		return err
	}
	return nil
}

func (h *httpRunnerReportTransport) Identity(runID int64) (string, string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.stateLocked(runID)
	return state.correlationID, h.resolvedProvider(), h.resolvedAdapter()
}

func (h *httpRunnerReportTransport) stateLocked(runID int64) *runnerTelemetryState {
	if h.states == nil {
		h.states = map[int64]*runnerTelemetryState{}
	}
	state := h.states[runID]
	if state == nil {
		state = &runnerTelemetryState{correlationID: fmt.Sprintf("run-%d-%d", runID, time.Now().UTC().UnixNano())}
		h.states[runID] = state
	}
	return state
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
	case outcomeCancellation:
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
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return truncateRunes(value, max)
}

type supervisorRequest struct {
	RunID             int64
	RepoRoot          string
	ExecCmd           string
	Env               []string
	StructuredClaude  bool
	ExecutionTimeout  time.Duration
	SilenceTimeout    time.Duration
	HeartbeatInterval time.Duration
	LogSink           io.Writer
	Reporter          runnerReportTransport
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
		return &safeProviderProgress{phase: "completed", summary: "Provider completed normally"}, nil
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
	if strings.TrimSpace(req.ExecCmd) == "" {
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider command is empty"}
	}
	cmd := exec.Command("sh", "-c", req.ExecCmd) // #nosec G204 -- operator-owned command
	cmd.Dir = req.RepoRoot
	cmd.Env = append(os.Environ(), req.Env...)
	configureProcessGroup(cmd)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider stdout could not be opened"}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return supervisorResult{Outcome: outcomeSpawnFailure, Summary: "provider stderr could not be opened"}
	}
	if prompt, promptErr := promptForCommand(req.ExecCmd, req.Env); promptErr != nil {
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
	go consumeProviderStderr(stderrPipe, visibleStderr, logWriter, activity, streamErrors)
	go func() { waited <- cmd.Wait() }()

	currentPhase := "starting"
	report := func(fact supervisorReport) error {
		if req.Reporter == nil {
			return nil
		}
		reportCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return req.Reporter.Report(reportCtx, req.RunID, fact)
	}
	if err := report(supervisorReport{Event: "liveness", Phase: currentPhase}); err != nil {
		terminateOwnedProcess(cmd)
		<-waited
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
		case <-ctx.Done():
			terminateOwnedProcess(cmd)
			<-waited
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeCancellation, Summary: "runner cancelled the child process"})
		case <-executionTimer.C:
			terminateOwnedProcess(cmd)
			<-waited
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeTimeout, Summary: "provider exceeded the execution timeout"})
		case <-silenceTimer.C:
			terminateOwnedProcess(cmd)
			<-waited
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
				terminateOwnedProcess(cmd)
				<-waited
				return reportErrorResult(err)
			}
			if p.needsInput {
				terminateOwnedProcess(cmd)
				<-waited
				return finishSupervisorResult(req, supervisorResult{Outcome: outcomeProviderFailure, Summary: "provider requested input; this runner is one-shot"})
			}
		case err := <-streamErrors:
			terminateOwnedProcess(cmd)
			<-waited
			return finishSupervisorResult(req, supervisorResult{Outcome: outcomeMalformedStream, Summary: safeStreamErrorSummary(err)})
		case <-heartbeat.C:
			if err := report(supervisorReport{Event: "heartbeat", Phase: currentPhase}); err != nil {
				terminateOwnedProcess(cmd)
				<-waited
				return reportErrorResult(err)
			}
		case waitErr := <-waited:
			<-stdoutDone
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

func finishSupervisorResult(req supervisorRequest, result supervisorResult) supervisorResult {
	if req.Reporter == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := req.Reporter.Report(ctx, req.RunID, supervisorReport{
		Event: "result", Phase: map[bool]string{true: "completed", false: "unknown"}[result.Outcome == outcomeNormalExit], Outcome: result.Outcome, Summary: result.Summary,
	})
	if err != nil {
		return reportErrorResult(err)
	}
	return result
}

func consumeProviderStdout(src io.Reader, adapter providerStreamAdapter, visible, log io.Writer, activity chan<- struct{}, progress chan<- safeProviderProgress, failures chan<- error) {
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
			select {
			case progress <- *p:
			default:
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) && !strings.Contains(err.Error(), "file already closed") {
		notifyFailure(failures, errors.New("provider event exceeded its bounded size"))
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
		return supervisorResult{Outcome: outcomeCancellation, Summary: "run was cancelled while child process was alive"}
	}
	if errors.Is(err, errRunnerDisappeared) {
		return supervisorResult{Outcome: outcomeRunnerDisappearance, Summary: "run disappeared while child process was alive"}
	}
	return supervisorResult{Outcome: outcomeReportFailure, Summary: "supervisor could not report liveness"}
}
