// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// LeadRuntimeAdapter is separately qualified for the coordinator's native
// tools/provider boundary. Ordinary Adapter.Start never implies this authority.
// StartLead must enforce the immutable actor grant and consume only run-bound
// tools. It receives no daemon key. Local maps, never prompt paths, select cwd.
type LeadRuntimeAdapter interface {
	RoutineExecutionAdapter
	LeadExitConfirmed(Process) bool
	StartLead(context.Context, LeadStartRequest) (Process, error)
	// ServiceLead consumes only the bound session IO supplied at StartLead.
	// Qualification must prove durable control/input replay, cooperative pause
	// and exact-ownership/deadline checks before any native signal.
	ServiceLead(context.Context, Process, *HarnessPause) error
}

// LeadSessionIO binds lifecycle operations to this session; the native adapter
// receives neither the daemon API key nor a way to select another target.
type LeadSessionIO struct {
	Yield            func(context.Context) ([]HarnessControl, error)
	CompleteControl  func(context.Context, string, string, string) error
	Drain            func(context.Context) ([]HarnessDelivery, error)
	CompleteDelivery func(context.Context, HarnessDelivery) error
}

type LeadStartRequest struct {
	Launch              agentsetup.LeadLaunch
	Session             HarnessSession
	AccountKey          string
	Workspace           string
	ResourceEnvironment []string
	Lifetime            context.Context
	SessionIO           LeadSessionIO
}

// Prepare uses S11's worktree isolation and AEON-1089's resource allocator.
// It must retain disputed allocations and never borrow another session/tree.
// Missing qualification/mapping/allocator refuses before any native process.
type LeadRuntimeConfig struct {
	Repositories []RoutineRepository
	Prepare      func(context.Context, agentsetup.LeadDemand, RoutineRepository) (RoutineWorkspace, RoutineResourceLease, error)
}

type leadRuntimeAPI interface {
	LeadDemand(context.Context, string) (*agentsetup.LeadDemand, error)
	ClaimLead(context.Context, agentsetup.LeadPickup) (agentsetup.LeadLaunch, error)
	ReportLead(context.Context, agentsetup.LeadPickup, string, *ownedprocess.Identity) (agentsetup.LeadLaunch, error)
}

type leadRecord struct {
	Version      int                    `json:"version"`
	Pickup       agentsetup.LeadPickup  `json:"pickup"`
	Launch       *agentsetup.LeadLaunch `json:"launch,omitempty"`
	Workspace    RoutineWorkspace       `json:"workspace"`
	Group        string                 `json:"group"`
	Attempted    bool                   `json:"attempted"`
	Ownership    *ownedprocess.Identity `json:"ownership,omitempty"`
	ExitObserved bool                   `json:"exit_observed"`
	ExitReported bool                   `json:"exit_reported"`
}

type localLead struct {
	record    leadRecord
	process   Process
	resources RoutineResourceLease
	cancel    context.CancelFunc
	done      chan struct{}
	adapter   LeadRuntimeAdapter
	sequence  int64
}

type leadRuntime struct {
	mu     sync.Mutex
	config *LeadRuntimeConfig
	leads  map[string]*localLead
}

func (s *Supervisor) writeLead(record leadRecord, exclusive bool) error {
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 32768 {
		return ErrRoutineUnavailable
	}
	return s.state.Write("lead-runtime-"+record.Pickup.Demand.ID+".json", raw, exclusive)
}

// PollLeadRuntime is called after workers, retaining their pickup priority.
// Nil local configuration makes no API calls and never changes legacy work.
func (s *Supervisor) PollLeadRuntime(ctx context.Context) error {
	return s.pollLeadRuntime(ctx, true)
}

// ReconcileLeadRuntime services retained owners without discovering new demand.
// Worker pickup failures and shutdown never become fresh lead launch authority.
func (s *Supervisor) ReconcileLeadRuntime(ctx context.Context) error {
	return s.pollLeadRuntime(ctx, false)
}

func (s *Supervisor) pollLeadRuntime(ctx context.Context, discover bool) error {
	if s.leads == nil || s.leads.config == nil {
		return nil
	}
	if s.ledgerBarrier != nil {
		s.ledgerBarrier("lead_pickup_requested")
	}
	if err := s.dispatchMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.dispatchMu.Unlock()
	api, ok := s.api.(leadRuntimeAPI)
	if !ok || s.ledger == nil || s.leads.config.Prepare == nil {
		return ErrRoutineUnavailable
	}
	s.leads.mu.Lock()
	defer s.leads.mu.Unlock()
	if err := s.reconcileLeadReceiptsLocked(ctx, api, true); err != nil {
		return err
	}
	if !discover {
		return nil
	}
	if !s.dispatchAllowed("") {
		return ErrDraining
	}
	if len(s.leads.config.Repositories) > 32 {
		return ErrRoutineUnavailable
	}
	for _, repository := range s.leads.config.Repositories {
		demand, e := api.LeadDemand(ctx, repository.ProjectID)
		if e != nil {
			return e
		}
		if demand == nil {
			continue
		}
		if demand.ProjectID != repository.ProjectID || demand.OwnerID != repository.OwnerID || repository.TenantID != s.tenantID {
			return ErrScope
		}
		if e = s.startLead(ctx, api, *demand, repository); e != nil {
			return e
		}
	}
	return nil
}

// Caller holds dispatchMu followed by leads.mu. service=false never delivers
// a control during Close; it only reconciles independently observed exits.
func (s *Supervisor) reconcileLeadReceiptsLocked(ctx context.Context, api leadRuntimeAPI, service bool) error {
	// Reconcile retained receipts before discovering fresh work. The local files
	// are exact-generation authority; a daemon restart never resumes a process.
	names, err := s.state.Names(4096)
	if err != nil {
		return err
	}
	count := 0
	for _, name := range names {
		if len(name) < len("lead-runtime-.json") || name[:13] != "lead-runtime-" {
			continue
		}
		raw, e := s.state.Read(name, 32768)
		if e != nil {
			return e
		}
		var r leadRecord
		if json.Unmarshal(raw, &r) != nil || r.Version != 1 || !uuidPattern.MatchString(r.Pickup.Demand.ID) || name != "lead-runtime-"+r.Pickup.Demand.ID+".json" || r.Pickup.DaemonID != s.daemonID {
			return ErrProcessesUnconfirmed
		}
		if r.ExitReported {
			continue
		}
		count++
		if count > 128 {
			return ErrProcessesUnconfirmed
		}
		if r.Pickup.DaemonGeneration != s.generation || r.Pickup.LedgerGeneration != s.ledgerBinding.Generation {
			return ErrProcessesUnconfirmed
		}
		local := s.leads.leads[r.Pickup.Demand.ID]
		if r.Attempted && !r.ExitObserved && local == nil {
			return ErrProcessesUnconfirmed
		}
		if r.ExitObserved {
			if r.Ownership == nil {
				return ErrProcessesUnconfirmed
			}
			// A lost started reply is repaired before reporting exit. Identity replay
			// is exact; a server conflict retains all local owners and evidence.
			if _, e = api.ReportLead(ctx, r.Pickup, "started", r.Ownership); e != nil {
				return e
			}
			if _, e = api.ReportLead(ctx, r.Pickup, "exit_observed", r.Ownership); e != nil {
				return e
			}
			// Root Wait alone is insufficient for its fixtures/resource lease.
			observer, proven := localProcess(local).(RoutineFixtureExitObserver)
			if !proven || !observer.RoutineFixturesExited() {
				return ErrProcessesUnconfirmed
			}
			if local.resources != nil {
				if e = local.resources.Finish(true); e != nil {
					return e
				}
				local.resources = nil
			}
			if e = s.ledger.Release(s.ledgerBinding.Member.ID, r.Pickup.LedgerGeneration, r.Group); e != nil {
				return e
			}
			r.ExitReported = true
			if e = s.writeLead(r, false); e != nil {
				return e
			}
			delete(s.leads.leads, r.Pickup.Demand.ID)
		} else if r.Attempted {
			if !service {
				return ErrDraining
			}
			if r.Ownership == nil {
				return ErrProcessesUnconfirmed
			}
			if _, e = api.ReportLead(ctx, r.Pickup, "started", r.Ownership); e != nil {
				return e
			}
			if local == nil || local.process == nil || local.adapter == nil || r.Launch == nil {
				return ErrProcessesUnconfirmed
			}
			session := leadSession(r)
			local.sequence++
			session.ActivitySequence = local.sequence
			var pause *HarnessPause
			if heartbeat, ok := s.api.(pauseHeartbeatAPI); ok {
				pause, e = heartbeat.HeartbeatHarnessPause(ctx, session, "working")
			} else {
				e = s.api.HeartbeatHarness(ctx, session, "working")
			}
			if e != nil {
				return e
			}
			if e = local.adapter.ServiceLead(ctx, local.process, pause); e != nil {
				return e
			}
		}
	}
	return nil
}

func localProcess(local *localLead) Process {
	if local == nil {
		return nil
	}
	return local.process
}

func leadSession(record leadRecord) HarnessSession {
	l := record.Launch
	return HarnessSession{ID: l.SessionID, ProjectID: l.Demand.ProjectID, Lease: record.Pickup.Lease, Harness: l.Harness, Model: l.Model, ReasoningEffort: l.Effort, Ownership: record.Ownership}
}

func (s *Supervisor) leadSessionIO(session HarnessSession) LeadSessionIO {
	return LeadSessionIO{
		Yield: func(ctx context.Context) ([]HarnessControl, error) {
			controls, err := s.api.YieldHarness(ctx, session)
			if len(controls) > 32 {
				return nil, ErrRoutineUnavailable
			}
			return controls, err
		},
		CompleteControl: func(ctx context.Context, id, outcome, reason string) error {
			if !uuidPattern.MatchString(id) || len(outcome) > 32 || len(reason) > 128 {
				return ErrScope
			}
			return s.api.CompleteHarnessControl(ctx, session, id, outcome, reason)
		},
		Drain: func(ctx context.Context) ([]HarnessDelivery, error) {
			deliveries, err := s.api.DrainHarness(ctx, session)
			if len(deliveries) > 1 {
				return nil, ErrRoutineUnavailable
			}
			return deliveries, err
		},
		CompleteDelivery: func(ctx context.Context, delivery HarnessDelivery) error {
			if !uuidPattern.MatchString(delivery.ID) || len(delivery.Outcome) > 32 || len(delivery.FailureReason) > 128 {
				return ErrScope
			}
			return s.api.CompleteHarnessDelivery(ctx, session, delivery)
		},
	}
}

func (s *Supervisor) startLead(ctx context.Context, api leadRuntimeAPI, demand agentsetup.LeadDemand, repository RoutineRepository) error {
	if !uuidPattern.MatchString(demand.ID) || !uuidPattern.MatchString(demand.ProjectID) || !uuidPattern.MatchString(demand.OwnerID) || !uuidPattern.MatchString(demand.RoutineRunID) || demand.Generation < 0 || demand.Revision < 1 || !s.dispatchAllowed(repository.AccountID) || !s.accountAvailable(repository.AccountID) {
		return ErrRoutineUnavailable
	}
	// The server resolves the model/role. This local account only constrains the
	// authorized map; it cannot substitute a model or borrow a login.
	var account EnrolledAccount
	var adapter Adapter
	for _, a := range s.accounts {
		if a.ID == repository.AccountID {
			account = a
			adapter = s.adapters[a.Harness]
			break
		}
	}
	native, ok := adapter.(LeadRuntimeAdapter)
	if !ok {
		return ErrRoutineUnavailable
	}
	if err := validateRoutineRepository(repository); err != nil {
		return err
	}
	name := "lead-runtime-" + demand.ID + ".json"
	var record leadRecord
	var resources RoutineResourceLease
	raw, err := s.state.Read(name, 32768)
	if err == nil {
		if json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.Pickup.Demand != demand || record.Pickup.DaemonGeneration != s.generation || record.Pickup.LedgerGeneration != s.ledgerBinding.Generation || record.Attempted {
			return ErrProcessesUnconfirmed
		}
		local := s.leads.leads[demand.ID]
		if local == nil || local.resources == nil {
			return ErrProcessesUnconfirmed
		}
		resources = local.resources
	} else if errors.Is(err, os.ErrNotExist) {
		workspace, lease, e := s.leads.config.Prepare(ctx, demand, repository)
		if e != nil {
			return e
		}
		// Preparation may allocate, but cannot execute. The same approved local
		// mapping/physical path and opaque worktree are checked before claim.
		if lease == nil || !agentsetup.RoutineDigest(workspace.ID) || workspace.Path != filepath.Join(repository.Root, workspace.ID) {
			return ErrRoutineUnavailable
		}
		physical, e := filepath.EvalSymlinks(workspace.Path)
		if e != nil || physical != workspace.Path {
			return ErrRoutineUnavailable
		}
		ref, e := randomID()
		if e != nil {
			return e
		}
		secret, e := randomID()
		if e != nil {
			return e
		}
		group, e := agentsetup.LedgerID()
		if e != nil {
			return e
		}
		candidates, e := s.ledger.Acquire(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, group, s.ledgerCandidates([]string{repository.AccountID}, s.ledgerBinding.Generation))
		if e != nil {
			return e
		}
		if len(candidates) != 1 || candidates[0].AccountID != repository.AccountID {
			return ErrScope
		}
		record = leadRecord{Version: 1, Pickup: agentsetup.LeadPickup{Demand: demand, DaemonID: s.daemonID, DaemonGeneration: s.generation, LedgerGeneration: s.ledgerBinding.Generation, AccountID: repository.AccountID, ComputerID: repository.ComputerID, DaemonDigest: repository.DaemonDigest, CapabilityDigest: repository.CapabilityDigest, Capability: agentsetup.LeadExecutionCapability, WorktreeID: workspace.ID, Ref: ref, Lease: secret}, Workspace: workspace, Group: group}
		if e = s.writeLead(record, true); e != nil {
			return e
		}
		resources = lease
		s.leads.leads[demand.ID] = &localLead{record: record, resources: resources}
	} else {
		return err
	}
	launch, err := api.ClaimLead(ctx, record.Pickup)
	if err != nil {
		return err
	}
	if err = s.validateLocalLead(launch, record, repository, account, native); err != nil {
		return err
	}
	if record.Launch != nil && *record.Launch != launch { // Grant has pointers; compare canonical bytes below instead.
		before, _ := json.Marshal(record.Launch)
		after, _ := json.Marshal(launch)
		if string(before) != string(after) {
			return ErrReplay
		}
	}
	record.Launch = &launch
	if err = s.writeLead(record, false); err != nil {
		return err
	}
	if err = s.ledger.Claimed(s.ledgerBinding.Member.ID, record.Pickup.LedgerGeneration, record.Group, s.ledgerLogin(account.ID, record.Pickup.LedgerGeneration)); err != nil {
		return err
	}
	checked, err := api.ReportLead(ctx, record.Pickup, "preflight", nil)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(checked)
	b, _ := json.Marshal(launch)
	if string(a) != string(b) {
		return ErrReplay
	}
	if !s.capacityNow().Before(launch.Grant.ExpiresAt) || !s.dispatchAllowed(account.ID) || !s.accountAvailable(account.ID) {
		return ErrRoutineUnavailable
	}
	environment, err := validateRoutineEnvironment(resources.Environment(), launch.Grant)
	if err != nil {
		return err
	}
	// Persist the irreversible boundary BEFORE StartLead. Any uncertain error,
	// crash or lost reply freezes this exact attempt instead of calling again.
	record.Attempted = true
	if err = s.writeLead(record, false); err != nil {
		return err
	}
	if err = s.ledger.Launching(s.ledgerBinding.Member.ID, record.Pickup.LedgerGeneration, record.Group); err != nil {
		return err
	}
	// Disk/ledger contention must not carry an expiring grant past a window.
	// Its durable attempted boundary remains conservative if this refuses.
	if !s.capacityNow().Before(launch.Grant.ExpiresAt) || ctx.Err() != nil || !s.dispatchAllowed(account.ID) {
		return ErrProcessesUnconfirmed
	}
	lifetime, cancel := context.WithTimeout(s.lifetime, time.Duration(launch.MaxDurationSeconds)*time.Second)
	local := s.leads.leads[demand.ID]
	local.record = record
	local.cancel = cancel
	local.adapter = native
	resources.HandoffPorts()
	session := leadSession(record)
	process, err := native.StartLead(ctx, LeadStartRequest{Launch: launch, Session: session, AccountKey: account.Key, Workspace: record.Workspace.Path, ResourceEnvironment: environment, Lifetime: lifetime, SessionIO: s.leadSessionIO(session)})
	if err != nil || process == nil {
		cancel()
		return ErrProcessesUnconfirmed
	}
	local.process = process
	local.done = make(chan struct{})
	ownership, ok := process.(RecoveryProcess)
	if !ok {
		return ErrProcessesUnconfirmed
	}
	identity, err := ownership.Ownership()
	if err != nil || identity.DaemonID != s.daemonID || identity.Generation != s.generation || identity.RootPID != process.PID() || identity.GroupID != identity.RootPID || identity.StartedAt.IsZero() || identity.ProcessID == "" {
		return ErrProcessesUnconfirmed
	}
	record.Ownership = &identity
	local.record = record
	if err = s.writeLead(record, false); err != nil {
		return err
	}
	// Wait observes the actual native process, including when a chat turn ends.
	go func() {
		defer close(local.done)
		_ = process.Wait()
		cancel()
		s.leads.mu.Lock()
		defer s.leads.mu.Unlock()
		if !native.LeadExitConfirmed(process) {
			return
		}
		local.record.ExitObserved = true
		_ = s.writeLead(local.record, false) // failure retains attempted/unconfirmed state
	}()
	if err = resources.Running(process.PID()); err != nil {
		return err
	}
	if err = s.ledger.Running(s.ledgerBinding.Member.ID, record.Pickup.LedgerGeneration, record.Group, identity.RootPID, identity.StartedAt); err != nil {
		return err
	}
	_, err = api.ReportLead(ctx, record.Pickup, "started", &identity)
	return err
}

func (s *Supervisor) validateLocalLead(launch agentsetup.LeadLaunch, record leadRecord, repository RoutineRepository, account EnrolledAccount, native LeadRuntimeAdapter) error {
	g := launch.Grant
	p := record.Pickup
	if g.Validate() != nil || launch.Demand != p.Demand || g.LaunchID != p.Demand.ID || launch.Generation != p.Demand.Generation+1 || !uuidPattern.MatchString(launch.SessionID) || launch.Harness != account.Harness || launch.ProfileID == "" || launch.Model == "" || launch.MaxDurationSeconds < 1 || launch.MaxDurationSeconds > 86400 ||
		g.TenantID != s.tenantID || g.PrincipalID != s.principalID || g.OwnerID != repository.OwnerID || g.ProjectID != repository.ProjectID || g.RoutineRunID != p.Demand.RoutineRunID || g.AccountID != account.ID || g.ComputerID != repository.ComputerID || g.DaemonID != s.daemonID || g.DaemonGeneration != s.generation || g.LedgerGeneration != s.ledgerBinding.Generation || g.DaemonDigest != repository.DaemonDigest || g.CapabilityDigest != repository.CapabilityDigest || g.HostMappingDigest != repository.HostMappingDigest || !s.capacityNow().Before(g.ExpiresAt) || g.ExpiresAt.Sub(s.capacityNow()) > 2*time.Minute || !native.RoutineExecutionSupported(g) {
		return ErrRoutineUnavailable
	}
	return nil
}

// Worktree identity derives only from an owner-approved map and demand id.
func LeadWorktreeID(demand agentsetup.LeadDemand, repository RoutineRepository) string {
	raw, _ := json.Marshal([]string{repository.TenantID, repository.OwnerID, repository.ProjectID, repository.AccountID, repository.ComputerID, demand.ID})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// An old/default-off daemon also retains unknown native lead ownership. It
// must not open an ordinary worker path around a prior generation's journal.
func checkRetainedLeads(store *agentsetup.Store) error {
	names, err := store.Names(4096)
	if err != nil {
		return err
	}
	for _, name := range names {
		if len(name) < len("lead-runtime-.json") || name[:13] != "lead-runtime-" {
			continue
		}
		raw, err := store.Read(name, 32768)
		if err != nil {
			return ErrProcessesUnconfirmed
		}
		var r leadRecord
		if json.Unmarshal(raw, &r) != nil || r.Version != 1 || !r.ExitReported || !r.ExitObserved || r.Ownership == nil {
			return ErrProcessesUnconfirmed
		}
	}
	return nil
}
