// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const defaultHeartbeatInterval = 15 * time.Second
const reporterTimeout = 10 * time.Second
const sessionFinalizeTimeout = 10 * time.Second
const maxControlReplayEntries = 256

type SupervisorConfig struct {
	Adapters              []Adapter
	HeartbeatInterval     time.Duration
	MaxSessions           int
	StateRoot             string
	Instance              string
	Reporter              Reporter
	DispatchResolver      DispatchResolver
	AllowSharedWorkspaces bool
	WorkspaceInspector    workspaceInspector
}

type sessionEntry struct {
	mu             sync.Mutex
	controlMu      sync.Mutex
	session        Session
	process        Process
	capabilities   map[Capability]bool
	monitorDone    chan struct{}
	monitorErr     error
	stopRequested  bool
	failureCode    ErrorCode
	controlReady   bool
	controlReplays map[string]controlReplay
}

type controlReplay struct {
	operation string
	textHash  [sha256.Size]byte
	receipt   Receipt
	err       error
}

type Supervisor struct {
	mu                    sync.RWMutex
	startMu               sync.Mutex
	daemonID              string
	adapters              map[string]Adapter
	sessions              map[string]*sessionEntry
	heartbeatInterval     time.Duration
	maxSessions           int
	closed                bool
	instance              string
	journal               *registryJournal
	reporter              Reporter
	dispatchResolver      DispatchResolver
	allowSharedWorkspaces bool
	inspectWorkspace      workspaceInspector
	reporterErrorCode     ErrorCode
	reporterFailures      int64
	reportMu              sync.Mutex
	reportWake            chan struct{}
	done                  chan struct{}
	lifecycleCtx          context.Context
	lifecycleCancel       context.CancelFunc
}

func NewSupervisor(config SupervisorConfig) (*Supervisor, error) {
	heartbeat := config.HeartbeatInterval
	if heartbeat == 0 {
		heartbeat = defaultHeartbeatInterval
	}
	if heartbeat < time.Millisecond {
		return nil, errors.New("agentd heartbeat interval is invalid")
	}
	maximum := config.MaxSessions
	if maximum == 0 {
		maximum = 256
	}
	if maximum < 1 || maximum > 4096 {
		return nil, errors.New("agentd session bound is invalid")
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	s := &Supervisor{
		daemonID: uuid.NewString(), adapters: map[string]Adapter{}, sessions: map[string]*sessionEntry{},
		heartbeatInterval: heartbeat, maxSessions: maximum, instance: config.Instance, reporter: config.Reporter,
		done: make(chan struct{}), reportWake: make(chan struct{}, 1), lifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel,
		dispatchResolver: config.DispatchResolver, allowSharedWorkspaces: config.AllowSharedWorkspaces, inspectWorkspace: config.WorkspaceInspector,
	}
	if s.dispatchResolver == nil {
		if resolver, ok := config.Reporter.(DispatchResolver); ok {
			s.dispatchResolver = resolver
		}
	}
	if s.inspectWorkspace == nil {
		s.inspectWorkspace = inspectWorkspace
	}
	if config.StateRoot != "" {
		journal, err := openRegistryJournal(config.StateRoot, config.Instance, maximum)
		if err != nil {
			return nil, err
		}
		s.journal = journal
		for _, recovered := range journal.recovered() {
			capabilities := map[Capability]bool{CapabilityInbox: true, CapabilityStatus: true}
			s.sessions[recovered.ID] = &sessionEntry{session: recovered, capabilities: capabilities}
			if err := journal.put(recovered); err != nil {
				return nil, err
			}
		}
	}
	for _, adapter := range config.Adapters {
		if err := s.RegisterAdapter(adapter); err != nil {
			return nil, err
		}
	}
	if s.reporter != nil {
		if binding, ok := s.reporter.(ControllerBindingReporter); ok {
			if err := binding.BindController(s); err != nil {
				return nil, err
			}
		}
		go s.heartbeatLoop()
	}
	return s, nil
}

// RegisterAdapter atomically replaces a harness adapter without touching the
// daemon-owned child registry. A crashed/restarted app-server proxy therefore
// cannot make an owned child become unmanaged or introduce foreign sessions.
func (s *Supervisor) RegisterAdapter(adapter Adapter) error {
	if adapter == nil {
		return errors.New("agentd adapter is nil")
	}
	name := strings.TrimSpace(adapter.Name())
	if name == "" || name != adapter.Name() || len(name) > 64 || strings.ContainsAny(name, "\x00\r\n/ ") {
		return errors.New("agentd adapter name is invalid")
	}
	if _, err := canonicalCapabilities(adapter.Capabilities()); err != nil {
		return fmt.Errorf("agentd adapter %s: %w", name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("agentd supervisor is closed")
	}
	s.adapters[name] = adapter
	return nil
}

func canonicalCapabilities(input []Capability) ([]Capability, error) {
	wanted := make(map[Capability]bool, len(input))
	for _, capability := range input {
		switch capability {
		case CapabilityInbox, CapabilityStatus, CapabilitySteer, CapabilityInterrupt, CapabilityStop:
		default:
			return nil, errors.New("adapter advertises an unknown capability")
		}
		if wanted[capability] {
			return nil, errors.New("adapter advertises a duplicate capability")
		}
		wanted[capability] = true
	}
	order := []Capability{CapabilityInbox, CapabilityStatus, CapabilitySteer, CapabilityInterrupt, CapabilityStop}
	out := make([]Capability, 0, len(input))
	for _, capability := range order {
		if wanted[capability] {
			out = append(out, capability)
		}
	}
	return out, nil
}

func (s *Supervisor) Start(ctx context.Context, request StartRequest) (Session, error) {
	validated, err := validateStartRequest(request)
	if err != nil {
		return Session{}, err
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.RLock()
	adapter := s.adapters[validated.Adapter]
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return Session{}, errors.New("agentd supervisor is closed")
	}
	if adapter == nil {
		return Session{}, ErrAdapterUnsupported
	}
	if validated.WorkspaceMode == WorkspaceShared && !s.allowSharedWorkspaces {
		return Session{}, errors.New("shared managed workspaces require explicit daemon authorization")
	}
	var profile *dispatchprofile.Profile
	if validated.DispatchProfileID != "" {
		if s.dispatchResolver == nil {
			return Session{}, ErrDispatchProfile
		}
		expected, catalogErr := dispatchprofile.Resolve(validated.DispatchProfileID, validated.DispatchProfileVersion, validated.Adapter)
		if catalogErr != nil {
			return Session{}, ErrDispatchProfile
		}
		resolved, resolveErr := s.dispatchResolver.ResolveDispatchProfile(ctx, validated.DispatchProfileID, validated.DispatchProfileVersion, validated.Adapter)
		if resolveErr != nil || dispatchprofile.Validate(resolved) != nil || resolved != expected || resolved.WorkspaceMode != validated.WorkspaceMode {
			return Session{}, ErrDispatchProfile
		}
		profile = &resolved
		validated.ResolvedProfile = profile
	}
	provenance, err := s.inspectWorkspace(ctx, validated.Workspace, validated.WorkspaceMode)
	if err != nil {
		return Session{}, err
	}
	accountLabel := "unknown"
	if prober, ok := adapter.(AccountProber); ok {
		if candidate := prober.AccountLabel(ctx); validAccountLabel(candidate) {
			accountLabel = candidate
		}
	}
	capabilities, err := canonicalCapabilities(adapter.Capabilities())
	if err != nil {
		return Session{}, err
	}
	capabilitySet := make(map[Capability]bool, len(capabilities))
	for _, capability := range capabilities {
		capabilitySet[capability] = true
	}
	if !capabilitySet[CapabilityStatus] || !capabilitySet[CapabilityStop] {
		return Session{}, ErrAdapterUnsupported
	}
	now := time.Now().UTC()
	entry := &sessionEntry{session: Session{
		ID: uuid.NewString(), Identity: validated.Identity, Adapter: validated.Adapter, Workspace: validated.Workspace,
		ProjectID: validated.ProjectID, Role: validated.Role, ParentSessionID: validated.ParentSessionID, TicketID: validated.TicketID,
		WorkspaceProvenance: provenance, DispatchProfile: profile, AccountLabel: accountLabel,
		Capabilities: append([]Capability(nil), capabilities...), Managed: true, State: StateStarting,
		StartedAt: now, HeartbeatAt: now,
	}, capabilities: capabilitySet, monitorDone: make(chan struct{})}
	if err := s.reserveSession(entry); err != nil {
		return Session{}, err
	}
	observe := func(event AdapterEvent) {
		entry.observe(event)
		s.scheduleReport()
	}
	startCtx, cancelStart := context.WithCancel(ctx)
	stopLifecycleCancel := context.AfterFunc(s.lifecycleCtx, cancelStart)
	defer func() {
		stopLifecycleCancel()
		cancelStart()
	}()
	process, err := adapter.Start(startCtx, validated, observe)
	if err != nil {
		s.releaseReservation(entry.session.ID)
		return Session{}, err
	}
	if process == nil || process.PID() <= 0 {
		if process != nil {
			_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "agentd-invalid-start"})
		}
		s.releaseReservation(entry.session.ID)
		return Session{}, errors.New("agentd adapter returned an invalid owned process")
	}
	s.mu.RLock()
	closed = s.closed
	s.mu.RUnlock()
	if closed {
		_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "agentd-shutdown-start"})
		s.releaseReservation(entry.session.ID)
		return Session{}, errors.New("agentd supervisor is closed")
	}
	entry.mu.Lock()
	entry.process = process
	entry.session.PID = process.PID()
	entry.session.State = StateRunning
	entry.refreshSteerableLocked()
	snapshot := entry.snapshotLocked()
	entry.mu.Unlock()

	if err := s.persist(entry); err != nil {
		s.releaseReservation(snapshot.ID)
		_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "agentd-persist-stop"})
		return Session{}, err
	}
	go s.monitor(entry)
	s.scheduleReport()
	return snapshot, nil
}

func (s *Supervisor) reserveSession(entry *sessionEntry) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("agentd supervisor is closed")
	}
	var candidates []*sessionEntry
	for _, existing := range s.sessions {
		existing.mu.Lock()
		terminal := existing.session.State == StateStopped || existing.session.State == StateExited ||
			existing.session.State == StateFailed || existing.session.State == StateOwnershipLost
		remoteClosed := existing.session.Reporter.PublicSessionID == "" || existing.session.Reporter.Closed
		workspaceConflict := !terminal && existing.session.WorkspaceProvenance.Identity != "" &&
			existing.session.WorkspaceProvenance.Identity == entry.session.WorkspaceProvenance.Identity &&
			(existing.session.WorkspaceProvenance.Mode == WorkspaceExclusive || entry.session.WorkspaceProvenance.Mode == WorkspaceExclusive)
		existing.mu.Unlock()
		if workspaceConflict {
			s.mu.Unlock()
			return ErrWorkspaceConflict
		}
		if terminal && remoteClosed {
			candidates = append(candidates, existing)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].session.StartedAt.Before(candidates[j].session.StartedAt) })
	pruned := make([]*sessionEntry, 0)
	for len(s.sessions) >= s.maxSessions && len(candidates) > 0 {
		candidate := candidates[0]
		candidates = candidates[1:]
		delete(s.sessions, candidate.session.ID)
		pruned = append(pruned, candidate)
	}
	if len(s.sessions) >= s.maxSessions {
		s.mu.Unlock()
		return errors.New("agentd active session bound reached")
	}
	s.sessions[entry.session.ID] = entry
	s.mu.Unlock()
	for _, candidate := range pruned {
		if err := s.journal.delete(candidate.session.ID); err != nil {
			s.mu.Lock()
			delete(s.sessions, entry.session.ID)
			for _, restore := range pruned {
				s.sessions[restore.session.ID] = restore
			}
			s.mu.Unlock()
			return err
		}
	}
	return nil
}

func (s *Supervisor) releaseReservation(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func validateStartRequest(request StartRequest) (StartRequest, error) {
	request.Adapter = strings.TrimSpace(request.Adapter)
	request.Identity = strings.TrimSpace(request.Identity)
	if request.Adapter == "" || len(request.Adapter) > 64 || strings.ContainsAny(request.Adapter, "\x00\r\n/ ") {
		return StartRequest{}, errors.New("agentd adapter is invalid")
	}
	if request.Identity == "" || request.Identity != strings.TrimSpace(request.Identity) || len(request.Identity) > 256 ||
		!utf8.ValidString(request.Identity) || strings.ContainsAny(request.Identity, "\x00\r\n") {
		return StartRequest{}, errors.New("agentd identity is invalid")
	}
	if request.ProjectID <= 0 {
		return StartRequest{}, errors.New("agentd project is invalid")
	}
	request.Role = strings.ToLower(strings.TrimSpace(request.Role))
	if request.Role == "" {
		request.Role = "worker"
	}
	if request.Role != "worker" && request.Role != "coordinator" {
		return StartRequest{}, errors.New("agentd role is invalid")
	}
	request.ParentSessionID = strings.TrimSpace(request.ParentSessionID)
	if request.ParentSessionID != "" && uuid.Validate(request.ParentSessionID) != nil {
		return StartRequest{}, errors.New("agentd parent harness session id is invalid")
	}
	if request.TicketID < 0 {
		return StartRequest{}, errors.New("agentd ticket id is invalid")
	}
	request.WorkspaceMode = strings.ToLower(strings.TrimSpace(request.WorkspaceMode))
	if request.WorkspaceMode == "" {
		request.WorkspaceMode = WorkspaceExclusive
	}
	if request.WorkspaceMode != WorkspaceExclusive && request.WorkspaceMode != WorkspaceShared {
		return StartRequest{}, errors.New("agentd workspace mode is invalid")
	}
	request.DispatchProfileID = strings.TrimSpace(request.DispatchProfileID)
	request.DispatchProfileVersion = strings.TrimSpace(request.DispatchProfileVersion)
	if (request.DispatchProfileID == "") != (request.DispatchProfileVersion == "") ||
		(request.DispatchProfileID != "" && (!validSafeLabel(request.DispatchProfileID, 128) || !validSafeLabel(request.DispatchProfileVersion, 64))) {
		return StartRequest{}, errors.New("agentd dispatch profile id and version must be supplied together")
	}
	if request.Prompt == "" || len(request.Prompt) > maxPromptBytes || !utf8.ValidString(request.Prompt) || strings.ContainsRune(request.Prompt, 0) {
		return StartRequest{}, errors.New("agentd prompt is invalid")
	}
	workspace, err := filepath.Abs(request.Workspace)
	if err != nil {
		return StartRequest{}, errors.New("agentd workspace is invalid")
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return StartRequest{}, errors.New("agentd workspace is unavailable")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return StartRequest{}, errors.New("agentd workspace is not a directory")
	}
	request.Workspace = workspace
	return request, nil
}

func (e *sessionEntry) observe(event AdapterEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if event.HarnessSessionID != "" && validOpaqueID(event.HarnessSessionID) && e.session.HarnessSessionID == "" {
		e.session.HarnessSessionID = event.HarnessSessionID
	}
	if validEventKind(event.Kind) {
		e.session.LastEventKind = event.Kind
		e.session.ActivitySequence++
		e.session.ActivityAt = time.Now().UTC()
		if event.Kind == EventSessionStarted || event.Kind == EventTurnStarted {
			e.controlReady = true
		}
	}
	if event.CorrelationID != "" && validCorrelationID(event.CorrelationID) {
		e.session.LastCorrelationID = event.CorrelationID
	}
	if validErrorCode(event.ErrorCode) {
		e.session.LastErrorCode = event.ErrorCode
	}
	e.session.HeartbeatAt = time.Now().UTC()
	e.refreshSteerableLocked()
}

func validOpaqueID(value string) bool {
	return value == strings.TrimSpace(value) && value != "" && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validSafeLabel(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validAccountLabel(value string) bool {
	switch value {
	case "unknown", "chatgpt", "api_key", "claude_ai_max", "claude_ai_pro", "claude_ai_team", "claude_ai_enterprise", "console":
		return true
	default:
		return false
	}
}

func validEventKind(kind EventKind) bool {
	switch kind {
	case EventSessionStarted, EventToolStarted, EventControlApplied, EventTurnStarted, EventTurnCompleted:
		return true
	default:
		return false
	}
}

func validErrorCode(code ErrorCode) bool {
	switch code {
	case ErrorEventStreamBound, ErrorAppServerProtocol, ErrorChildExitFailed, ErrorChildStopFailed, ErrorOwnershipLost, ErrorWorkspaceConflict:
		return true
	default:
		return false
	}
}

func validCorrelationID(value string) bool { return validOpaqueID(value) && len(value) <= 128 }

func (s *Supervisor) monitor(entry *sessionEntry) {
	defer close(entry.monitorDone)
	waited := make(chan error, 1)
	go func() { waited <- entry.process.Wait() }()
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			entry.mu.Lock()
			if entry.session.State == StateRunning {
				entry.session.HeartbeatAt = time.Now().UTC()
			}
			entry.mu.Unlock()
		case err := <-waited:
			now := time.Now().UTC()
			entry.mu.Lock()
			entry.session.ExitedAt = &now
			entry.session.HeartbeatAt = now
			entry.session.PID = 0
			if entry.failureCode != "" {
				entry.session.State = StateFailed
				entry.session.LastErrorCode = entry.failureCode
			} else if entry.stopRequested {
				entry.session.State = StateStopped
			} else if err != nil {
				entry.session.State = StateFailed
				entry.session.LastErrorCode = ErrorChildExitFailed
			} else {
				entry.session.State = StateExited
			}
			entry.refreshSteerableLocked()
			entry.mu.Unlock()
			persistErr := s.persist(entry)
			entry.mu.Lock()
			entry.monitorErr = persistErr
			entry.mu.Unlock()
			s.scheduleReport()
			return
		}
	}
}

func waitSessionFinalized(ctx context.Context, entry *sessionEntry) error {
	if entry == nil || entry.monitorDone == nil {
		return nil
	}
	result := func() error {
		entry.mu.Lock()
		defer entry.mu.Unlock()
		return entry.monitorErr
	}
	select {
	case <-entry.monitorDone:
		return result()
	default:
	}
	timer := time.NewTimer(sessionFinalizeTimeout)
	defer timer.Stop()
	select {
	case <-entry.monitorDone:
		return result()
	case <-ctx.Done():
		select {
		case <-entry.monitorDone:
			return result()
		default:
		}
		return ctx.Err()
	case <-timer.C:
		select {
		case <-entry.monitorDone:
			return result()
		default:
		}
		return errors.New("agentd session terminal persistence did not complete within the shutdown budget")
	}
}

func (s *Supervisor) report(ctx context.Context) {
	if s.reporter == nil {
		return
	}
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	reportCtx, cancel := context.WithTimeout(ctx, reporterTimeout)
	defer cancel()
	err := s.reporter.ReportStatus(reportCtx, s.Status())
	s.mu.Lock()
	if err != nil {
		s.reporterErrorCode = ErrorReporterUnavailable
		s.reporterFailures++
	} else {
		s.reporterErrorCode = ""
	}
	s.mu.Unlock()
}

func (s *Supervisor) heartbeatLoop() {
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()
	s.report(context.Background())
	for {
		select {
		case <-ticker.C:
			s.report(context.Background())
		case <-s.reportWake:
			s.report(context.Background())
		case <-s.done:
			return
		}
	}
}

func (s *Supervisor) scheduleReport() {
	if s.reporter == nil {
		return
	}
	select {
	case s.reportWake <- struct{}{}:
	default:
	}
}

func (s *Supervisor) CheckpointReporter(_ context.Context, id string, request ControlRequest, state ReporterState) error {
	entry, err := s.get(id)
	if err != nil {
		return err
	}
	if err := s.validateControlScope(entry, request); err != nil {
		return err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	prior := entry.session.Reporter
	entry.session.Reporter = state
	if err := s.journal.put(entry.snapshotLocked()); err != nil {
		entry.session.Reporter = prior
		return err
	}
	return nil
}

func (e *sessionEntry) refreshSteerableLocked() {
	e.session.Steerable = e.session.Managed && e.session.State == StateRunning && e.process != nil && e.controlReady && e.capabilities[CapabilitySteer]
}

func (e *sessionEntry) snapshotLocked() Session {
	out := e.session
	out.Capabilities = append([]Capability(nil), e.session.Capabilities...)
	return out
}

func (s *Supervisor) Status() Status {
	s.mu.RLock()
	entries := make([]*sessionEntry, 0, len(s.sessions))
	for _, entry := range s.sessions {
		entries = append(entries, entry)
	}
	daemonID := s.daemonID
	s.mu.RUnlock()
	sessions := make([]Session, 0, len(entries))
	for _, entry := range entries {
		entry.mu.Lock()
		sessions = append(sessions, entry.snapshotLocked())
		entry.mu.Unlock()
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].StartedAt.Equal(sessions[j].StartedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].StartedAt.Before(sessions[j].StartedAt)
	})
	s.mu.RLock()
	reporterErrorCode, reporterFailures := s.reporterErrorCode, s.reporterFailures
	s.mu.RUnlock()
	return Status{DaemonID: daemonID, Instance: s.instance, HeartbeatAt: time.Now().UTC(), Sessions: sessions,
		ReporterErrorCode: reporterErrorCode, ReporterFailureCount: reporterFailures}
}

func (s *Supervisor) persist(entry *sessionEntry) error {
	if s.journal == nil || entry == nil {
		return nil
	}
	entry.mu.Lock()
	snapshot := entry.snapshotLocked()
	entry.mu.Unlock()
	return s.journal.put(snapshot)
}

func (s *Supervisor) get(id string) (*sessionEntry, error) {
	if !validOpaqueID(id) {
		return nil, ErrSessionNotFound
	}
	s.mu.RLock()
	entry := s.sessions[id]
	s.mu.RUnlock()
	if entry == nil {
		return nil, ErrSessionNotFound
	}
	return entry, nil
}

func (s *Supervisor) validateControlScope(entry *sessionEntry, request ControlRequest) error {
	if request.Instance == "" || request.Instance != strings.TrimSpace(request.Instance) || len(request.Instance) > 512 ||
		!utf8.ValidString(request.Instance) || strings.ContainsAny(request.Instance, "\x00\r\n") || request.ProjectID <= 0 ||
		request.Identity == "" || request.Identity != strings.TrimSpace(request.Identity) || len(request.Identity) > 256 ||
		!utf8.ValidString(request.Identity) || strings.ContainsAny(request.Identity, "\x00\r\n") {
		return ErrControlScopeMismatch
	}
	entry.mu.Lock()
	matches := request.Instance == s.instance && request.ProjectID == entry.session.ProjectID && request.Identity == entry.session.Identity
	entry.mu.Unlock()
	if !matches {
		return ErrControlScopeMismatch
	}
	return nil
}

func (e *sessionEntry) replay(operation string, request ControlRequest) (Receipt, bool, error) {
	if e.controlReplays == nil {
		return Receipt{}, false, nil
	}
	replay, ok := e.controlReplays[request.CorrelationID]
	if !ok {
		// Never evict a live session's correlations: an old delivery lease can
		// arrive late, and forgetting it would turn a retry into a second effect.
		if len(e.controlReplays) >= maxControlReplayEntries {
			return Receipt{}, false, ErrControlReplayCapacity
		}
		return Receipt{}, false, nil
	}
	if replay.operation != operation || replay.textHash != sha256.Sum256([]byte(request.Text)) {
		return Receipt{}, false, ErrControlReplayConflict
	}
	return replay.receipt, true, replay.err
}

func (e *sessionEntry) remember(operation string, request ControlRequest, receipt Receipt, err error) {
	if e.controlReplays == nil {
		e.controlReplays = make(map[string]controlReplay, maxControlReplayEntries)
	}
	e.controlReplays[request.CorrelationID] = controlReplay{
		operation: operation, textHash: sha256.Sum256([]byte(request.Text)), receipt: receipt, err: err,
	}
}

func (s *Supervisor) Steer(ctx context.Context, id string, request ControlRequest) (Receipt, error) {
	if request.Text == "" || len(request.Text) > maxTextBytes || !utf8.ValidString(request.Text) ||
		strings.ContainsRune(request.Text, 0) || !validCorrelationID(request.CorrelationID) {
		return Receipt{}, errors.New("agentd steer text is invalid")
	}
	entry, err := s.get(id)
	if err != nil {
		return Receipt{}, err
	}
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	if err := s.validateControlScope(entry, request); err != nil {
		return Receipt{}, err
	}
	if receipt, ok, err := entry.replay("steer", request); err != nil || ok {
		return receipt, err
	}
	entry.mu.Lock()
	if entry.session.State != StateRunning {
		entry.mu.Unlock()
		return Receipt{}, ErrSessionNotRunning
	}
	if !entry.capabilities[CapabilitySteer] || !entry.controlReady || entry.process == nil {
		entry.mu.Unlock()
		return Receipt{}, ErrCapabilityMissing
	}
	process, identity, projectID := entry.process, entry.session.Identity, entry.session.ProjectID
	entry.mu.Unlock()
	effect, err := process.Steer(ctx, request)
	if err != nil {
		entry.remember("steer", request, Receipt{}, err)
		return Receipt{}, err
	}
	if err := validateControlEffect(request, effect); err != nil {
		entry.remember("steer", request, Receipt{}, err)
		return Receipt{}, err
	}
	_ = s.persist(entry)
	s.scheduleReport()
	receipt := s.effectReceipt("steer", id, identity, projectID, effect)
	entry.remember("steer", request, receipt, nil)
	return receipt, nil
}

func (s *Supervisor) Interrupt(ctx context.Context, id string, request ControlRequest) (Receipt, error) {
	if request.Text != "" || !validCorrelationID(request.CorrelationID) {
		return Receipt{}, errors.New("agentd interrupt request is invalid")
	}
	entry, err := s.get(id)
	if err != nil {
		return Receipt{}, err
	}
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	if err := s.validateControlScope(entry, request); err != nil {
		return Receipt{}, err
	}
	if receipt, ok, err := entry.replay("interrupt", request); err != nil || ok {
		return receipt, err
	}
	entry.mu.Lock()
	if entry.session.State != StateRunning {
		entry.mu.Unlock()
		return Receipt{}, ErrSessionNotRunning
	}
	if !entry.capabilities[CapabilityInterrupt] || !entry.controlReady || entry.process == nil {
		entry.mu.Unlock()
		return Receipt{}, ErrCapabilityMissing
	}
	process, identity, projectID := entry.process, entry.session.Identity, entry.session.ProjectID
	entry.mu.Unlock()
	effect, err := process.Interrupt(ctx, request)
	if err != nil {
		entry.remember("interrupt", request, Receipt{}, err)
		return Receipt{}, err
	}
	if err := validateControlEffect(request, effect); err != nil {
		entry.remember("interrupt", request, Receipt{}, err)
		return Receipt{}, err
	}
	_ = s.persist(entry)
	s.scheduleReport()
	receipt := s.effectReceipt("interrupt", id, identity, projectID, effect)
	entry.remember("interrupt", request, receipt, nil)
	return receipt, nil
}

func (s *Supervisor) effectReceipt(operation, id, identity string, projectID int64, effect ControlEffect) Receipt {
	return Receipt{Operation: operation, SessionID: id, Instance: s.instance, ProjectID: projectID, Identity: identity, RequestedLevel: "steer",
		EffectiveLevel: "steer", FallbackReason: "", Primitive: effect.Primitive, CorrelationID: effect.CorrelationID,
		VendorMessageID: effect.VendorMessageID, AppliedAt: time.Now().UTC()}
}

func validateControlEffect(request ControlRequest, effect ControlEffect) error {
	if effect.CorrelationID != request.CorrelationID || !validSafeLabel(effect.Primitive, 128) ||
		(effect.VendorMessageID != "" && !validOpaqueID(effect.VendorMessageID)) {
		return errors.New("agentd adapter returned invalid control evidence")
	}
	return nil
}

func (s *Supervisor) Stop(ctx context.Context, id string, request ControlRequest) (Receipt, error) {
	if request.Text != "" || !validCorrelationID(request.CorrelationID) {
		return Receipt{}, errors.New("agentd stop request is invalid")
	}
	entry, err := s.get(id)
	if err != nil {
		return Receipt{}, err
	}
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	if err := s.validateControlScope(entry, request); err != nil {
		return Receipt{}, err
	}
	if receipt, ok, replayErr := entry.replay("stop", request); replayErr != nil || ok {
		// A failed owned effect is memoized as strongly as a successful one. Do
		// not let the later child-finalization barrier replace that exact failure
		// with nil and turn a retry into a false applied receipt.
		if replayErr != nil {
			return receipt, replayErr
		}
		return receipt, waitSessionFinalized(ctx, entry)
	}
	entry.mu.Lock()
	if entry.session.State != StateRunning {
		entry.mu.Unlock()
		return Receipt{}, ErrSessionNotRunning
	}
	if !entry.capabilities[CapabilityStop] {
		entry.mu.Unlock()
		return Receipt{}, ErrCapabilityMissing
	}
	entry.stopRequested = true
	entry.session.State = StateStopping
	entry.refreshSteerableLocked()
	process, identity, projectID := entry.process, entry.session.Identity, entry.session.ProjectID
	entry.mu.Unlock()
	_ = s.persist(entry)
	s.scheduleReport()
	effect, err := process.Stop(ctx, request)
	if errors.Is(err, ErrSessionNotRunning) {
		effect = ControlEffect{Primitive: "owned process already exited", CorrelationID: request.CorrelationID}
		err = nil
	}
	if err != nil {
		entry.mu.Lock()
		if entry.session.State == StateStopping {
			entry.session.State = StateFailed
			entry.session.LastErrorCode = ErrorChildStopFailed
		}
		entry.mu.Unlock()
		_ = s.persist(entry)
		s.scheduleReport()
		entry.remember("stop", request, Receipt{}, err)
		return Receipt{}, err
	}
	if err := validateControlEffect(request, effect); err != nil {
		entry.remember("stop", request, Receipt{}, err)
		return Receipt{}, err
	}
	receipt := s.effectReceipt("stop", id, identity, projectID, effect)
	entry.remember("stop", request, receipt, nil)
	if err := waitSessionFinalized(ctx, entry); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

// Reject terminates a child whose authenticated registration authority
// refused its ownership claim. This is deliberately narrower than Stop: it
// records a typed terminal failure and closes reporting so a cross-daemon
// workspace conflict cannot leave an unregistered process running forever.
func (s *Supervisor) Reject(ctx context.Context, id string, request ControlRequest, code ErrorCode) error {
	if code != ErrorWorkspaceConflict || request.Text != "" || !validCorrelationID(request.CorrelationID) {
		return errors.New("agentd rejection is invalid")
	}
	entry, err := s.get(id)
	if err != nil {
		return err
	}
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	if err := s.validateControlScope(entry, request); err != nil {
		return err
	}
	entry.mu.Lock()
	if entry.session.State != StateRunning {
		entry.mu.Unlock()
		return ErrSessionNotRunning
	}
	entry.session.State = StateStopping
	entry.failureCode = code
	entry.refreshSteerableLocked()
	process := entry.process
	entry.mu.Unlock()
	startingPersistErr := s.persist(entry)
	effect, err := process.Stop(ctx, request)
	if err != nil {
		entry.mu.Lock()
		if entry.session.State == StateStopping {
			entry.session.State = StateRunning
			entry.failureCode = ""
			entry.refreshSteerableLocked()
		}
		entry.mu.Unlock()
		_ = s.persist(entry)
		return err
	}
	effectErr := validateControlEffect(request, effect)
	waitErr := waitSessionFinalized(ctx, entry)
	entry.mu.Lock()
	entry.session.State = StateFailed
	entry.session.LastErrorCode = code
	entry.session.Reporter.Closed = true
	entry.refreshSteerableLocked()
	entry.mu.Unlock()
	finalPersistErr := s.persist(entry)
	s.scheduleReport()
	return errors.Join(startingPersistErr, effectErr, waitErr, finalPersistErr)
}

func (s *Supervisor) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.lifecycleCancel()
	close(s.done)
	s.mu.Unlock()

	// Start holds this gate from reservation through Process publication. The
	// lifecycle cancellation above makes an in-flight adapter unwind; taking
	// the gate guarantees no starting child can be skipped by the snapshot.
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	entries := make([]*sessionEntry, 0, len(s.sessions))
	for _, entry := range s.sessions {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	var errs []error
	for _, entry := range entries {
		entry.controlMu.Lock()
		entry.mu.Lock()
		state := entry.session.State
		if entry.process != nil && (state == StateRunning || state == StateStopping || state == StateFailed) {
			if state == StateRunning {
				entry.stopRequested = true
				entry.session.State = StateStopping
			}
			process := entry.process
			entry.mu.Unlock()
			if _, err := process.Stop(ctx, ControlRequest{CorrelationID: "agentd-shutdown"}); err != nil {
				errs = append(errs, err)
			}
			entry.controlMu.Unlock()
			continue
		}
		entry.mu.Unlock()
		entry.controlMu.Unlock()
	}
	for _, entry := range entries {
		if err := waitSessionFinalized(ctx, entry); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type unsupportedAdapter struct{ name string }

func NewUnsupportedAdapter(name string) Adapter        { return &unsupportedAdapter{name: name} }
func (a *unsupportedAdapter) Name() string             { return a.name }
func (*unsupportedAdapter) Capabilities() []Capability { return nil }
func (*unsupportedAdapter) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	return nil, ErrAdapterUnsupported
}
