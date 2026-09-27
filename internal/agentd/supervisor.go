// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/localjournal"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

type Config struct {
	API               API
	StateRoot         string
	DaemonID          string
	Workspace         string
	Adapters          []Adapter
	EstimatedUnits    map[string]int64
	Accounts          []EnrolledAccount
	HeartbeatInterval time.Duration
	MaxRunDuration    time.Duration
}

type replay struct {
	Digest  string  `json:"digest"`
	Receipt Receipt `json:"receipt"`
}

// Record contains only local process provenance and bounded control digests.
// The journal is AEON v2; classic journals are never opened implicitly.
type Record struct {
	TenantID    string            `json:"tenant_id"`
	PrincipalID string            `json:"principal_id"`
	RunID       string            `json:"run_id"`
	WorkOrderID string            `json:"work_order_id,omitempty"`
	Generation  string            `json:"generation"`
	Workspace   string            `json:"workspace"`
	PID         int               `json:"pid"`
	State       string            `json:"state"`
	Sequence    int64             `json:"sequence"`
	Controls    map[string]replay `json:"controls,omitempty"`
}

type owned struct {
	mu              sync.Mutex
	harnessMu       sync.Mutex
	record          Record
	harness         HarnessSession
	usage           *sessionUsageReporter
	inboxCapable    bool
	pending         []HarnessControl
	process         Process
	tools           *managedToolServer
	replies         map[string]string // delivered message ID -> sender principal
	replyOrder      []string
	doneRequested   bool
	monitorDone     chan struct{}
	stopRequested   bool
	forceRequested  bool
	harnessArchived bool
}

type Supervisor struct {
	mu                sync.Mutex
	api               API
	journal           *localjournal.Journal[Record]
	lock              *os.File
	adapters          map[string]Adapter
	runs              map[string]*owned
	tenantID          string
	principalID       string
	daemonID          string
	generation        string
	workspace         string
	estimates         map[string]int64
	accounts          []EnrolledAccount
	heartbeatInterval time.Duration
	maxRunDuration    time.Duration
}

func NewSupervisor(ctx context.Context, c Config) (*Supervisor, error) {
	if c.API == nil || c.DaemonID == "" || len(c.DaemonID) > 128 || strings.ContainsAny(c.DaemonID, "/\\\x00\r\n") ||
		c.StateRoot == "" || !filepath.IsAbs(c.StateRoot) {
		return nil, errors.New("invalid daemon configuration")
	}
	physical, err := filepath.EvalSymlinks(c.Workspace)
	if err != nil || !filepath.IsAbs(c.Workspace) || physical != c.Workspace {
		return nil, errors.New("workspace must be an existing physical absolute path")
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		return nil, errors.New("workspace is not a directory")
	}
	tenantID, principalID, err := c.API.Identity(ctx)
	if err != nil {
		return nil, errors.New("AEON agent identity preflight failed")
	}
	if tenantID == "" || principalID == "" {
		return nil, errors.New("agent identity unavailable")
	}
	gen, err := randomID()
	if err != nil {
		return nil, err
	}
	adapters := make(map[string]Adapter)
	for _, a := range c.Adapters {
		if a == nil || a.Name() == "" || adapters[a.Name()] != nil {
			return nil, errors.New("duplicate or invalid adapter")
		}
		adapters[a.Name()] = a
	}
	if len(adapters) == 0 {
		return nil, errors.New("no adapters configured")
	}
	heartbeat := c.HeartbeatInterval
	if heartbeat == 0 {
		heartbeat = 15 * time.Second
	}
	if heartbeat < time.Second || heartbeat > time.Minute {
		return nil, errors.New("invalid heartbeat interval")
	}
	maxRun := c.MaxRunDuration
	if maxRun == 0 {
		maxRun = 4 * time.Hour
	}
	if maxRun < time.Second || maxRun > 24*time.Hour {
		return nil, errors.New("invalid child duration bound")
	}
	for unit, estimate := range c.EstimatedUnits {
		if (unit != "requests" && unit != "tokens" && unit != "cost_micros") || estimate < 1 {
			return nil, errors.New("invalid allowance estimate")
		}
	}
	for _, account := range c.Accounts {
		if account.ID == "" || account.Key == "" || adapters[account.Harness] == nil {
			return nil, errors.New("invalid local account enrollment")
		}
		if _, ok := adapters[account.Harness].(AccountProber); !ok {
			return nil, errors.New("adapter has no account probe")
		}
	}
	lock, err := acquireInstanceLock(c.StateRoot, c.DaemonID)
	if err != nil {
		return nil, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = lock.Close()
		}
	}()
	j, err := localjournal.Open(localjournal.Config[Record]{
		Directory: c.StateRoot, Prefix: "aeon-agentd-" + c.DaemonID, Version: 2,
		MaxBytes: 4 << 20, MaxRecords: 4096,
		Key: func(r Record) (string, error) {
			if r.RunID == "" {
				return "", errors.New("missing run")
			}
			return r.RunID, nil
		},
		Validate: func(r Record) error {
			if r.TenantID != tenantID || r.PrincipalID != principalID || r.Generation == "" || r.RunID == "" || r.Sequence < 0 || len(r.Controls) > 256 {
				return errors.New("invalid AEON journal binding")
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	s := &Supervisor{api: c.API, journal: j, lock: lock, adapters: adapters, runs: map[string]*owned{}, tenantID: tenantID,
		principalID: principalID, daemonID: c.DaemonID, generation: gen, workspace: physical, estimates: c.EstimatedUnits, accounts: c.Accounts,
		heartbeatInterval: heartbeat, maxRunDuration: maxRun}
	for _, rec := range j.Snapshot() {
		// A persisted PID is never proof of ownership after a restart.
		if rec.State == "running" || rec.State == "starting" || rec.State == "waiting" {
			rec.State = "ownership_lost"
			if err := j.Put(rec); err != nil {
				return nil, err
			}
		}
		s.runs[rec.RunID] = &owned{record: rec}
	}
	// Publish configured public metadata once per daemon generation. Probe
	// polling must not overwrite a person's subsequent display/grant edits.
	for _, account := range c.Accounts {
		if account.Metadata == nil {
			continue
		}
		reporter, ok := c.API.(interface {
			PublishAccountMetadata(context.Context, string, AccountMetadata) error
		})
		if !ok {
			return nil, errors.New("account metadata API unavailable")
		}
		if err := reporter.PublishAccountMetadata(ctx, account.ID, *account.Metadata); err != nil {
			return nil, err
		}
	}
	keepLock = true
	return s, nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Supervisor) Generation() string  { return s.generation }
func (s *Supervisor) DaemonID() string    { return s.daemonID }
func (s *Supervisor) TenantID() string    { return s.tenantID }
func (s *Supervisor) PrincipalID() string { return s.principalID }

func (s *Supervisor) Status() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.runs))
	for _, entry := range s.runs {
		entry.mu.Lock()
		view := entry.record
		view.Workspace = ""
		view.Controls = nil
		out = append(out, view)
		entry.mu.Unlock()
	}
	return out
}

// PollOnce fetches queued work. Owned inbox messages are leased by each managed
// harness session, so they cannot race a separate principal-wide inbox poll.
// A missing or stale reservation fails closed and leaves the run queued.
func (s *Supervisor) PollOnce(ctx context.Context) error {
	for _, account := range s.accounts {
		probe := s.adapters[account.Harness].(AccountProber)
		available := probe.Probe(ctx, account.Key)
		if err := s.api.Probe(ctx, account.ID, s.daemonID, s.generation, available); err != nil {
			return err
		}
	}
	runs, err := s.api.Queued(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.AgentPrincipalID != s.principalID {
			return ErrScope
		}
		if err := s.StartRun(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (s *Supervisor) StartRun(ctx context.Context, run Run) error {
	if run.ID == "" || run.WorkOrderID == "" || run.AgentPrincipalID != s.principalID || run.Status != "queued" {
		return ErrScope
	}
	s.mu.Lock()
	if s.runs[run.ID] != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	profiles, err := s.api.Profiles(ctx)
	if err != nil {
		return err
	}
	var profile Profile
	for _, p := range profiles {
		if p.ID == run.ModelProfileID {
			profile = p
			break
		}
	}
	adapter := s.adapters[profile.Harness]
	if adapter == nil || profile.ID == "" {
		return ErrUnsupported
	}
	node, err := s.api.Node(ctx, run.WorkOrderID)
	if err != nil {
		return err
	}
	if node.ID != run.WorkOrderID || strings.TrimSpace(node.Title) == "" {
		return errors.New("work order node unavailable")
	}
	if node.Key == "" {
		return errors.New("work order key unavailable")
	}
	order, err := s.api.WorkOrder(ctx, run.WorkOrderID)
	if err != nil {
		return err
	}
	if order.NodeID != run.WorkOrderID || (order.Status != "ready" && order.Status != "running") {
		return errors.New("work order is not dispatchable")
	}
	duration := s.maxRunDuration
	if order.MaxDurationSeconds != nil {
		if *order.MaxDurationSeconds <= 0 {
			return errors.New("work order duration invalid")
		}
		if *order.MaxDurationSeconds < int64(duration/time.Second) {
			duration = time.Duration(*order.MaxDurationSeconds) * time.Second
		}
	}
	prompt := node.Title
	if node.Body != "" {
		prompt += "\n\n" + node.Body
	}
	prompt += "\n\nAcceptance criteria (check each in Aeon and attach evidence):"
	for _, criterion := range order.Criteria {
		prompt += "\n- " + criterion.ID + ": " + criterion.Description
	}
	branch := ""
	if output, branchErr := exec.CommandContext(ctx, "git", "-C", s.workspace, "branch", "--show-current").Output(); branchErr == nil {
		branch = strings.TrimSpace(string(output))
	}
	prompt += "\n\nRun contract: You are bound to work order " + node.Key + " and run " + run.ID + ". Work only in this workspace. Current branch: " + branch + ". Use the Aeon tools to comment, check criteria, attach evidence, request approval, reply, and set status. Use aeon_terminal for tests and a local commit; never push without person approval. Report the commit ID and remaining blockers in your final reply."
	if len(prompt) > 256<<10 {
		return errors.New("work order prompt exceeds local bound")
	}
	if len(s.estimates) == 0 {
		return errors.New("allowance estimates required")
	}
	accountIDs := make([]string, 0, len(s.accounts))
	for _, account := range s.accounts {
		if account.Harness == profile.Harness {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	if len(accountIDs) == 0 {
		return errors.New("no local account enrollment for harness")
	}
	route, err := s.api.Route(ctx, run.ID, s.daemonID, accountIDs, s.estimates)
	if err != nil {
		return err
	}
	if route.DaemonID != s.daemonID || route.AccountKey == "" || len(route.Reservations) == 0 {
		return errors.New("account route is not bound to daemon")
	}
	localBinding := false
	for _, account := range s.accounts {
		if account.ID == route.AccountID && account.Key == route.AccountKey && account.Harness == profile.Harness {
			localBinding = true
			break
		}
	}
	if !localBinding {
		return errors.New("account route has no matching local enrollment")
	}
	ids := make([]string, 0, len(route.Reservations))
	for _, reservation := range route.Reservations {
		if reservation.ID == "" {
			return errors.New("empty account reservation")
		}
		ids = append(ids, reservation.ID)
	}
	if err := s.api.Claim(ctx, run.ID, s.daemonID, s.generation, ids); err != nil {
		return err
	}
	rec := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: run.ID, WorkOrderID: run.WorkOrderID, Generation: s.generation, Workspace: s.workspace, State: "starting", Controls: map[string]replay{}}
	if err := s.journal.Put(rec); err != nil {
		return err
	}
	entry := &owned{record: rec, replies: map[string]string{}}
	s.mu.Lock()
	s.runs[run.ID] = entry
	s.mu.Unlock()
	projectID, err := s.api.ProjectForNode(ctx, node.Key)
	if err != nil {
		_ = s.update(ctx, entry, Telemetry{Kind: "finished", Status: "failed", ErrorCode: "child_exit_failed"})
		return err
	}
	host, err := os.Hostname()
	if err != nil || host == "" || len(host) > 128 {
		_ = s.update(ctx, entry, Telemetry{Kind: "finished", Status: "failed", ErrorCode: "child_exit_failed"})
		return errors.New("valid harness host unavailable")
	}
	ref, err := randomID()
	if err != nil {
		return err
	}
	leaseA, err := randomID()
	if err != nil {
		return err
	}
	leaseB, err := randomID()
	if err != nil {
		return err
	}
	caps := []string{"status", "stop"}
	if profile.Harness != Grok {
		caps = append(caps, "interrupt")
	}
	entry.inboxCapable = profile.Harness == Claude || profile.Harness == Codex || profile.Harness == Pi
	if entry.inboxCapable {
		caps = append(caps, "inbox", "steer")
	}
	entry.harness, err = s.api.RegisterHarness(ctx, HarnessSession{ID: s.generation + "/" + ref, ProjectID: projectID, Lease: leaseA + leaseB,
		Model: profile.Model, ReasoningEffort: profile.Effort, AccountLabel: route.AccountLabel},
		s.principalID, run.ID, run.WorkOrderID, profile.Harness, host, caps)
	if err != nil {
		_ = s.update(ctx, entry, Telemetry{Kind: "finished", Status: "failed", ErrorCode: "child_exit_failed"})
		return err
	}
	if usageAPI, ok := s.api.(sessionUsageAPI); ok && profile.Harness == Codex {
		entry.usage = newSessionUsageReporter(usageAPI, entry.harness, func() bool {
			entry.mu.Lock()
			defer entry.mu.Unlock()
			return entry.harnessArchived || entry.record.Generation != s.generation
		}, func() {
			entry.mu.Lock()
			entry.harnessArchived = true
			entry.mu.Unlock()
		})
	}
	closeHarness := func(reason string) {
		s.finishSessionUsage(entry)
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.stopHarness(cleanup, entry, reason)
	}
	var runTools *RunTools
	if toolAPI, ok := s.api.(RunToolAPI); ok {
		if signer, ok := s.api.(interface {
			runCredential(string, string, string, string) string
		}); ok {
			active := func() bool {
				entry.mu.Lock()
				defer entry.mu.Unlock()
				return entry.record.Generation == s.generation && (entry.record.State == "starting" || entry.record.State == "running")
			}
			replySender := func(messageID string) (string, bool) {
				entry.mu.Lock()
				defer entry.mu.Unlock()
				sender, ok := entry.replies[messageID]
				return sender, ok
			}
			requestDone := func() {
				entry.mu.Lock()
				entry.doneRequested = true
				entry.mu.Unlock()
			}
			entry.tools, err = startManagedTools(signer.runCredential(s.tenantID, s.principalID, run.ID, s.generation),
				toolBinding{api: toolAPI, workOrderID: run.WorkOrderID, runID: run.ID, workspace: s.workspace, branch: branch, active: active, replySender: replySender, requestDone: requestDone})
			if err != nil {
				_ = s.update(ctx, entry, Telemetry{Kind: "finished", Status: "failed", ErrorCode: "child_exit_failed"})
				closeHarness("process_failed")
				return err
			}
			runTools = &entry.tools.tools
		}
	}
	observe := func(ev AdapterEvent) { s.observe(entry, ev) }
	proc, err := adapter.Start(ctx, StartRequest{TenantID: s.tenantID, PrincipalID: s.principalID, Run: run, Profile: profile,
		AccountKey: route.AccountKey, Workspace: s.workspace, StateRoot: filepath.Dir(s.journal.JournalPath()), Prompt: prompt, Generation: s.generation, Tools: runTools}, observe)
	if err != nil {
		_ = entry.tools.Close()
		_ = s.update(ctx, entry, Telemetry{Kind: "finished", Status: "failed", ErrorCode: "child_exit_failed"})
		closeHarness("process_failed")
		return err
	}
	entry.mu.Lock()
	entry.process = proc
	entry.record.PID = proc.PID()
	entry.record.State = "running"
	saveErr := s.journal.Put(entry.record)
	entry.mu.Unlock()
	if saveErr != nil {
		_ = entry.tools.Close()
		_ = proc.Stop(ctx)
		closeHarness("process_failed")
		return saveErr
	}
	if err := s.update(ctx, entry, Telemetry{Kind: "started", Status: "running"}); err != nil {
		_ = entry.tools.Close()
		_ = proc.Stop(ctx)
		closeHarness("process_failed")
		return err
	}
	heartbeatErr := s.heartbeatHarness(ctx, entry)
	entry.mu.Lock()
	entry.monitorDone = make(chan struct{})
	if errors.Is(heartbeatErr, ErrHarnessArchived) {
		entry.harnessArchived = true
		heartbeatErr = nil
	}
	if heartbeatErr != nil {
		entry.stopRequested = true
	}
	entry.mu.Unlock()
	go s.monitor(entry)
	if heartbeatErr != nil {
		_ = proc.Stop(ctx)
		return heartbeatErr
	}
	go s.heartbeat(entry, duration)
	return nil
}

func (s *Supervisor) heartbeat(entry *owned, duration time.Duration) {
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	entry.mu.Lock()
	done := entry.monitorDone
	entry.mu.Unlock()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := s.update(ctx, entry, Telemetry{Kind: "heartbeat"})
			if err == nil {
				err = s.serviceHarness(ctx, entry)
			}
			cancel()
			if errors.Is(err, ErrControlUnconfirmed) {
				continue
			}
			if err != nil {
				entry.mu.Lock()
				proc := entry.process
				entry.mu.Unlock()
				if proc != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					_ = proc.Stop(ctx)
					cancel()
				}
				return
			}
		case <-deadline.C:
			entry.mu.Lock()
			proc := entry.process
			entry.stopRequested = true
			entry.mu.Unlock()
			if proc != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = proc.Stop(ctx)
				cancel()
			}
			return
		}
	}
}

func (s *Supervisor) observe(entry *owned, ev AdapterEvent) {
	if ev.SessionUsage != nil {
		entry.usage.submit(*ev.SessionUsage)
	}
	if ev.Kind == "" {
		return
	}
	kind := ev.Kind
	if kind != "turn" && kind != "tool" && kind != "usage" && kind != "heartbeat" {
		kind = "status"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.update(ctx, entry, Telemetry{Kind: kind, InputTokensDelta: ev.InputTokensDelta,
		OutputTokensDelta: ev.OutputTokensDelta, CostMicrosDelta: ev.CostMicrosDelta, TurnCountDelta: ev.TurnCountDelta,
		EffectiveModel: ev.EffectiveModel, ModelEvidence: ev.ModelEvidence, ErrorCode: ev.ErrorCode}); err != nil {
		entry.mu.Lock()
		proc := entry.process
		entry.mu.Unlock()
		if proc != nil {
			_ = proc.Stop(ctx)
		}
	}
}

func (s *Supervisor) monitor(entry *owned) {
	entry.mu.Lock()
	proc := entry.process
	done := entry.monitorDone
	entry.mu.Unlock()
	if done != nil {
		defer close(done)
	}
	if proc == nil {
		return
	}
	defer entry.tools.Close()
	err := proc.Wait()
	s.finishSessionUsage(entry)
	entry.harnessMu.Lock()
	defer entry.harnessMu.Unlock()
	entry.mu.Lock()
	stopped := entry.stopRequested
	forced := entry.forceRequested
	entry.mu.Unlock()
	if err == nil {
		if evidence, ok := proc.(EvidenceProcess); ok {
			answer := evidence.Evidence()
			if answer == "" {
				err = errors.New("agent output unavailable")
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err = s.api.AddEvidence(ctx, entry.record.WorkOrderID, entry.record.RunID, answer)
				cancel()
			}
		}
	}
	status, code := "completed", ""
	if err != nil {
		status, code = "failed", "child_exit_failed"
	}
	if stopped {
		status, code = "cancelled", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reportErr := s.update(ctx, entry, Telemetry{Kind: "finished", Status: status, ErrorCode: code})
	entry.mu.Lock()
	doneRequested := entry.doneRequested
	entry.mu.Unlock()
	if reportErr == nil && status == "completed" && doneRequested {
		if toolAPI, ok := s.api.(RunToolAPI); ok {
			order, err := s.api.WorkOrder(ctx, entry.record.WorkOrderID)
			if err == nil {
				_, err = toolAPI.SetWorkStatus(ctx, order.NodeID, order.Revision, "done")
			}
			if err != nil {
				_ = toolAPI.Comment(ctx, entry.record.WorkOrderID, "The run completed, but the requested done transition was rejected. Check the work-order criteria, evidence, and revision.")
			}
		}
	}
	reason := "process_exited"
	if status == "failed" {
		reason = "process_failed"
	} else if status == "cancelled" {
		reason = "stopped"
		if forced {
			reason = "force_stopped"
		}
	}
	s.stopHarness(ctx, entry, reason)
}

func (s *Supervisor) finishSessionUsage(entry *owned) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := entry.usage.finish(ctx); err != nil && !errors.Is(err, ErrHarnessArchived) {
		// No server errors, vendor fields, lease or account context in diagnostics.
		slog.Warn("managed session usage settlement incomplete")
	}
}

func (s *Supervisor) stopHarness(ctx context.Context, entry *owned, reason string) {
	entry.mu.Lock()
	archived := entry.harnessArchived
	entry.mu.Unlock()
	if !archived {
		_ = s.api.StopHarness(ctx, entry.harness, reason)
	}
}

// serviceHarness uses the harness module's yield claim queue and managed inbox
// drain. The harness lock keeps completion ahead of terminal session closure.
func (s *Supervisor) serviceHarness(ctx context.Context, entry *owned) (result error) {
	entry.harnessMu.Lock()
	defer entry.harnessMu.Unlock()
	defer func() {
		if errors.Is(result, ErrHarnessArchived) {
			entry.mu.Lock()
			entry.harnessArchived = true
			entry.mu.Unlock()
			entry.pending = nil
			result = nil
		}
	}()
	entry.mu.Lock()
	running := entry.record.State == "running" && !entry.harnessArchived
	entry.mu.Unlock()
	if !running {
		return nil
	}
	controls, err := s.api.YieldHarness(ctx, entry.harness)
	if err != nil {
		return err
	}
	entry.pending = append(entry.pending, controls...)
	for len(entry.pending) > 0 {
		control := entry.pending[0]
		outcome, reason := "applied", "agentd_applied"
		if control.Kind == "force_stop" {
			reason = "owned_group_signalled_root_exited"
		}
		_, err := s.control(ctx, ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID,
			RunID: entry.record.RunID, Generation: s.generation, CorrelationID: control.ID, Operation: control.Kind, ExpectedOwnership: control.ExpectedOwnership, ExpiresAt: control.ExpiresAt}, true)
		if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrNotOwned) || errors.Is(err, ErrGeneration) {
			outcome, reason, err = "rejected", "child_unavailable", nil
		}
		if errors.Is(err, ErrGracefulTimeout) {
			outcome, reason, err = "rejected", "graceful_stop_timeout", nil
		}
		if errors.Is(err, ErrForceExitUnconfirmed) {
			outcome, reason, err = "rejected", "owned_group_signalled_root_exit_unconfirmed", nil
		}
		if err != nil {
			if control.Kind == "force_stop" || control.Kind == "stop" {
				return ErrControlUnconfirmed
			}
			return err
		}
		if err := s.api.CompleteHarnessControl(ctx, entry.harness, control.ID, outcome, reason); err != nil {
			if !errors.Is(err, ErrHarnessArchived) && (control.Kind == "force_stop" || control.Kind == "stop") {
				return ErrControlUnconfirmed
			}
			return err
		}
		entry.pending = entry.pending[1:]
	}
	if entry.inboxCapable {
		// A drain returns at most one leased message and replays it until completed.
		items, err := s.api.DrainHarness(ctx, entry.harness)
		if err != nil {
			return err
		}
		for _, item := range items {
			if len(item.Body) > 64<<10 {
				return errors.New("harness delivery exceeds local bound")
			}
			messageText := item.Body
			if item.MessageID != "" && item.SenderPrincipalID != "" {
				messageText = "Aeon inbox message " + item.MessageID + " from " + item.SenderPrincipalID + ":\n" + item.Body
			}
			if len(messageText) > 64<<10 {
				messageText = item.Body
			}
			req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID,
				RunID: entry.record.RunID, Generation: s.generation, CorrelationID: item.ID,
				Operation: "steer", Text: messageText}
			if item.SenderPrincipalID == s.principalID {
				var in inboxControl
				if json.Unmarshal([]byte(item.Body), &in) == nil && in.RunID != "" {
					req.RunID, req.Generation, req.Operation, req.Text = in.RunID, in.Generation, in.Operation, in.Text
				}
			}
			if item.MessageID != "" && item.SenderPrincipalID != "" && item.SenderPrincipalID != s.principalID {
				entry.mu.Lock()
				if _, seen := entry.replies[item.MessageID]; !seen {
					if len(entry.replyOrder) == 256 {
						delete(entry.replies, entry.replyOrder[0])
						entry.replyOrder = entry.replyOrder[1:]
					}
					entry.replyOrder = append(entry.replyOrder, item.MessageID)
				}
				entry.replies[item.MessageID] = item.SenderPrincipalID
				entry.mu.Unlock()
			}
			if _, err := s.Control(ctx, req); err != nil {
				return err
			}
			if err := s.api.CompleteHarnessDelivery(ctx, entry.harness, item); err != nil {
				return err
			}
		}
	}
	return s.heartbeatHarness(ctx, entry)
}

func (s *Supervisor) update(ctx context.Context, entry *owned, t Telemetry) error {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.record.Generation != s.generation {
		return ErrGeneration
	}
	if t.Kind == "heartbeat" && (entry.record.State == "completed" || entry.record.State == "failed" || entry.record.State == "cancelled" || entry.record.State == "ownership_lost") {
		return nil
	}
	t.Sequence = entry.record.Sequence + 1
	if err := s.api.Report(ctx, entry.record.RunID, t); err != nil {
		return err
	}
	entry.record.Sequence = t.Sequence
	if t.Status != "" {
		entry.record.State = t.Status
	}
	return s.journal.Put(entry.record)
}

func (s *Supervisor) Control(ctx context.Context, req ControlRequest) (Receipt, error) {
	return s.control(ctx, req, false)
}

// Force authorization originates only in the server's human-confirmed recovery
// queue. Local transport credentials and inbox messages cannot mint that grant.
func (s *Supervisor) control(ctx context.Context, req ControlRequest, fromRecoveryQueue bool) (Receipt, error) {
	if req.Operation == "force_stop" && !fromRecoveryQueue {
		return Receipt{}, ErrUnsupported
	}
	if req.TenantID != s.tenantID || req.PrincipalID != s.principalID || req.Generation != s.generation ||
		req.RunID == "" || req.CorrelationID == "" || len(req.CorrelationID) > 128 {
		return Receipt{}, ErrScope
	}
	if req.Operation != "steer" && req.Operation != "interrupt" && req.Operation != "resume" && req.Operation != "stop" && req.Operation != "force_stop" {
		return Receipt{}, ErrUnsupported
	}
	if len(req.Text) > 64<<10 || (req.Operation != "steer" && req.Text != "") {
		return Receipt{}, errors.New("invalid control body")
	}
	s.mu.Lock()
	entry := s.runs[req.RunID]
	s.mu.Unlock()
	if entry == nil {
		return Receipt{}, ErrNotOwned
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()

	payload, _ := json.Marshal(struct {
		Ownership *ownedprocess.Identity
		ExpiresAt *time.Time
	}{req.ExpectedOwnership, req.ExpiresAt})
	digest := sha256.Sum256([]byte(req.Operation + "\x00" + req.Text + "\x00" + string(payload)))
	key := hex.EncodeToString(digest[:])
	if prior, ok := entry.record.Controls[req.CorrelationID]; ok {
		if prior.Digest != key {
			return Receipt{}, ErrReplay
		}
		return prior.Receipt, nil
	}
	if entry.record.Generation != s.generation || entry.process == nil || entry.record.State != "running" || entry.harnessArchived {
		return Receipt{}, ErrNotOwned
	}
	if len(entry.record.Controls) >= 256 {
		return Receipt{}, errors.New("control replay capacity reached")
	}
	var err error
	if req.Operation == "force_stop" {
		recovery, ok := entry.process.(RecoveryProcess)
		expected := req.ExpectedOwnership
		if !ok || expected == nil || req.ExpiresAt == nil || !req.ExpiresAt.After(time.Now()) {
			return Receipt{}, ErrUnsupported
		}
		if expected.DaemonID != s.daemonID || expected.Generation != s.generation {
			return Receipt{}, ErrGeneration
		}
		current, e := recovery.Ownership()
		if e != nil || current.ProcessID != expected.ProcessID || current.RootPID != expected.RootPID || current.GroupID != expected.GroupID || !current.StartedAt.Equal(expected.StartedAt) {
			return Receipt{}, ErrNotOwned
		}
		entry.stopRequested = true
		entry.forceRequested = true
		err = recovery.ForceStop(ctx, *expected, *req.ExpiresAt)
		if err != nil && !errors.Is(err, ErrForceExitUnconfirmed) {
			entry.forceRequested = false
			entry.stopRequested = false
			if errors.Is(err, ownedprocess.ErrAuthorizationExpired) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				err = ErrUnsupported
			}
		}
	} else if req.Operation == "stop" {
		graceful, ok := entry.process.(GracefulProcess)
		if !ok {
			return Receipt{}, ErrUnsupported
		}
		entry.stopRequested = true
		err = graceful.GracefulStop(ctx)
		if err != nil && !errors.Is(err, ErrGracefulTimeout) {
			entry.stopRequested = false
		}
	} else {
		err = entry.process.Control(ctx, req.Operation, req.Text)
	}
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{RunID: req.RunID, Generation: s.generation, CorrelationID: req.CorrelationID, Operation: req.Operation, AppliedAt: time.Now().UTC()}
	entry.record.Controls[req.CorrelationID] = replay{Digest: key, Receipt: receipt}
	if err := s.journal.Put(entry.record); err != nil {
		return Receipt{}, fmt.Errorf("persist control receipt: %w", err)
	}
	return receipt, nil
}

func (s *Supervisor) Close(ctx context.Context) error {
	s.mu.Lock()
	entries := make([]*owned, 0, len(s.runs))
	for _, e := range s.runs {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	var first error
	for _, e := range entries {
		e.mu.Lock()
		proc := e.process
		done := e.monitorDone
		e.mu.Unlock()
		if proc != nil {
			e.mu.Lock()
			e.stopRequested = true
			e.mu.Unlock()
			if err := proc.Stop(ctx); err != nil {
				e.mu.Lock()
				e.stopRequested = false
				e.mu.Unlock()
				if first == nil {
					first = err
				}
			}
		}
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				if first == nil {
					first = ctx.Err()
				}
			}
		}
	}
	if s.lock != nil {
		if err := s.lock.Close(); err != nil && first == nil {
			first = err
		}
		s.lock = nil
	}
	return first
}

// heartbeatHarness publishes only the process the current daemon actually owns.
// A restarted daemon has no in-memory Process and cannot re-adopt a journal PID.
func (s *Supervisor) heartbeatHarness(ctx context.Context, entry *owned) error {
	entry.mu.Lock()
	if entry.harnessArchived {
		entry.mu.Unlock()
		return ErrHarnessArchived
	}
	session := entry.harness
	if recovery, ok := entry.process.(RecoveryProcess); ok && entry.record.Generation == s.generation && entry.record.State == "running" {
		if identity, err := recovery.Ownership(); err == nil {
			identity.DaemonID, identity.Generation = s.daemonID, s.generation
			session.Ownership = &identity
		}
	}
	entry.mu.Unlock()
	return s.api.HeartbeatHarness(ctx, session, "working")
}
