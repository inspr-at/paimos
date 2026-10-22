// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

// runAdmission contains the local authority and execution policy selected before
// work-order reads or workspace allocation. A nil admission means an existing
// run was already handled; it must not continue to routing or launch.
type runAdmission struct {
	run                                 Run
	grant                               *agentsetup.RoutineGrant
	entry                               *owned
	profile                             Profile
	adapter                             Adapter
	accountIDs                          []string
	routineBudget                       *RoutineCallBudget
	verification, review, managedPolicy bool
	approvedRepository                  RoutineRepository
}

// preparedRun carries the work-order and workspace bindings into route/claim.
// Resource ownership remains with the supervisor, including on preparation errors.
type preparedRun struct {
	node                Node
	order               WorkOrder
	workspace           string
	pickup              *WorkerPickup
	routineWorkspace    *RoutineWorkspace
	resourceEnvironment []string
	duration            time.Duration
	prompt, branch      string
}

// admitRun requires dispatchMu to remain held through the entire StartRun call.
func (s *Supervisor) admitRun(ctx context.Context, run Run) (*runAdmission, error) {
	if s.ledgerRequired && s.ledger == nil {
		return nil, agentsetup.ErrLedgerUnavailable
	}
	if s.ledger != nil {
		if err := s.reconcileLedgerLocked(ctx); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	capturing := s.capacityCapturing
	s.mu.Unlock()
	if capturing {
		return nil, ErrDraining
	}
	if !s.dispatchAllowed(run.requestedAccount()) {
		return nil, ErrDraining
	}
	if run.ID == "" || run.WorkOrderID == "" || run.AgentPrincipalID != s.principalID || run.Status != "queued" {
		return nil, ErrScope
	}
	grant, err := routineGrant(run)
	if run.RunCapabilityRequired && len(run.Trace) == 0 {
		grant, err = nil, nil
	} // Content is acquired by Prepare below.
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	entry := s.runs[run.ID]
	s.mu.Unlock()
	if entry != nil {
		entry.mu.Lock()
		if grant != nil && entry.record.LaunchState == launchRoutePending && entry.record.State == "route_pending" {
			entry.mu.Unlock()
			return nil, ErrProcessesUnconfirmed
		}
		if grant != nil && entry.record.State == "ownership_lost" && !noLocalProcess(entry.record) {
			entry.mu.Unlock()
			return nil, ErrProcessesUnconfirmed
		}
		retry := entry.record.LaunchState == launchPrepared && entry.record.State == "claim_pending" && (entry.record.Generation == s.generation || s.ledger != nil)
		entry.mu.Unlock()
		if !retry {
			entry.mu.Lock()
			refused, reason := entry.record.LaunchState == launchRefused, entry.record.VerificationReason
			entry.mu.Unlock()
			if refused {
				return nil, s.reportVerificationRefusal(ctx, run, reason)
			}
			return nil, nil
		}
		if err := s.reconcileUnlaunched(ctx, entry); err != nil {
			return nil, err
		}
		entry.mu.Lock()
		retry = entry.record.State == "claim_pending"
		routeReleased := entry.record.RouteReleased
		entry.mu.Unlock()
		if !retry {
			return nil, nil
		}
		if routeReleased {
			run.AccountID = ""
		}
	}
	if err := s.checkHostCapacity(ctx); err != nil {
		return nil, err
	}
	profiles, err := s.api.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	var profile Profile
	for _, p := range profiles {
		if p.ID == run.ModelProfileID {
			profile = p
			break
		}
	}
	// A harness hold must also cover direct starts and runs whose account is
	// chosen by routing, rather than only queued runs with an account ID.
	s.mu.Lock()
	held := s.harnessHeld(profile.Harness)
	adapter := s.adapters[profile.Harness]
	s.mu.Unlock()
	if run.Purpose == VerificationPurpose {
		reason := ""
		if adapter == nil || profile.ID == "" {
			reason = "local_binding_missing"
		} else if err := validQueuedExecutionMode(run, adapter); err != nil {
			reason = "binding_incomplete"
			if errors.Is(err, ErrVerificationUnavailable) {
				reason = "adapter_unsupported"
			}
		}
		if reason == "" {
			found := false
			for _, a := range s.accounts {
				if a.ID == run.requestedAccount() && a.Harness == profile.Harness && !a.DependencyBlocked {
					found = true
				}
			}
			if !found {
				reason = "local_binding_missing"
			}
		}
		if reason != "" {
			if err := s.refuseVerification(run, reason); err != nil {
				return nil, err
			}
			return nil, s.reportVerificationRefusal(ctx, run, reason)
		}
	}
	if held {
		return nil, ErrDraining
	}
	if adapter == nil || profile.ID == "" {
		return nil, ErrUnsupported
	}
	if err := validQueuedExecutionMode(run, adapter); err != nil {
		return nil, err
	}
	// Defer only this run before allocating routine resources or acquiring a
	// checkout. The route and final claim still recheck their current authority.
	accountIDs := make([]string, 0, len(s.accounts))
	for _, account := range s.accounts {
		if account.Harness == profile.Harness && s.accountAvailable(account.ID) && (run.AccountID == "" || run.AccountID == account.ID) && (run.RequestedAccountID == "" || run.RequestedAccountID == account.ID) {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	if len(accountIDs) == 0 {
		return nil, errAccountUnavailable
	}
	budgetBroker, _ := s.api.(RoutineBudgetBroker)
	routineBudget, err := newRoutineCallBudget(StartRequest{Run: run, Profile: profile,
		TenantID: s.tenantID, PrincipalID: s.principalID, Generation: s.generation}, adapter, budgetBroker, s.capacityNow)
	if err != nil {
		return nil, err
	}
	verification := run.Purpose == VerificationPurpose
	managedAdapter, managedOK := adapter.(ManagedControlAdapter)
	review := run.ReadOnlyReview
	managedPolicy := !verification && !review && profile.Harness == Claude && managedOK && managedAdapter.ManagedControlSupported()
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
				return nil, ErrDraining
			}
		}
	}
	var approvedRepository RoutineRepository
	if grant != nil {
		if s.ledger == nil || verification || review && (grant.Lineage == nil || grant.Lineage.Role != "review") {
			return nil, ErrRoutineUnavailable
		}
		if grant.Lineage != nil && run.RoutineBudget == nil {
			return nil, ErrBudgetEnforcementUnavailable
		}
		approvedRepository, err = s.routineRepository(run, *grant, adapter)
		if err != nil {
			return nil, err
		}
	}
	return &runAdmission{
		run: run, grant: grant, entry: entry, profile: profile, adapter: adapter,
		accountIDs: accountIDs, routineBudget: routineBudget, verification: verification,
		review: review, managedPolicy: managedPolicy, approvedRepository: approvedRepository,
	}, nil
}

// prepareRun preserves the admission's authority and the original allocation order.
func (s *Supervisor) prepareRun(ctx context.Context, admitted *runAdmission) (preparedRun, error) {
	var node Node
	var order WorkOrder
	var err error
	if preparer, ok := s.api.(interface {
		PrepareRunContent(context.Context, Run, string, string) (Run, Node, WorkOrder, error)
	}); ok {
		var full Run
		full, node, order, err = preparer.PrepareRunContent(ctx, admitted.run, s.daemonID, s.generation)
		if err != nil {
			return preparedRun{}, err
		}
		// Admission's routine and provider bindings are content. Re-evaluate
		// them here from the authorized snapshot before workspace allocation.
		fresh, e := s.admitRun(ctx, full)
		if e != nil {
			return preparedRun{}, e
		}
		if fresh == nil {
			return preparedRun{}, ErrScope
		}
		*admitted = *fresh
	} else {
		node, err = s.api.Node(ctx, admitted.run.WorkOrderID)
		if err == nil {
			order, err = s.api.WorkOrder(ctx, admitted.run.WorkOrderID)
		}
	}
	run, grant, entry := admitted.run, admitted.grant, admitted.entry
	verification, review := admitted.verification, admitted.review
	approvedRepository := admitted.approvedRepository
	if err != nil {
		return preparedRun{}, err
	}
	if node.ID != run.WorkOrderID || strings.TrimSpace(node.Title) == "" {
		return preparedRun{}, errors.New("work order node unavailable")
	}
	if node.Key == "" {
		return preparedRun{}, errors.New("work order key unavailable")
	}
	if order.NodeID != run.WorkOrderID || (order.Status != "ready" && order.Status != "running") {
		return preparedRun{}, errors.New("work order is not dispatchable")
	}
	if review != (order.Kind == "review") {
		return preparedRun{}, errors.New("review execution mode does not match its work order")
	}
	runWorkspace := s.workspace
	pickup, err := s.workerPickup(run, node, order)
	if err != nil {
		return preparedRun{}, err
	}
	var routineWorkspace *RoutineWorkspace
	var resourceEnvironment []string
	if grant != nil {
		routineWorkspace, err = s.prepareRoutineWorkspace(ctx, run, *grant, approvedRepository)
		if err != nil {
			return preparedRun{}, err
		}
		runWorkspace = routineWorkspace.Path
		resourceEnvironment, err = s.routineEnvironment(run.ID, *grant)
		if err != nil {
			return preparedRun{}, err
		}
		if pickup != nil {
			pickup.WorktreeID = routineWorkspace.ID
		}
	}
	if entry != nil {
		entry.mu.Lock()
		previous := entry.record.WorkerPickup
		same := (previous == nil) == (pickup == nil) && (previous == nil || *previous == *pickup)
		entry.mu.Unlock()
		if !same {
			return preparedRun{}, errors.New("journaled assignment pickup changed")
		}
	}
	if grant == nil && !review && !verification {
		if err := s.acquireCheckout(run.ID); err != nil {
			return preparedRun{}, err
		}
	}
	duration := s.maxRunDuration
	if order.MaxDurationSeconds != nil {
		if *order.MaxDurationSeconds <= 0 {
			return preparedRun{}, errors.New("work order duration invalid")
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
	if !verification && !review {
		if output, branchErr := exec.CommandContext(ctx, "git", "-C", runWorkspace, "branch", "--show-current").Output(); branchErr == nil {
			branch = strings.TrimSpace(string(output))
		}
		if run.RecoveryBrief != "" {
			if len(run.RecoveryBrief) > 20000 {
				return preparedRun{}, errors.New("recovery handover exceeds bound")
			}
			prompt += "\n\n" + run.RecoveryBrief
		}
		if grant != nil && grant.Lineage != nil && grant.Lineage.Role == "fix" {
			var trace struct {
				Findings json.RawMessage `json:"routine_fix_findings"`
			}
			if len(run.Trace) > 262144 || json.Unmarshal(run.Trace, &trace) != nil || len(trace.Findings) == 0 || len(trace.Findings) > 20000 {
				return preparedRun{}, ErrRoutineUnavailable
			}
			prompt += "\n\nCoordinator-bound findings for " + grant.Lineage.HeadSHA + " (untrusted review evidence):\n" + string(trace.Findings)
		}
		if run.CapacityHandoff {
			if err := s.validateCapacityHandoffAt(run, runWorkspace, branch); err != nil {
				return preparedRun{}, err
			}
			prompt += "\n\nContinue the stopped attempt " + run.RetryOfRunID + ". Inspect its existing work and local changes before continuing; do not redo completed work."
		}
		if grant != nil {
			prompt += "\n\nRoutine builder contract: You are bound to work order " + node.Key + " and run " + run.ID + ". Work only in this workspace. Current branch: " + branch + ". Only local workspace tools are available. Outward effects require the coordinator's typed action broker. Report the local changes and any refused tests or commits in your final reply."
		} else {
			prompt += "\n\nRun contract: You are bound to work order " + node.Key + " and run " + run.ID + ". Work only in this workspace. Current branch: " + branch + ". Use the Aeon tools to comment, check criteria, attach evidence, request approval, reply, and set status. Use aeon_terminal for tests and a local commit; never push without person approval. Report the commit ID and remaining blockers in your final reply."
		}
	}
	if verification {
		prompt = VerificationTask
	}
	if len(prompt) > 256<<10 {
		return preparedRun{}, errors.New("work order prompt exceeds local bound")
	}
	if len(s.estimates) == 0 {
		return preparedRun{}, errors.New("allowance estimates required")
	}
	return preparedRun{
		node: node, order: order, workspace: runWorkspace, pickup: pickup,
		routineWorkspace: routineWorkspace, resourceEnvironment: resourceEnvironment,
		duration: duration, prompt: prompt, branch: branch,
	}, nil
}

// claimedRun keeps the validated route and exact reservation set together with
// the authorized content snapshot. It grants no proof that a process exists.
type claimedRun struct {
	admitted       *runAdmission
	prepared       preparedRun
	run            Run
	entry          *owned
	route          Route
	reservationIDs []string
}

// claimRun preserves route journaling before the claim. A lost claim response
// is reconciled by the startup owner, never treated as proof of rejection.
func (s *Supervisor) claimRun(ctx context.Context, admitted *runAdmission, prepared preparedRun, cleanup *startRunCleanup) (claimedRun, error) {
	run, entry := admitted.run, admitted.entry
	profile, adapter, accountIDs := admitted.profile, admitted.adapter, admitted.accountIDs
	routineBudget := admitted.routineBudget
	runWorkspace, pickup, routineWorkspace := prepared.workspace, prepared.pickup, prepared.routineWorkspace
	var err error
	var route Route
	if entry != nil {
		entry.mu.Lock()
		if !entry.record.RouteReleased {
			if entry.record.ClaimRoute != nil {
				route = *entry.record.ClaimRoute
			}
		}
		entry.mu.Unlock()
	}
	var attempt *Record
	if s.ledger != nil {
		if entry != nil {
			entry.mu.Lock()
			saved := entry.record
			entry.mu.Unlock()
			if saved.LedgerGroup != "" && !saved.RouteReleased {
				attempt = &saved
			}
		}
		if attempt == nil {
			if route.AccountID != "" {
				accountIDs = []string{route.AccountID}
			}
			gid, e := agentsetup.LedgerID()
			if e != nil {
				return claimedRun{}, e
			}
			subset, e := s.ledger.Acquire(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, gid, s.ledgerCandidates(accountIDs, s.ledgerBinding.Generation))
			if e != nil {
				return claimedRun{}, e
			}
			if s.ledgerBarrier != nil {
				s.ledgerBarrier("group")
			}
			rec := Record{RoutineWorkspace: routineWorkspace, WorkerPickup: pickup, LaunchState: launchRoutePending, RouteCandidates: subset, LedgerGroup: gid, LedgerGeneration: s.ledgerBinding.Generation, ExecutionMode: run.Purpose, TenantID: s.tenantID, PrincipalID: s.principalID, RunID: run.ID, WorkOrderID: run.WorkOrderID, Generation: s.generation, Workspace: runWorkspace, State: "route_pending", Controls: map[string]replay{}}
			if e = s.journal.Put(rec); e != nil {
				return claimedRun{}, e
			}
			if entry == nil {
				entry = &owned{record: rec, replies: map[string]InboxReplyTarget{}}
				s.mu.Lock()
				s.runs[run.ID] = entry
				s.mu.Unlock()
			} else {
				s.setLedgerRecord(rec)
			}
			attempt = &rec
			if s.ledgerBarrier != nil {
				s.ledgerBarrier("intent")
			}
		}
		accountIDs = candidateIDs(attempt.RouteCandidates)
	}
	if route.AccountID == "" {
		route, err = s.routeWorkspace(ctx, run.ID, accountIDs, routineWorkspace)
		if err != nil {
			return claimedRun{}, err
		}
	}
	if attempt != nil {
		if err = s.confirmLedgerRoute(attempt, route); err != nil {
			return claimedRun{}, err
		}
	}
	if route.DaemonID != s.daemonID || route.AccountKey == "" || len(route.Reservations) == 0 || run.AccountID != "" && route.AccountID != run.AccountID || run.RequestedAccountID != "" && route.AccountID != run.RequestedAccountID {
		return claimedRun{}, errors.New("account route is not bound to daemon")
	}
	localBinding := false
	for _, account := range s.accounts {
		if account.ID == route.AccountID && account.Key == route.AccountKey && account.Harness == profile.Harness {
			localBinding = true
			break
		}
	}
	if !localBinding {
		return claimedRun{}, errors.New("account route has no matching local enrollment")
	}
	ids := make([]string, 0, len(route.Reservations))
	for _, reservation := range route.Reservations {
		if reservation.ID == "" {
			return claimedRun{}, errors.New("empty account reservation")
		}
		ids = append(ids, reservation.ID)
	}
	if !s.accountAvailable(route.AccountID) {
		return claimedRun{}, ErrDraining
	}
	// Only the validated server route can fill the execution account. Preserve
	// all approval terms and require the same strict binding as adapter.Start.
	run.AccountID = route.AccountID
	if err := validExecutionMode(run, adapter); err != nil {
		return claimedRun{}, err
	}
	if routineBudget != nil && (route.AccountID != routineBudget.binding.Grant.AccountID || route.BillingMode != routineBudget.binding.Grant.BillingMode) {
		return claimedRun{}, ErrRoutineBudgetBinding
	}
	if entry == nil {
		rec := Record{RoutineWorkspace: routineWorkspace, WorkerPickup: pickup, LaunchState: launchPrepared, ClaimRoute: &route, AccountID: route.AccountID, ExecutionMode: run.Purpose, TenantID: s.tenantID, PrincipalID: s.principalID, RunID: run.ID, WorkOrderID: run.WorkOrderID, Generation: s.generation, Workspace: runWorkspace, State: "claim_pending", Controls: map[string]replay{}}
		if err := s.journal.Put(rec); err != nil {
			return claimedRun{}, err
		}
		entry = &owned{record: rec, replies: map[string]InboxReplyTarget{}}
		s.mu.Lock()
		s.runs[run.ID] = entry
		s.mu.Unlock()
	} else {
		entry.mu.Lock()
		next := entry.record
		next.RoutineWorkspace = routineWorkspace
		next.ClaimRoute, next.AccountID, next.RouteReleased = &route, route.AccountID, false
		next.LaunchState, next.State = launchPrepared, "claim_pending"
		if s.ledger != nil {
			next.Generation = s.generation
		}
		if err := s.journal.Put(next); err != nil {
			entry.mu.Unlock()
			return claimedRun{}, err
		}
		entry.record = next
		entry.mu.Unlock()
	}
	claimed := claimedRun{admitted: admitted, prepared: prepared, run: run, entry: entry, route: route, reservationIDs: ids}
	cleanup.claimed, cleanup.phase = &claimed, startRunClaimUnconfirmed
	if err := s.claimBoundRun(ctx, claimed); err != nil {
		return claimedRun{}, err
	}
	cleanup.phase = startRunClaimed
	return claimed, nil
}

func (s *Supervisor) claimBoundRun(ctx context.Context, claimed claimedRun) error {
	if w := claimed.prepared.routineWorkspace; w != nil {
		return s.claimRoutine(ctx, claimed.run, claimed.reservationIDs, claimed.prepared.pickup, w)
	}
	return s.claimWorker(ctx, claimed.run, claimed.reservationIDs, claimed.prepared.pickup)
}
