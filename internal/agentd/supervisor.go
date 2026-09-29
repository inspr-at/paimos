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
	"sync/atomic"
	"time"
	"unicode"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/localjournal"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

type Config struct {
	MaxTokens         int64
	MaxTurns          int64
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
	SettingRejected bool    `json:"setting_rejected,omitempty"`
	Rejected        bool    `json:"rejected,omitempty"`
	Digest          string  `json:"digest"`
	Receipt         Receipt `json:"receipt"`
}

// Record contains only local process provenance and bounded control digests.
// The journal is AEON v2; classic journals are never opened implicitly.
type Record struct {
	BudgetStopReason      string `json:"budget_stop_reason,omitempty"`
	BudgetStopUnconfirmed bool   `json:"budget_stop_unconfirmed,omitempty"`
	// Only launchPrepared proves that adapter.Start has never been called.
	// Empty is a legacy record, never evidence that no child was forked.
	LaunchState   string            `json:"launch_state,omitempty"`
	ClaimRoute    *Route            `json:"claim_route,omitempty"`
	AccountID     string            `json:"account_id,omitempty"`
	ExecutionMode string            `json:"execution_mode,omitempty"`
	ExitObserved  bool              `json:"exit_observed,omitempty"`
	Pending       []Telemetry       `json:"pending,omitempty"`
	SettlementGap bool              `json:"settlement_gap,omitempty"`
	TenantID      string            `json:"tenant_id"`
	PrincipalID   string            `json:"principal_id"`
	RunID         string            `json:"run_id"`
	WorkOrderID   string            `json:"work_order_id,omitempty"`
	Generation    string            `json:"generation"`
	Workspace     string            `json:"workspace"`
	PID           int               `json:"pid"`
	State         string            `json:"state"`
	Sequence      int64             `json:"sequence"`
	Controls      map[string]replay `json:"controls,omitempty"`
}

type owned struct {
	budgetMu                                       sync.Mutex
	budgetProcess                                  Process
	budgetTools                                    *managedToolServer
	controlMu                                      sync.Mutex
	managedPolicy                                  bool
	tokenBudget, turnBudget, tokensUsed, turnsUsed int64
	budgetReason                                   string
	budgetStopStarted, budgetStopUnconfirmed       bool

	deadlineExpired atomic.Bool
	mu              sync.Mutex
	harnessMu       sync.Mutex
	record          Record
	harness         HarnessSession
	heartbeatSeq    int64
	metadataSeq     uint64
	metadataPending []harnessMetadata
	usage           *sessionUsageReporter
	capacityPending map[string]capacity.Reading
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
	protocolFailed  bool
	protocolStopped bool
}

type harnessMetadata struct {
	seq           uint64
	model, effort string
}

type Supervisor struct {
	maxTokens, maxTurns int64

	dispatchMu        sync.Mutex
	state             *agentsetup.Store
	closing           bool
	blockedAccounts   map[string]bool
	probedAccounts    map[string]bool
	loginRequired     map[string]bool
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
	prepareScratch    func(string) (string, error)
	newHarnessID      func() (string, error)
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
	if c.MaxTokens < 0 || c.MaxTurns < 0 {
		return nil, errors.New("invalid managed budget")
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = defaultTokenBudget
	}
	if c.MaxTurns == 0 {
		c.MaxTurns = defaultTurnBudget
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
	state, err := agentsetup.OpenStore(c.StateRoot, true)
	if err != nil {
		return nil, err
	}
	keepState := false
	defer func() {
		if !keepState {
			state.Close()
		}
	}()
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
	for _, suffix := range []string{".journal", ".checkpoint.json"} {
		if _, e := state.Read("aeon-agentd-"+c.DaemonID+suffix, 4<<20); e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
	}
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
			if err := validateLaunchRecord(r); err != nil {
				return err
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	s := &Supervisor{maxTokens: c.MaxTokens, maxTurns: c.MaxTurns, state: state, blockedAccounts: map[string]bool{}, probedAccounts: map[string]bool{}, loginRequired: map[string]bool{}, api: c.API, journal: j, lock: lock, adapters: adapters, runs: map[string]*owned{}, tenantID: tenantID,
		principalID: principalID, daemonID: c.DaemonID, generation: gen, workspace: physical, estimates: c.EstimatedUnits, accounts: c.Accounts,
		heartbeatInterval: heartbeat, maxRunDuration: maxRun, prepareScratch: verificationScratch, newHarnessID: randomID}
	for _, rec := range j.Snapshot() {
		// A persisted PID is never proof of ownership after a restart.
		if !noLocalProcess(rec) && rec.State != "ownership_lost" {
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
	keepState = true
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
	entries := make([]*owned, 0, len(s.runs))
	for _, entry := range s.runs {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	out := make([]Record, 0, len(entries))
	for _, entry := range entries {
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
	// Recover no-launch claims before a fresh probe changes account generation.
	recoveryErr := s.recoverUnlaunched(ctx)
	s.settlePending(ctx)
	if !s.dispatchAllowed("") {
		return nil
	}
	runs, err := s.api.Queued(ctx)
	if err != nil {
		return err
	}
	failures := []error{recoveryErr}
	s.mu.Lock()
	accounts := append([]EnrolledAccount(nil), s.accounts...)
	adapters := map[string]Adapter{}
	for k, v := range s.adapters {
		adapters[k] = v
	}
	s.mu.Unlock()
	for _, account := range accounts {
		if s.hasUnresolvedOldClaim(account.ID) {
			s.freezeOnError(account.ID)
			continue
		}
		fenced, fenceErr := s.readFence(account.ID)
		if fenced || fenceErr != nil {
			continue
		}
		// Account probes can start vendor executables. A refused verification
		// must not reach one merely because health probing precedes dispatch.
		probeBlocked := false
		for _, run := range runs {
			if run.AgentPrincipalID == s.principalID && run.Purpose == VerificationPurpose && run.requestedAccount() == account.ID && validQueuedExecutionMode(run, adapters[account.Harness]) != nil {
				probeBlocked = true
				break
			}
		}
		if probeBlocked {
			continue
		}
		probe := adapters[account.Harness].(AccountProber)
		available := probe.Probe(ctx, account.Key)
		err := s.api.Probe(ctx, account.ID, s.daemonID, s.generation, available)
		if err == nil && available {
			if capture, ok := probe.(interface {
				CaptureCapacity(context.Context, string) []capacity.Reading
			}); ok {
				if api, ok := s.api.(capacityAPI); ok {
					if readings := capture.CaptureCapacity(ctx, account.Key); len(readings) > 0 {
						_ = api.ReportCapacity(ctx, account.ID, readings)
					}
				}
			}
		}
		s.mu.Lock()
		s.blockedAccounts[account.ID] = err != nil || !available
		s.probedAccounts[account.ID] = err == nil && available
		s.loginRequired[account.ID] = !available
		s.mu.Unlock()
		if err != nil {
			failures = append(failures, errors.New("account probe unavailable"))
		}
	}
	for _, run := range runs {
		if run.AgentPrincipalID != s.principalID {
			failures = append(failures, ErrScope)
			continue
		}
		if err := s.StartRun(ctx, run); err != nil && !errors.Is(err, ErrDraining) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Supervisor) StartRun(ctx context.Context, run Run) (resultErr error) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if !s.dispatchAllowed(run.requestedAccount()) {
		return ErrDraining
	}
	if run.ID == "" || run.WorkOrderID == "" || run.AgentPrincipalID != s.principalID || run.Status != "queued" {
		return ErrScope
	}
	s.mu.Lock()
	entry := s.runs[run.ID]
	s.mu.Unlock()
	if entry != nil {
		entry.mu.Lock()
		retry := entry.record.LaunchState == launchPrepared && entry.record.State == "claim_pending" && entry.record.Generation == s.generation
		entry.mu.Unlock()
		if !retry {
			return nil
		}
		if err := s.reconcileUnlaunched(ctx, entry); err != nil {
			return err
		}
		entry.mu.Lock()
		retry = entry.record.State == "claim_pending"
		entry.mu.Unlock()
		if !retry {
			return nil
		}
	}
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
	if err := validQueuedExecutionMode(run, adapter); err != nil {
		if errors.Is(err, ErrVerificationUnavailable) && entry == nil {
			if saveErr := s.refuseVerification(run); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	verification := run.Purpose == VerificationPurpose
	managedAdapter, managedOK := adapter.(ManagedControlAdapter)
	managedPolicy := !verification && profile.Harness == Claude && managedOK && managedAdapter.ManagedControlSupported()
	if verification {
		s.mu.Lock()
		entries := make([]*owned, 0, len(s.runs))
		for _, entry := range s.runs {
			entries = append(entries, entry)
		}
		s.mu.Unlock()
		for _, entry := range entries {
			entry.mu.Lock()
			busy := entry.record.RunID != run.ID && entry.record.ExecutionMode == VerificationPurpose && !noLocalProcess(entry.record) && (entry.record.State == "running" || entry.record.State == "starting" || entry.record.State == "waiting" || entry.record.State == "ownership_lost")
			entry.mu.Unlock()
			if busy {
				return ErrDraining
			}
		}
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
	if verification && time.Duration(*run.MaxDurationSeconds)*time.Second < duration {
		duration = time.Duration(*run.MaxDurationSeconds) * time.Second
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
	if !verification {
		if output, branchErr := exec.CommandContext(ctx, "git", "-C", s.workspace, "branch", "--show-current").Output(); branchErr == nil {
			branch = strings.TrimSpace(string(output))
		}
		prompt += "\n\nRun contract: You are bound to work order " + node.Key + " and run " + run.ID + ". Work only in this workspace. Current branch: " + branch + ". Use the Aeon tools to comment, check criteria, attach evidence, request approval, reply, and set status. Use aeon_terminal for tests and a local commit; never push without person approval. Report the commit ID and remaining blockers in your final reply."
	}
	if verification {
		prompt = VerificationTask
	}
	runWorkspace := s.workspace
	if len(prompt) > 256<<10 {
		return errors.New("work order prompt exceeds local bound")
	}
	if len(s.estimates) == 0 {
		return errors.New("allowance estimates required")
	}
	accountIDs := make([]string, 0, len(s.accounts))
	for _, account := range s.accounts {
		if account.Harness == profile.Harness && s.accountAvailable(account.ID) && (run.AccountID == "" || run.AccountID == account.ID) && (run.RequestedAccountID == "" || run.RequestedAccountID == account.ID) {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	if len(accountIDs) == 0 {
		return errors.New("no local account enrollment for harness")
	}
	var route Route
	if entry != nil {
		entry.mu.Lock()
		route = *entry.record.ClaimRoute
		entry.mu.Unlock()
	} else {
		route, err = s.api.Route(ctx, run.ID, s.daemonID, accountIDs, s.estimates)
		if err != nil {
			return err
		}
	}
	if route.DaemonID != s.daemonID || route.AccountKey == "" || len(route.Reservations) == 0 || run.AccountID != "" && route.AccountID != run.AccountID || run.RequestedAccountID != "" && route.AccountID != run.RequestedAccountID {
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
	if !s.accountAvailable(route.AccountID) {
		return ErrDraining
	}
	// Only the validated server route can fill the execution account. Preserve
	// all approval terms and require the same strict binding as adapter.Start.
	run.AccountID = route.AccountID
	if err := validExecutionMode(run, adapter); err != nil {
		return err
	}
	if entry == nil {
		rec := Record{LaunchState: launchPrepared, ClaimRoute: &route, AccountID: route.AccountID, ExecutionMode: run.Purpose, TenantID: s.tenantID, PrincipalID: s.principalID, RunID: run.ID, WorkOrderID: run.WorkOrderID, Generation: s.generation, Workspace: s.workspace, State: "claim_pending", Controls: map[string]replay{}}
		if err := s.journal.Put(rec); err != nil {
			return err
		}
		entry = &owned{record: rec, replies: map[string]string{}}
		s.mu.Lock()
		s.runs[run.ID] = entry
		s.mu.Unlock()
	}
	if err := s.api.Claim(ctx, run.ID, s.daemonID, s.generation, ids); err != nil {
		return errors.Join(err, s.reconcileUnlaunched(ctx, entry))
	}
	// Every error before launch intent must settle this claimed, never-launched
	// run. A failed report stays in the durable outbox with the same sequence.
	defer func() {
		entry.mu.Lock()
		neverLaunched := entry.record.LaunchState == launchPrepared
		entry.mu.Unlock()
		if resultErr != nil && neverLaunched {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, s.finishUnlaunched(cleanup, entry))
		}
	}()
	if verification {
		runWorkspace, err = s.prepareScratch(s.workspace)
		if err != nil {
			return err
		}
	}
	projectID, err := s.api.ProjectForNode(ctx, node.Key)
	if err != nil {
		return err
	}
	host, err := os.Hostname()
	if err != nil || host == "" || len(host) > 128 {
		return errors.New("valid harness host unavailable")
	}
	ref, err := s.newHarnessID()
	if err != nil {
		return err
	}
	leaseA, err := s.newHarnessID()
	if err != nil {
		return err
	}
	leaseB, err := s.newHarnessID()
	if err != nil {
		return err
	}
	entry.managedPolicy = managedPolicy
	if managedPolicy {
		entry.tokenBudget, entry.turnBudget = s.maxTokens, s.maxTurns
	}
	caps := []string{"status", "stop"}
	if managedPolicy {
		caps = append(caps, managedControlCapability, "steer", "rename", "model", "effort")
	}
	if profile.Harness != Grok {
		caps = append(caps, "interrupt")
	}
	entry.inboxCapable = !verification && !managedPolicy && (profile.Harness == Claude || profile.Harness == Codex || profile.Harness == Pi)
	if entry.inboxCapable {
		caps = append(caps, "inbox", "steer")
	}
	registration := HarnessSession{ID: s.generation + "/" + ref, ProjectID: projectID, Lease: leaseA + leaseB, AccountLabel: route.AccountLabel}
	if profile.Harness != Codex {
		registration.Model, registration.ReasoningEffort = profile.Model, profile.Effort
	}
	entry.harness, err = s.api.RegisterHarness(ctx, registration,
		s.principalID, run.ID, run.WorkOrderID, profile.Harness, host, caps)
	if err != nil {
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
	if toolAPI, ok := s.api.(RunToolAPI); ok && !verification {
		if signer, ok := s.api.(interface {
			runCredential(string, string, string, string) string
		}); ok {
			active := func() bool {
				entry.mu.Lock()
				defer entry.mu.Unlock()
				return entry.budgetStopReason() == "" && entry.record.Generation == s.generation && (entry.record.State == "starting" || entry.record.State == "running")
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
				closeHarness("process_failed")
				return err
			}
			runTools = &entry.tools.tools
		}
	}
	ephemeralRules := ""
	if managedPolicy {
		if runTools == nil {
			closeHarness("process_failed")
			return errors.New("bound tools required for managed controls")
		}
		ephemeralRules, err = s.managedRules(ctx, entry, run, profile)
		if err != nil {
			_ = entry.tools.Close()
			closeHarness("process_failed")
			return err
		}
	}
	observe := func(ev AdapterEvent) { s.observe(entry, ev) }
	// Commit the possible-fork boundary before handing control to an adapter.
	// On a journal failure Start is never invoked; after success a crash is
	// unconfirmed, even if no PID was subsequently persisted.
	entry.mu.Lock()
	intent := entry.record
	intent.LaunchState, intent.State = launchAttempted, "starting"
	err = s.journal.Put(intent)
	if err == nil {
		entry.record = intent
	}
	entry.mu.Unlock()
	if err != nil {
		_ = entry.tools.Close()
		closeHarness("process_failed")
		return err
	}
	launchedAt := time.Now()
	proc, err := adapter.Start(ctx, StartRequest{TenantID: s.tenantID, PrincipalID: s.principalID, Run: run, Profile: profile,
		AccountKey: route.AccountKey, Workspace: runWorkspace, StateRoot: filepath.Dir(s.journal.JournalPath()), Prompt: prompt, Generation: s.generation, Tools: runTools, Rules: ephemeralRules, MaxTurns: entry.turnBudget, MaxTokens: entry.tokenBudget}, observe)
	if err != nil {
		_ = entry.tools.Close()
		// A generic Start error does not prove that a child was never forked.
		entry.mu.Lock()
		entry.record.State = "ownership_lost"
		_ = s.journal.Put(entry.record)
		entry.mu.Unlock()
		s.freezeOnError(route.AccountID)
		return err
	}
	entry.mu.Lock()
	entry.process = proc
	entry.record.PID = proc.PID()
	entry.record.State = "running"
	saveErr := s.journal.Put(entry.record)
	entry.mu.Unlock()
	entry.mu.Lock()
	entry.monitorDone = make(chan struct{})
	done := entry.monitorDone
	entry.mu.Unlock()
	entry.budgetMu.Lock()
	entry.budgetProcess, entry.budgetTools = proc, entry.tools
	entry.budgetMu.Unlock()
	s.observeBudget(entry, AdapterEvent{})
	go s.runDeadline(entry, proc, done, time.Until(launchedAt.Add(duration)))
	var startedErr error
	if saveErr == nil {
		startedErr = s.update(ctx, entry, Telemetry{Kind: "started", Status: "running"})
	}
	var heartbeatErr error
	if saveErr == nil && startedErr == nil {
		heartbeatErr = s.heartbeatHarness(ctx, entry)
	}
	if errors.Is(heartbeatErr, ErrHarnessArchived) {
		entry.mu.Lock()
		entry.harnessArchived = true
		entry.mu.Unlock()
		heartbeatErr = nil
	}
	entry.mu.Lock()
	protocolFailed := entry.protocolFailed
	entry.mu.Unlock()
	startErr := errors.Join(saveErr, startedErr, heartbeatErr)
	if protocolFailed {
		// An adapter can report before Start returns its owned Process.
		startErr = errors.Join(startErr, ErrTelemetryProtocol)
	}
	s.handleRunError(entry, startErr)
	go s.monitor(entry)
	go s.heartbeat(entry)
	return startErr
}

func (s *Supervisor) heartbeat(entry *owned) {
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()
	entry.mu.Lock()
	done := entry.monitorDone
	entry.mu.Unlock()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.flushCapacity(ctx, entry)
			err := s.update(ctx, entry, Telemetry{Kind: "heartbeat"})
			if err == nil {
				err = s.serviceHarness(ctx, entry)
			}
			cancel()
			if errors.Is(err, ErrControlUnconfirmed) {
				continue
			}
			if err != nil {
				s.handleRunError(entry, err)
			}

		}
	}
}

// The approved process deadline is independent of API/entry locks. A revoked
// key or slow telemetry must not extend the approved verification duration.
func (s *Supervisor) runDeadline(entry *owned, proc Process, done <-chan struct{}, remaining time.Duration) {
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}
	entry.deadlineExpired.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = proc.Stop(ctx)
}

func (s *Supervisor) observe(entry *owned, ev AdapterEvent) {
	s.observeBudget(entry, ev)
	if len(ev.Capacity) > 0 {
		s.observeCapacity(entry, ev.Capacity)
	}
	if ev.SessionUsage != nil {
		entry.usage.submit(*ev.SessionUsage)
	}
	if ev.HarnessModel != "" || ev.HarnessEffort != "" {
		entry.mu.Lock()
		if entry.record.Generation == s.generation && !entry.harnessArchived &&
			(entry.record.State == "starting" || entry.record.State == "running") {
			change := harnessMetadata{}
			if validHarnessValue(ev.HarnessModel, 128) && ev.HarnessModel != entry.harness.Model {
				change.model, entry.harness.Model = ev.HarnessModel, ev.HarnessModel
			}
			if validHarnessValue(ev.HarnessEffort, 40) && ev.HarnessEffort != entry.harness.ReasoningEffort {
				change.effort, entry.harness.ReasoningEffort = ev.HarnessEffort, ev.HarnessEffort
			}
			if change.model != "" || change.effort != "" {
				entry.metadataSeq++
				change.seq = entry.metadataSeq
				entry.metadataPending = append(entry.metadataPending, change)
				if len(entry.metadataPending) > 20 {
					// Preserve the in-flight head for an identical retry.
					// Carry the evicted snapshot fields into the next retained
					// change so a later model-only update cannot lose known effort.
					dropped, next := entry.metadataPending[1], &entry.metadataPending[2]
					if next.model == "" {
						next.model = dropped.model
					}
					if next.effort == "" {
						next.effort = dropped.effort
					}
					entry.metadataPending = append(entry.metadataPending[:1], entry.metadataPending[2:]...)
				}
			}
		}
		entry.mu.Unlock()
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
		s.handleRunError(entry, err)
	}
}

func validHarnessValue(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value &&
		!strings.ContainsFunc(value, unicode.IsControl)
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
	entry.mu.Lock()
	entry.record.BudgetStopReason = entry.budgetStopReason()
	entry.record.ExitObserved = err == nil
	if observer, ok := proc.(interface{ ProcessExited() bool }); ok {
		entry.record.ExitObserved = observer.ProcessExited()
	}
	if saveErr := s.journal.Put(entry.record); saveErr != nil {
		entry.record.SettlementGap = true
	}
	if !entry.record.ExitObserved {
		entry.record.State = "ownership_lost"
		_ = s.journal.Put(entry.record)
		entry.mu.Unlock()
		return
	}
	entry.mu.Unlock()
	s.finishSessionUsage(entry)
	entry.harnessMu.Lock()
	defer entry.harnessMu.Unlock()
	entry.mu.Lock()
	pendingMetadata := len(entry.metadataPending) > 0 && entry.record.Generation == s.generation && !entry.harnessArchived
	entry.mu.Unlock()
	if pendingMetadata {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		flushErr := s.heartbeatHarnessPhase(ctx, entry, "stopping")
		cancel()
		if errors.Is(flushErr, ErrHarnessArchived) {
			entry.mu.Lock()
			entry.harnessArchived = true
			entry.mu.Unlock()
		} else if flushErr != nil {
			slog.Warn("managed session metadata settlement incomplete")
		}
	}
	entry.mu.Lock()
	stopped := entry.stopRequested || entry.deadlineExpired.Load() || entry.budgetStopReason() != ""
	forced := entry.forceRequested
	protocolFailed := entry.protocolFailed
	budgetReason := entry.budgetStopReason()
	entry.mu.Unlock()
	if err == nil && !protocolFailed {
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
	if protocolFailed {
		status, code = "failed", "app_server_protocol"
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
	if budgetReason != "" {
		reason = budgetReason
	}
	s.stopHarness(ctx, entry, reason)
}

func (s *Supervisor) finishSessionUsage(entry *owned) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.flushCapacity(ctx, entry)
	cancel()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
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
			RunID: entry.record.RunID, Generation: s.generation, CorrelationID: control.ID, Operation: control.Kind, Text: control.Text, Value: control.Value, ExpectedOwnership: control.ExpectedOwnership, ExpiresAt: control.ExpiresAt}, true)
		if entry.managedPolicy && control.Kind != "force_stop" {
			switch {
			case errors.Is(err, ErrControlExpired):
				outcome, reason, err = "rejected", "authorization_expired", nil
			case errors.Is(err, ErrUnsupported), errors.Is(err, ErrNotOwned), errors.Is(err, ErrGeneration):
				outcome, reason, err = "rejected", "child_unavailable", nil
			case errors.Is(err, ErrBudgetExhausted):
				outcome, reason, err = "rejected", "budget_exhausted", nil
			case errors.Is(err, ErrSettingRejected):
				outcome, reason, err = "rejected", "setting_rejected", nil
			case err != nil:
				outcome, reason, err = "rejected", "outcome_unconfirmed", nil
			case control.Kind == "effort":
				reason = "setting_applied_next_turn"
			case control.Kind == "model" || control.Kind == "rename":
				reason = "setting_applied"
			case control.Kind == "steer":
				reason = "queued_next_turn"
			case control.Kind == "interrupt":
				reason = "native_interrupt_acknowledged"
			case control.Kind == "stop":
				reason = "native_close_exited"
			}
		}
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
		// Publish the acknowledged model/effort before releasing the pending
		// setting: subsequent account validation must see this applied pair.
		if isSetting(control.Kind) && outcome == "applied" {
			if err := s.heartbeatHarnessPhase(ctx, entry, "working"); err != nil {
				return ErrControlUnconfirmed
			}
		}
		if err := s.api.CompleteHarnessControl(ctx, entry.harness, control.ID, outcome, reason); err != nil {
			if !errors.Is(err, ErrHarnessArchived) && (entry.managedPolicy || control.Kind == "force_stop" || control.Kind == "stop") {
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
	if entry.record.Generation != s.generation && entry.record.LaunchState != launchPrepared {
		return ErrGeneration
	}
	if t.Kind == "heartbeat" && (entry.record.State == "completed" || entry.record.State == "failed" || entry.record.State == "cancelled" || entry.record.State == "ownership_lost") {
		return nil
	}
	if len(entry.record.Pending) >= 512 {
		entry.record.SettlementGap = true
		_ = s.journal.Put(entry.record)
		return errors.New("telemetry settlement backlog full")
	}
	t.Sequence = entry.record.Sequence + 1
	entry.record.Sequence = t.Sequence
	if t.Status != "" {
		entry.record.State = t.Status
	}
	entry.record.Pending = append(entry.record.Pending, t)
	if err := s.journal.Put(entry.record); err != nil {
		return err
	}
	return s.flushReports(ctx, entry)
}

func (s *Supervisor) Control(ctx context.Context, req ControlRequest) (Receipt, error) {
	return s.control(ctx, req, false)
}

// Force authorization originates only in the server's human-confirmed recovery
// queue. Local transport credentials and inbox messages cannot mint that grant.
func (s *Supervisor) control(ctx context.Context, req ControlRequest, fromRecoveryQueue bool) (Receipt, error) {
	if (req.Operation == "force_stop" || isSetting(req.Operation)) && !fromRecoveryQueue {
		return Receipt{}, ErrUnsupported
	}
	if req.TenantID != s.tenantID || req.PrincipalID != s.principalID || req.Generation != s.generation ||
		req.RunID == "" || req.CorrelationID == "" || len(req.CorrelationID) > 128 {
		return Receipt{}, ErrScope
	}
	if req.Operation != "steer" && req.Operation != "interrupt" && req.Operation != "resume" && req.Operation != "stop" && req.Operation != "force_stop" && !isSetting(req.Operation) {
		return Receipt{}, ErrUnsupported
	}
	if (isSetting(req.Operation) && !validSettingValue(req.Operation, req.Value)) || (!isSetting(req.Operation) && req.Value != "") {
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
	if entry.record.ExecutionMode == VerificationPurpose && (req.Operation == "steer" || req.Operation == "resume") {
		return Receipt{}, ErrUnsupported
	}
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if (entry.managedPolicy && !fromRecoveryQueue) || (isSetting(req.Operation) && !entry.managedPolicy) {
		return Receipt{}, ErrUnsupported
	}

	payload, _ := json.Marshal(struct {
		Ownership *ownedprocess.Identity
		ExpiresAt *time.Time
	}{req.ExpectedOwnership, req.ExpiresAt})
	body := req.Text
	if isSetting(req.Operation) {
		body = req.Value
	}
	digest := sha256.Sum256([]byte(req.Operation + "\x00" + body + "\x00" + string(payload)))
	key := hex.EncodeToString(digest[:])
	if prior, ok := entry.record.Controls[req.CorrelationID]; ok {
		if prior.Digest != key {
			return Receipt{}, ErrReplay
		}
		if prior.Rejected {
			if prior.SettingRejected {
				return Receipt{}, ErrSettingRejected
			}
			return Receipt{}, ErrControlUnconfirmed
		}
		return prior.Receipt, nil
	}
	if entry.record.Generation != s.generation || entry.process == nil || entry.record.State != "running" || entry.harnessArchived {
		return Receipt{}, ErrNotOwned
	}
	if len(entry.record.Controls) >= 256 {
		return Receipt{}, errors.New("control replay capacity reached")
	}
	if req.Operation != "force_stop" && fromRecoveryQueue && (entry.managedPolicy || req.ExpectedOwnership != nil) {
		if !entry.managedPolicy {
			return Receipt{}, ErrUnsupported
		}
		if entry.budgetStopReason() != "" {
			return Receipt{}, ErrBudgetExhausted
		}
		if req.ExpectedOwnership == nil || req.ExpiresAt == nil {
			return Receipt{}, ErrUnsupported
		}
		if !req.ExpiresAt.After(time.Now()) {
			return Receipt{}, ErrControlExpired
		}
		recovery, ok := entry.process.(RecoveryProcess)
		if !ok {
			return Receipt{}, ErrNotOwned
		}
		expected := req.ExpectedOwnership
		if expected.DaemonID != s.daemonID || expected.Generation != s.generation {
			return Receipt{}, ErrGeneration
		}
		current, e := recovery.Ownership()
		if e != nil || current.ProcessID != expected.ProcessID || current.RootPID != expected.RootPID || current.GroupID != expected.GroupID || !current.StartedAt.Equal(expected.StartedAt) {
			return Receipt{}, ErrNotOwned
		}
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
		entry.mu.Unlock()
		err = recovery.ForceStop(ctx, *expected, *req.ExpiresAt)
		entry.mu.Lock()
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
		entry.mu.Unlock()
		err = graceful.GracefulStop(ctx)
		entry.mu.Lock()
		if err != nil && !errors.Is(err, ErrGracefulTimeout) {
			entry.stopRequested = false
		}
	} else if req.Operation == "rename" {
		// Aeon's public label is server metadata, not a persisted vendor
		// transcript title. The exact owned process has been verified above.
	} else if isSetting(req.Operation) {
		proc := entry.process
		entry.mu.Unlock()
		settingCtx, cancel := context.WithDeadline(ctx, *req.ExpiresAt)
		err = proc.Control(settingCtx, req.Operation, req.Value)
		cancel()
		entry.mu.Lock()
	} else {
		proc := entry.process
		entry.mu.Unlock()
		err = proc.Control(ctx, req.Operation, req.Text)
		entry.mu.Lock()
	}
	if err != nil {
		if entry.managedPolicy && fromRecoveryQueue {
			entry.record.Controls[req.CorrelationID] = replay{Digest: key, Rejected: true, SettingRejected: errors.Is(err, ErrSettingRejected)}
			_ = s.journal.Put(entry.record)
		}
		return Receipt{}, err
	}
	receipt := Receipt{RunID: req.RunID, Generation: s.generation, CorrelationID: req.CorrelationID, Operation: req.Operation, AppliedAt: time.Now().UTC()}
	entry.record.Controls[req.CorrelationID] = replay{Digest: key, Receipt: receipt}
	if err := s.journal.Put(entry.record); err != nil {
		return Receipt{}, fmt.Errorf("persist control receipt: %w", err)
	}
	return receipt, nil
}

// Close fences future dispatch but never signals a child. The caller must
// keep the supervisor/service alive and retry after observed process exit.
func (s *Supervisor) Close(ctx context.Context) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	status := s.Lifecycle("")
	if len(status.ActiveRunIDs) > 0 {
		return ErrDraining
	}
	if len(status.UnconfirmedRunIDs) > 0 {
		return ErrProcessesUnconfirmed
	}
	s.mu.Lock()
	entries := make([]*owned, 0, len(s.runs))
	for _, e := range s.runs {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		done := e.monitorDone
		e.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if s.lock != nil {
		if err := s.lock.Close(); err != nil {
			return err
		}
		s.lock = nil
	}
	return s.state.Close()
}

// heartbeatHarness publishes only the process the current daemon actually owns.
// A restarted daemon has no in-memory Process and cannot re-adopt a journal PID.
func (s *Supervisor) heartbeatHarness(ctx context.Context, entry *owned) error {
	return s.heartbeatHarnessPhase(ctx, entry, "working")
}

func (s *Supervisor) heartbeatHarnessPhase(ctx context.Context, entry *owned, phase string) error {
	for {
		entry.mu.Lock()
		if entry.harnessArchived {
			entry.mu.Unlock()
			return ErrHarnessArchived
		}
		if entry.record.Generation != s.generation || entry.record.State != "running" {
			entry.mu.Unlock()
			return ErrGeneration
		}
		if phase == "stopping" && len(entry.metadataPending) == 0 {
			entry.mu.Unlock()
			return nil
		}
		session := entry.harness
		if phase == "stopping" {
			session.Ownership = nil
		}
		session.ActivitySequence = entry.heartbeatSeq + 1
		// Metadata is sent only from verified adapter snapshots. The ordinary
		// heartbeat must not replay a stale launch value after a live change.
		session.Model, session.ReasoningEffort = "", ""
		var change harnessMetadata
		if len(entry.metadataPending) > 0 {
			change = entry.metadataPending[0]
			session.Model, session.ReasoningEffort = change.model, change.effort
		}
		if phase == "working" {
			if recovery, ok := entry.process.(RecoveryProcess); ok {
				if identity, err := recovery.Ownership(); err == nil {
					identity.DaemonID, identity.Generation = s.daemonID, s.generation
					session.Ownership = &identity
				}
			}
		}
		entry.mu.Unlock()
		if err := s.api.HeartbeatHarness(ctx, session, phase); err != nil {
			return err
		}
		entry.mu.Lock()
		entry.heartbeatSeq = session.ActivitySequence
		for len(entry.metadataPending) > 0 && entry.metadataPending[0].seq <= change.seq {
			entry.metadataPending = entry.metadataPending[1:]
		}
		entry.mu.Unlock()
		if change.seq == 0 {
			return nil
		}
	}
}
