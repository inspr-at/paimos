// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
)

const reporterOutputLimit = 64 << 10
const reporterSessionTimeout = 3 * time.Second
const reporterPreflightTimeout = 10 * time.Second

var reporterStableValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

type reporterCommand func(context.Context, string, []string, []string, io.Reader) ([]byte, error)

type reporterLeaseStore interface {
	GetOrCreate(string) (string, error)
	Delete(string) error
}

type memoryReporterLeaseStore struct {
	mu     sync.Mutex
	values map[string]string
}

func newMemoryReporterLeaseStore() reporterLeaseStore {
	return &memoryReporterLeaseStore{values: make(map[string]string)}
}

func (s *memoryReporterLeaseStore) GetOrCreate(sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value := s.values[sessionID]; value != "" {
		return value, nil
	}
	value, err := generateReporterLease()
	if err != nil {
		return "", err
	}
	s.values[sessionID] = value
	return value, nil
}

func (s *memoryReporterLeaseStore) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, sessionID)
	return nil
}

type reportedSession struct {
	publicID  string
	projectID int64
	identity  string
	terminal  bool
}

// cliReporter is the authenticated bridge from agentd to M161. The paimos
// child reads the protected API-key file itself; agentd never reads or copies
// credentials, vendor traffic, prompts, or output.
type cliReporter struct {
	mu          sync.Mutex
	instance    string
	host        string
	paimosPath  string
	environment []string
	run         reporterCommand
	leases      reporterLeaseStore
	sessions    map[string]reportedSession
	controller  agentd.Controller
	nextSession int
}

type harnessSessionResponse struct {
	ID              string  `json:"id"`
	ProjectID       int64   `json:"project_id"`
	AgentName       string  `json:"agent_name"`
	Harness         string  `json:"harness"`
	Phase           string  `json:"phase"`
	Role            string  `json:"role,omitempty"`
	ParentSessionID *string `json:"parent_harness_session_id"`
	TicketID        *int64  `json:"ticket_id"`
}

type harnessControlResponse struct {
	ID               string `json:"id"`
	HarnessSessionID string `json:"harness_session_id"`
	Kind             string `json:"kind"`
	State            string `json:"state"`
	Reason           string `json:"reason"`
}

type harnessYieldResponse struct {
	Session  harnessSessionResponse   `json:"session"`
	Controls []harnessControlResponse `json:"controls"`
}

func newCLIReporter(instance, stateRoot, host, paimosPath, reportURL, apiKeyFile string) (*cliReporter, error) {
	host = strings.TrimSpace(host)
	if !safeReporterValue(host, 128) {
		return nil, errors.New("--report-host must be a safe stable host identifier")
	}
	if strings.TrimSpace(paimosPath) == "" {
		paimosPath = "paimos"
	}
	resolved, err := exec.LookPath(paimosPath)
	if err != nil {
		return nil, errors.New("authenticated paimos CLI for agentd reporting is unavailable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, errors.New("authenticated paimos CLI path is invalid")
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, errors.New("authenticated paimos CLI path cannot be pinned")
	}
	parsed, err := url.Parse(strings.TrimSpace(reportURL))
	loopbackHTTP := err == nil && parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && !loopbackHTTP) {
		return nil, errors.New("--report-url must be an HTTPS URL (HTTP is allowed only for loopback)")
	}
	apiKeyFile = strings.TrimSpace(apiKeyFile)
	if apiKeyFile == "" {
		return nil, errors.New("--report-api-key-file must be an absolute protected credential path")
	}
	apiKeyFile, err = filepath.Abs(apiKeyFile)
	if err != nil {
		return nil, errors.New("--report-api-key-file must be an absolute protected credential path")
	}
	environment := reporterEnvironment(os.Environ(), parsed.String(), apiKeyFile)
	leases, err := newDiskReporterLeaseStore(stateRoot, instance)
	if err != nil {
		return nil, errors.New("private agentd worker-lease store is unavailable")
	}
	reporter, err := newCLIReporterWithRunner(instance, host, resolved, environment, runReporterCommand, leases)
	if err != nil {
		return nil, err
	}
	preflightCtx, cancel := context.WithTimeout(context.Background(), reporterPreflightTimeout)
	defer cancel()
	if _, err := reporter.run(preflightCtx, reporter.paimosPath, []string{"--json", "auth", "whoami"}, reporter.environment, nil); err != nil {
		return nil, errors.New("authenticated M161 reporter preflight failed; verify --report-url, --report-api-key-file, file ownership/mode, and network access")
	}
	return reporter, nil
}

func newCLIReporterWithRunner(instance, host, paimosPath string, environment []string, run reporterCommand, leases reporterLeaseStore) (*cliReporter, error) {
	if strings.TrimSpace(instance) == "" || !safeReporterValue(strings.TrimSpace(host), 128) || strings.TrimSpace(paimosPath) == "" || run == nil || leases == nil {
		return nil, errors.New("agentd reporter configuration is invalid")
	}
	return &cliReporter{instance: instance, host: strings.TrimSpace(host), paimosPath: paimosPath, environment: environment,
		run: run, leases: leases, sessions: make(map[string]reportedSession)}, nil
}

func (r *cliReporter) BindController(controller agentd.Controller) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if controller == nil || r.controller != nil {
		return errors.New("agentd reporter controller binding is invalid")
	}
	r.controller = controller
	return nil
}

func reporterEnvironment(base []string, reportURL, apiKeyFile string) []string {
	blocked := map[string]bool{"PAIMOS_URL": true, "PAIMOS_API_KEY": true, "PAIMOS_API_KEY_FILE": true, "PPM_URL": true, "PPMAPIKEY": true,
		"PAIMOS_AGENT_NAME": true, "PAIMOS_SESSION_ID": true}
	out := make([]string, 0, len(base)+2)
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[key] {
			out = append(out, value)
		}
	}
	return append(out, "PAIMOS_URL="+reportURL, "PAIMOS_API_KEY_FILE="+apiKeyFile)
}

func safeReporterValue(value string, limit int) bool {
	return len(value) >= 1 && len(value) <= limit && reporterStableValue.MatchString(value)
}

func generateReporterLease() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("generate private worker lease: unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func runReporterCommand(ctx context.Context, path string, args, environment []string, stdin io.Reader) ([]byte, error) {
	command := exec.CommandContext(ctx, path, args...) // #nosec G204 -- resolved once at startup; argv is never interpreted by a shell.
	command.Stdin, command.Env = stdin, environment
	var output boundedReporterBuffer
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return nil, errors.New("authenticated paimos reporter command failed")
	}
	if output.overflow {
		return nil, errors.New("paimos reporter response exceeded its bound")
	}
	return output.Bytes(), nil
}

type boundedReporterBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedReporterBuffer) Write(value []byte) (int, error) {
	original, remaining := len(value), reporterOutputLimit-b.Len()
	if remaining <= 0 {
		b.overflow = true
		return original, nil
	}
	if len(value) > remaining {
		value, b.overflow = value[:remaining], true
	}
	_, _ = b.Buffer.Write(value)
	return original, nil
}

func (r *cliReporter) ReportStatus(ctx context.Context, status agentd.Status) error {
	if status.Instance != r.instance {
		return errors.New("agentd reporter instance scope mismatch")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.controller == nil {
		return errors.New("agentd reporter has no owned controller")
	}
	failures := 0
	start := r.nextSession
	for attempted := 0; attempted < len(status.Sessions); attempted++ {
		index := (start + attempted) % len(status.Sessions)
		sessionCtx, cancel := context.WithTimeout(ctx, reporterSessionTimeout)
		err := r.reportSession(sessionCtx, status.Sessions[index])
		cancel()
		r.nextSession = (index + 1) % len(status.Sessions)
		if err != nil {
			failures++
		}
		if ctx.Err() != nil {
			break
		}
	}
	if failures > 0 {
		return fmt.Errorf("agentd reporter failed for %d session(s)", failures)
	}
	return nil
}

func (r *cliReporter) reportSession(ctx context.Context, session agentd.Session) error {
	terminal := terminalAgentdState(session.State)
	if (terminal && len(session.Reporter.Capabilities) == 0) || session.ProjectID <= 0 || !session.Managed {
		return nil
	}
	if session.Reporter.Closed {
		return nil
	}
	if session.Reporter.RemoteClosed {
		if err := r.leases.Delete(session.ID); err != nil {
			return errors.New("private agentd worker lease could not be released")
		}
		closedState := r.baseReporterState(session, session.Reporter.PublicSessionID)
		closedState.RemoteClosed = true
		closedState.Closed = true
		return r.checkpoint(ctx, session, closedState)
	}
	workerLease, err := r.leases.GetOrCreate(session.ID)
	if err != nil {
		return errors.New("private agentd worker lease is unavailable")
	}
	known, exists := r.sessions[session.ID]
	if !exists && session.Reporter.PublicSessionID != "" {
		known = reportedSession{publicID: session.Reporter.PublicSessionID, projectID: session.ProjectID, identity: session.Identity}
		r.sessions[session.ID] = known
		exists = true
	}
	if pending := session.Reporter.Pending; pending != nil {
		_, agentName, err := reporterIdentity(session)
		if err != nil {
			return err
		}
		if err := r.completeControl(ctx, known.publicID, session, agentName, workerLease, *pending); err != nil {
			return err
		}
		if err := r.checkpoint(ctx, session, r.baseReporterState(session, known.publicID)); err != nil {
			return err
		}
		session.Reporter.Pending = nil
	}
	if exists {
		if known.projectID != session.ProjectID || known.identity != session.Identity {
			return errors.New("agentd reporter session scope changed")
		}
		if known.terminal {
			return nil
		}
		if terminal {
			if err := r.markStopped(ctx, known.publicID, session, workerLease); err != nil {
				return err
			}
			closedState := r.baseReporterState(session, known.publicID)
			closedState.RemoteClosed = true
			if err := r.checkpoint(ctx, session, closedState); err != nil {
				return err
			}
			if err := r.leases.Delete(session.ID); err != nil {
				return errors.New("private agentd worker lease could not be released")
			}
			closedState.Closed = true
			if err := r.checkpoint(ctx, session, closedState); err != nil {
				return err
			}
			known.terminal = true
			r.sessions[session.ID] = known
			return nil
		}
		if err := r.heartbeat(ctx, known.publicID, session, workerLease, reporterPhase(session.State)); err != nil {
			return err
		}
		return r.yieldControls(ctx, known.publicID, session, workerLease)
	}
	harness, agentName, err := reporterIdentity(session)
	if err != nil {
		return err
	}
	ownedCapabilities := session.Reporter.Capabilities
	if len(ownedCapabilities) == 0 {
		ownedCapabilities = reporterCapabilitySet(session.Capabilities)
		if err := r.checkpoint(ctx, session, agentd.ReporterState{Capabilities: ownedCapabilities}); err != nil {
			return err
		}
	}
	capabilities := joinReporterCapabilities(ownedCapabilities)
	if !strings.Contains(","+capabilities+",", ",status,") {
		return errors.New("agentd reporter session has no status capability")
	}
	role := session.Role
	if role == "" {
		role = "worker"
	}
	args := []string{"--json", "harness", "register", "--project", strconv.FormatInt(session.ProjectID, 10),
		"--agent", agentName, "--harness", harness, "--host", r.host, "--registration-file", "-",
		"--management", "managed", "--role", role, "--steer-mode", "none", "--capability", capabilities}
	if session.ParentSessionID != "" {
		args = append(args, "--parent-session", session.ParentSessionID)
	}
	if session.TicketID > 0 {
		args = append(args, "--ticket-id", strconv.FormatInt(session.TicketID, 10))
	}
	registration, err := json.Marshal(map[string]string{"harness_session_ref": session.ID, "worker_lease": workerLease})
	if err != nil {
		return errors.New("encode private reporter registration: unavailable")
	}
	raw, err := r.run(ctx, r.paimosPath, args, r.environment, bytes.NewReader(registration))
	if err != nil {
		return err
	}
	response := harnessSessionResponse{Role: role}
	if json.Unmarshal(raw, &response) != nil || uuid.Validate(response.ID) != nil || response.ProjectID != session.ProjectID || response.AgentName != agentName || response.Harness != harness ||
		response.Role != role || !reporterBindingMatches(response, session) {
		return errors.New("paimos reporter returned mismatched harness session scope")
	}
	if err := r.checkpoint(ctx, session, agentd.ReporterState{PublicSessionID: response.ID, Capabilities: ownedCapabilities}); err != nil {
		return err
	}
	r.sessions[session.ID] = reportedSession{publicID: response.ID, projectID: session.ProjectID, identity: session.Identity}
	if terminal {
		if err := r.markStopped(ctx, response.ID, session, workerLease); err != nil {
			return err
		}
		closedState := r.baseReporterState(session, response.ID)
		closedState.RemoteClosed = true
		if err := r.checkpoint(ctx, session, closedState); err != nil {
			return err
		}
		if err := r.leases.Delete(session.ID); err != nil {
			return errors.New("private agentd worker lease could not be released")
		}
		closedState.Closed = true
		return r.checkpoint(ctx, session, closedState)
	}
	if err := r.heartbeat(ctx, response.ID, session, workerLease, reporterPhase(session.State)); err != nil {
		return err
	}
	return r.yieldControls(ctx, response.ID, session, workerLease)
}

func reporterBindingMatches(response harnessSessionResponse, session agentd.Session) bool {
	parentMatches := session.ParentSessionID == "" && response.ParentSessionID == nil ||
		session.ParentSessionID != "" && response.ParentSessionID != nil && *response.ParentSessionID == session.ParentSessionID
	ticketMatches := session.TicketID == 0 && response.TicketID == nil ||
		session.TicketID > 0 && response.TicketID != nil && *response.TicketID == session.TicketID
	return parentMatches && ticketMatches
}

func reporterIdentity(session agentd.Session) (string, string, error) {
	prefix := session.Adapter + ":"
	if !strings.HasPrefix(session.Identity, prefix) {
		return "", "", errors.New("agentd reporter identity does not match its adapter")
	}
	agentName := strings.TrimPrefix(session.Identity, prefix)
	if !safeReporterValue(session.Adapter, 64) || !safeReporterValue(agentName, 64) {
		return "", "", errors.New("agentd reporter identity is not a safe M161 address")
	}
	return session.Adapter, agentName, nil
}

func reporterCapabilitySet(input []agentd.Capability) []agentd.Capability {
	allowed := map[agentd.Capability]bool{}
	for _, capability := range input {
		switch capability {
		case agentd.CapabilityStatus, agentd.CapabilityInterrupt, agentd.CapabilityStop:
			allowed[capability] = true
		}
	}
	// Durable steer remains on the existing agentd_codex/agentd_claude target
	// and receiver listener. Advertising inbox or steer here would create a
	// managed_harness reroute target this reporter does not drain.
	order := []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt, agentd.CapabilityStop}
	values := make([]string, 0, len(order))
	for _, capability := range order {
		if allowed[capability] {
			values = append(values, string(capability))
		}
	}
	out := make([]agentd.Capability, len(values))
	for i := range values {
		out[i] = agentd.Capability(values[i])
	}
	return out
}

func joinReporterCapabilities(input []agentd.Capability) string {
	values := make([]string, len(input))
	for i := range input {
		values[i] = string(input[i])
	}
	return strings.Join(values, ",")
}

func reporterPhase(state agentd.SessionState) string {
	if state == agentd.StateStarting {
		return "starting"
	}
	if state == agentd.StateStopping {
		return "stopping"
	}
	return "working"
}

func terminalAgentdState(state agentd.SessionState) bool {
	switch state {
	case agentd.StateStopped, agentd.StateExited, agentd.StateFailed, agentd.StateOwnershipLost:
		return true
	}
	return false
}

func (r *cliReporter) heartbeat(ctx context.Context, publicID string, session agentd.Session, workerLease, phase string) error {
	_, agentName, err := reporterIdentity(session)
	if err != nil {
		return err
	}
	args := []string{"--json", "harness", "heartbeat", "--project", strconv.FormatInt(session.ProjectID, 10), "--session", publicID, "--agent", agentName, "--worker-lease-file", "-", "--phase", phase}
	if session.ActivitySequence > 0 && session.LastEventKind != "" {
		args = append(args, "--activity-sequence", strconv.FormatInt(session.ActivitySequence, 10), "--activity-kind", string(session.LastEventKind))
	}
	raw, err := r.run(ctx, r.paimosPath, args, r.environment, strings.NewReader(workerLease))
	if err != nil {
		return err
	}
	var response harnessSessionResponse
	if json.Unmarshal(raw, &response) != nil || response.ID != publicID || response.ProjectID != session.ProjectID || response.AgentName != agentName || response.Harness != session.Adapter || response.Phase != phase {
		return errors.New("paimos reporter returned mismatched heartbeat evidence")
	}
	return nil
}

func (r *cliReporter) yieldControls(ctx context.Context, publicID string, session agentd.Session, workerLease string) error {
	_, agentName, err := reporterIdentity(session)
	if err != nil {
		return err
	}
	args := []string{"--json", "harness", "yield", "--project", strconv.FormatInt(session.ProjectID, 10), "--session", publicID, "--agent", agentName, "--worker-lease-file", "-"}
	raw, err := r.run(ctx, r.paimosPath, args, r.environment, strings.NewReader(workerLease))
	if err != nil {
		return err
	}
	var response harnessYieldResponse
	if json.Unmarshal(raw, &response) != nil || response.Session.ID != publicID || response.Session.ProjectID != session.ProjectID || response.Session.AgentName != agentName || response.Session.Harness != session.Adapter {
		return errors.New("paimos reporter returned mismatched yield scope")
	}
	for _, control := range response.Controls {
		if uuid.Validate(control.ID) != nil || control.HarnessSessionID != publicID || control.State != "claimed" || (control.Kind != "interrupt" && control.Kind != "stop") {
			return errors.New("paimos reporter returned an invalid typed control")
		}
		request := agentd.ControlRequest{Instance: r.instance, ProjectID: session.ProjectID, Identity: session.Identity, CorrelationID: control.ID}
		outcome, reason := "rejected", "ownership_lost"
		completion := agentd.ReporterCompletion{ControlID: control.ID, Kind: control.Kind, Outcome: outcome, Reason: reason}
		if err := r.checkpoint(ctx, session, r.pendingReporterState(session, publicID, completion)); err != nil {
			return err
		}
		if !terminalAgentdState(session.State) {
			outcome, reason = "applied", "applied"
			var receipt agentd.Receipt
			if control.Kind == "interrupt" {
				receipt, err = r.controller.Interrupt(ctx, session.ID, request)
			} else {
				receipt, err = r.controller.Stop(ctx, session.ID, request)
			}
			if err == nil && !validReporterReceipt(receipt, control.Kind, session, request) {
				err = errors.New("agentd reporter received invalid owned control evidence")
			}
			if err != nil {
				outcome, reason = "rejected", reporterControlReason(err)
			}
			completion = agentd.ReporterCompletion{ControlID: control.ID, Kind: control.Kind, Outcome: outcome, Reason: reason}
			if err := r.checkpoint(ctx, session, r.pendingReporterState(session, publicID, completion)); err != nil {
				return err
			}
		}
		if err := r.completeControl(ctx, publicID, session, agentName, workerLease, completion); err != nil {
			return err
		}
		if err := r.checkpoint(ctx, session, r.baseReporterState(session, publicID)); err != nil {
			return err
		}
	}
	return nil
}

func validReporterReceipt(receipt agentd.Receipt, operation string, session agentd.Session, request agentd.ControlRequest) bool {
	return receipt.Operation == operation && receipt.SessionID == session.ID && receipt.Instance == request.Instance &&
		receipt.ProjectID == request.ProjectID && receipt.Identity == request.Identity && receipt.CorrelationID == request.CorrelationID &&
		receipt.RequestedLevel == "steer" && receipt.EffectiveLevel == "steer" && receipt.FallbackReason == "" &&
		strings.TrimSpace(receipt.Primitive) != "" && !receipt.AppliedAt.IsZero()
}

func reporterControlReason(err error) string {
	switch {
	case errors.Is(err, agentd.ErrSessionNotFound), errors.Is(err, agentd.ErrSessionNotRunning):
		return "not_running"
	case errors.Is(err, agentd.ErrCapabilityMissing):
		return "unsupported"
	default:
		return "failed"
	}
}

func (r *cliReporter) completeControl(ctx context.Context, publicID string, session agentd.Session, agentName, workerLease string, completion agentd.ReporterCompletion) error {
	args := []string{"--json", "harness", "complete-control", "--project", strconv.FormatInt(session.ProjectID, 10),
		"--session", publicID, "--control-id", completion.ControlID, "--agent", agentName, "--worker-lease-file", "-",
		"--outcome", completion.Outcome, "--reason", completion.Reason}
	raw, err := r.run(ctx, r.paimosPath, args, r.environment, strings.NewReader(workerLease))
	if err != nil {
		return err
	}
	var response harnessControlResponse
	if json.Unmarshal(raw, &response) != nil || response.ID != completion.ControlID || response.HarnessSessionID != publicID || response.Kind != completion.Kind || response.State != completion.Outcome || response.Reason != completion.Reason {
		return errors.New("paimos reporter returned mismatched control completion evidence")
	}
	return nil
}

func (r *cliReporter) checkpoint(ctx context.Context, session agentd.Session, state agentd.ReporterState) error {
	request := agentd.ControlRequest{Instance: r.instance, ProjectID: session.ProjectID, Identity: session.Identity}
	return r.controller.CheckpointReporter(ctx, session.ID, request, state)
}

func (r *cliReporter) baseReporterState(session agentd.Session, publicID string) agentd.ReporterState {
	capabilities := session.Reporter.Capabilities
	if len(capabilities) == 0 {
		capabilities = reporterCapabilitySet(session.Capabilities)
	}
	return agentd.ReporterState{PublicSessionID: publicID, Capabilities: capabilities}
}

func (r *cliReporter) pendingReporterState(session agentd.Session, publicID string, completion agentd.ReporterCompletion) agentd.ReporterState {
	state := r.baseReporterState(session, publicID)
	state.Pending = &completion
	return state
}

func (r *cliReporter) markStopped(ctx context.Context, publicID string, session agentd.Session, workerLease string) error {
	_, agentName, err := reporterIdentity(session)
	if err != nil {
		return err
	}
	args := []string{"--json", "harness", "mark-stopped", "--project", strconv.FormatInt(session.ProjectID, 10), "--session", publicID, "--agent", agentName, "--worker-lease-file", "-", "--reason", reporterClosedReason(session.State)}
	raw, err := r.run(ctx, r.paimosPath, args, r.environment, strings.NewReader(workerLease))
	if err != nil {
		return fmt.Errorf("agentd reporter could not close harness session: %w", err)
	}
	var response harnessSessionResponse
	if json.Unmarshal(raw, &response) != nil || response.ID != publicID || response.ProjectID != session.ProjectID || response.AgentName != agentName || response.Harness != session.Adapter || response.Phase != "stopped" {
		return errors.New("paimos reporter returned mismatched stopped-session evidence")
	}
	return nil
}

func reporterClosedReason(state agentd.SessionState) string {
	switch state {
	case agentd.StateOwnershipLost:
		return "ownership_lost"
	case agentd.StateFailed:
		return "process_failed"
	case agentd.StateExited:
		return "process_exited"
	default:
		return "stopped"
	}
}
