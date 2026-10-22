// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/agentactivity"
	"github.com/inspr-at/paimos/internal/servicetier"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

// launchedRun contains the live process returned by the adapter. Only handoffRun
// transfers its cleanup to the monitor; a failed Start never creates this value.
type launchedRun struct {
	process   Process
	startedAt time.Time
	rules     string
}

func (s *Supervisor) launchRun(ctx context.Context, claimed claimedRun, cleanup *startRunCleanup) (launchedRun, error) {
	admitted, prepared := claimed.admitted, claimed.prepared
	run, entry, route := claimed.run, claimed.entry, claimed.route
	grant, profile, adapter := admitted.grant, admitted.profile, admitted.adapter
	verification, review, managedPolicy := admitted.verification, admitted.review, admitted.managedPolicy
	approvedRepository, routineBudget := admitted.approvedRepository, admitted.routineBudget
	order, runWorkspace, routineWorkspace := prepared.order, prepared.workspace, prepared.routineWorkspace
	resourceEnvironment, duration := prepared.resourceEnvironment, prepared.duration
	prompt, branch := prepared.prompt, prepared.branch
	var err error
	if review {
		repository := s.workspace
		if grant != nil {
			repository = approvedRepository.Repository
		}
		prompt, err = reviewPrompt(ctx, repository, order, profile)
		if err != nil {
			return launchedRun{}, err
		}
	}
	if verification || review && routineWorkspace == nil {
		runWorkspace, err = s.prepareScratch(s.workspace)
		if err != nil {
			return launchedRun{}, err
		}
	}
	harness, err := s.registerRunHarness(ctx, claimed, runWorkspace, cleanup)
	if err != nil {
		return launchedRun{}, err
	}
	caps := harness.caps
	runTools, err := s.prepareRunTools(claimed, runWorkspace)
	if err != nil {
		return launchedRun{}, err
	}
	ephemeralRules, err := s.prepareClaudeRules(ctx, entry, run, profile, prepared, &prompt, runTools)
	if err != nil {
		return launchedRun{}, err
	}
	observe := func(ev AdapterEvent) { s.observe(entry, ev) }
	if !verification && !review {
		entry.mu.Lock()
		entry.chat = newSessionChat(ChatBinding{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: run.ID, Generation: s.generation, SessionID: entry.harness.ID}, sessionChatCapabilities(profile.Harness, caps))
		if harness.relayAPI != nil && slices.Contains(caps, chatCapability) {
			entry.relay = s.newChatRelay(harness.relayAPI, entry, run.ID)
			entry.chat.attachRelay(entry.relay)
			go entry.relay.Run(s.lifetime)
		}
		entry.mu.Unlock()
	}
	if routineWorkspace != nil {
		if err := validateRoutineCheckout(ctx, approvedRepository, *routineWorkspace); err != nil {
			return launchedRun{}, err
		}
		if run.CapacityHandoff || routineWorkspace.Grant.Lineage != nil && routineWorkspace.Grant.Lineage.Role == "fix" {
			retained, err := s.routineContinuation(ctx, run, routineWorkspace.Grant, approvedRepository)
			if err != nil {
				return launchedRun{}, err
			}
			if retained.ID != routineWorkspace.ID || retained.Path != runWorkspace || retained.Branch != branch || retained.CheckoutLaunchID != routineWorkspace.CheckoutLaunchID {
				return launchedRun{}, ErrProcessesUnconfirmed
			}
			if run.CapacityHandoff {
				if err := s.validateCapacityHandoffAt(run, runWorkspace, branch); err != nil {
					return launchedRun{}, err
				}
			}
		}
		if err := s.claimBoundRun(ctx, claimed); err != nil {
			return launchedRun{}, err
		}
		resourceEnvironment, err = s.routineEnvironment(run.ID, routineWorkspace.Grant)
		if err != nil {
			return launchedRun{}, err
		}
	}
	// Commit the possible-fork boundary before handing control to an adapter.
	// On a journal failure Start is never invoked; after success a crash is
	// unconfirmed, even if no PID was subsequently persisted.
	var launchRev, launchDefault string
	if !verification && !review {
		launchRev, launchDefault = workspaceHEAD(ctx, runWorkspace), launchDefaultRev(ctx, runWorkspace)
	}
	if s.ledger != nil {
		entry.mu.Lock()
		r := entry.record
		entry.mu.Unlock()
		if err = s.ledger.Launching(s.ledgerBinding.Member.ID, r.LedgerGeneration, r.LedgerGroup); err != nil {
			return launchedRun{}, err
		}
		if s.ledgerBarrier != nil {
			s.ledgerBarrier("launching")
		}
	}
	entry.mu.Lock()
	intent := entry.record
	intent.LaunchState, intent.State = launchAttempted, "starting"
	intent.LaunchRev, intent.LaunchDefaultRev = launchRev, launchDefault
	intent.AutomaticReview = order.Kind == "build"
	intent.LaunchBranch = branch
	err = s.journal.Put(intent)
	if err == nil {
		entry.record = intent
	}
	entry.mu.Unlock()
	if err != nil {
		return launchedRun{}, err
	}
	if s.ledgerBarrier != nil {
		s.ledgerBarrier("attempted")
	}
	if routineWorkspace != nil {
		s.routineResources[run.ID].HandoffPorts()
	}
	launchedAt := time.Now()
	s.verificationDiagnosticFor(run.ID, run.AccountID, run.Purpose, "starting", "")
	// The accepted duration includes startup, before the monitor exists.
	runCtx, cancelRun := context.WithTimeout(s.lifetime, duration)
	cleanup.cancelRun = cancelRun
	startCtx, cancelStart := context.WithDeadline(ctx, launchedAt.Add(duration))
	cleanup.releasePower = s.holdRunPower()
	cleanup.phase = startRunLaunching
	proc, err := startRoutineBudgetAdapter(startCtx, adapter, StartRequest{ResourceEnvironment: resourceEnvironment, Lifetime: runCtx, TenantID: s.tenantID, PrincipalID: s.principalID, Run: run, Profile: profile,
		AccountKey: route.AccountKey, Workspace: runWorkspace, StateRoot: filepath.Dir(s.journal.JournalPath()), Prompt: prompt, Generation: s.generation, InboxEnabled: entry.inboxCapable, ManagedPolicy: managedPolicy, Capabilities: caps, ServiceTier: entry.harness.ServiceTier, Tools: runTools, Rules: ephemeralRules, MaxTurns: entry.turnBudget, MaxTokens: entry.tokenBudget}, routineBudget, observe)
	cancelStart()
	if err != nil {
		return launchedRun{}, err
	}
	return launchedRun{process: proc, startedAt: launchedAt, rules: ephemeralRules}, nil
}

// runHarness carries only successfully registered capabilities into launch.
type runHarness struct {
	caps     []string
	relayAPI ChatRelayAPI
}

func (s *Supervisor) registerRunHarness(ctx context.Context, claimed claimedRun, runWorkspace string, cleanup *startRunCleanup) (runHarness, error) {
	run, entry, route := claimed.run, claimed.entry, claimed.route
	profile, adapter := claimed.admitted.profile, claimed.admitted.adapter
	verification, review, managedPolicy := claimed.admitted.verification, claimed.admitted.review, claimed.admitted.managedPolicy
	node := claimed.prepared.node
	projectID, err := s.api.ProjectForNode(ctx, node.Key)
	if err != nil {
		return runHarness{}, err
	}
	host, err := os.Hostname()
	if err != nil || host == "" || len(host) > 128 {
		return runHarness{}, errors.New("valid harness host unavailable")
	}
	ref, err := s.newHarnessID()
	if err != nil {
		return runHarness{}, err
	}
	leaseA, err := s.newHarnessID()
	if err != nil {
		return runHarness{}, err
	}
	leaseB, err := s.newHarnessID()
	if err != nil {
		return runHarness{}, err
	}
	entry.managedPolicy = managedPolicy
	if managedPolicy {
		entry.tokenBudget, entry.turnBudget = s.maxTokens, s.maxTurns
	}
	caps := []string{"status", "stop"}
	if managedPolicy {
		caps = append(caps, recoveryCapability, managedControlCapability, "steer", "rename", "model", "effort")
	}
	if profile.Harness != Grok && !review {
		caps = append(caps, "interrupt")
	}
	entry.queueInput = queuedChatHarness(profile.Harness)
	entry.inboxCapable = !verification && !review && (profile.Harness == Claude || profile.Harness == Codex || profile.Harness == Pi || entry.queueInput)
	if entry.inboxCapable {
		caps = append(caps, "inbox")
		if !managedPolicy {
			caps = append(caps, "steer")
		}
	}
	if (profile.Harness == Claude || profile.Harness == Codex) && !verification && !review {
		caps = append(caps, servicetier.Capability)
	}
	relayAPI, relayable := s.api.(ChatRelayAPI)
	// Advertise chat only for an S1-streaming harness the server's chat
	// registration admits and whose turns name the input they consume; person
	// input additionally needs inbox delivery.
	if relayable && !verification && !review && relayChatHarness(profile.Harness) && sessionChatCapabilities(profile.Harness, caps).Deltas && s.chatGate.enabled() {
		caps = append(caps, chatCapability)
	}
	registration := HarnessSession{ID: s.generation + "/" + ref, ProjectID: projectID, Lease: leaseA + leaseB, AccountLabel: route.AccountLabel, DisplayLabel: run.RecoveryLabel}
	if profile.Harness != Codex {
		registration.Model, registration.ReasoningEffort = profile.Model, profile.Effort
	}
	entry.harness, err = s.api.RegisterHarness(ctx, registration,
		s.principalID, run.ID, run.WorkOrderID, profile.Harness, host, caps)
	if err != nil && slices.Contains(caps, chatCapability) && chatCapabilityRefused(err) {
		// An older server rejects the capability before writing anything. The
		// run registers without live chat; only the relay is switched off.
		s.chatGate.disable("capability_refused")
		caps = slices.DeleteFunc(caps, func(c string) bool { return c == chatCapability })
		entry.harness, err = s.api.RegisterHarness(ctx, registration,
			s.principalID, run.ID, run.WorkOrderID, profile.Harness, host, caps)
	}
	if err != nil {
		return runHarness{}, err
	}
	cleanup.registered = true
	if api, ok := s.api.(serviceTierAPI); ok {
		report := servicetier.Advertised(profile.Harness, profile.Model, "unknown")
		if reporter, ok := adapter.(serviceTierAdapter); ok {
			observed, e := reporter.ServiceTiers(ctx, StartRequest{Profile: profile, AccountKey: route.AccountKey, Workspace: runWorkspace})
			if e == nil {
				report = observed
			}
		}
		active := "default"
		if run.RecoveryTier != "" {
			if !servicetier.Valid(run.RecoveryTier) {
				return runHarness{}, errors.New("invalid recovery service tier")
			}
			offered := false
			for _, tier := range report.Tiers {
				if tier.Tier == run.RecoveryTier && tier.Offered {
					offered = true
				}
			}
			if !offered {
				return runHarness{}, errors.New("recovery service tier no longer offered")
			}
			active = run.RecoveryTier
		}
		if err = api.ReportServiceTiers(ctx, entry.harness, servicetier.Reports(report.Harness, report.Model, report.HarnessVersion), &active); err != nil {
			return runHarness{}, err
		}
		entry.harness.ServiceTier = active
	}
	s.mu.Lock()
	billing := s.accountBilling[route.AccountID]
	s.mu.Unlock()
	if sessionusage.BillingMode(billing) == "unknown" {
		billing = route.BillingMode
	}
	if usageAPI, ok := s.api.(sessionUsageAPI); ok {
		entry.usage = newSessionUsageReporter(usageAPI, entry.harness, func() bool {
			entry.mu.Lock()
			defer entry.mu.Unlock()
			return entry.harnessArchived || entry.record.Generation != s.generation
		}, func() {
			entry.mu.Lock()
			entry.harnessArchived = true
			entry.chat.close()
			entry.mu.Unlock()
		})
		entry.usage.billing = sessionusage.BillingMode(billing)
		entry.usage.accountID = route.AccountID
		entry.usage.plan = route.SubscriptionLabel
	}
	return runHarness{caps: caps, relayAPI: relayAPI}, nil
}

func (s *Supervisor) prepareRunTools(claimed claimedRun, runWorkspace string) (*RunTools, error) {
	run, entry := claimed.run, claimed.entry
	grant := claimed.admitted.grant
	verification, review := claimed.admitted.verification, claimed.admitted.review
	resourceEnvironment, branch := claimed.prepared.resourceEnvironment, claimed.prepared.branch
	var err error
	var runTools *RunTools
	if toolAPI, ok := s.api.(RunToolAPI); ok && !verification && !review {
		if scoped, ok := s.api.(interface{ RunToolsFor(string) RunToolAPI }); ok {
			toolAPI = scoped.RunToolsFor(run.ID)
		}
		if signer, ok := s.api.(interface {
			runCredential(string, string, string, string) string
		}); ok {
			active := func() bool {
				entry.mu.Lock()
				defer entry.mu.Unlock()
				return entry.budgetStopReason() == "" && entry.record.Generation == s.generation && (entry.record.State == "starting" || entry.record.State == "running")
			}
			replySender := func(messageID string) (InboxReplyTarget, bool) {
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
			reportActivity := func(doing string) (string, bool) {
				entry.mu.Lock()
				id := entry.harness.ID
				entry.mu.Unlock()
				mode := agentactivity.Off
				if policy, ok := s.api.(interface{ SessionActivityMode(string) string }); ok {
					mode = policy.SessionActivityMode(id)
				}
				if mode != agentactivity.Summary || doing == "" {
					return mode, false
				}
				s.observe(entry, AdapterEvent{Doing: doing})
				return mode, true
			}
			var browser BrowserCaptureRequester
			if grant != nil && s.browserCaptures != nil {
				browser = s.browserCaptures(*grant)
			}
			entry.tools, err = startManagedTools(signer.runCredential(s.tenantID, s.principalID, run.ID, s.generation),
				toolBinding{routine: grant, browser: browser, resourceEnvironment: resourceEnvironment, api: toolAPI, workOrderID: run.WorkOrderID, runID: run.ID, workspace: runWorkspace, branch: branch, active: active, replySender: replySender, requestDone: requestDone, reportActivity: reportActivity})
			if err != nil {
				return nil, err
			}
			runTools = &entry.tools.tools
		}
	}
	return runTools, nil
}

// handoffRun always installs the monitor, including when the initial journal,
// telemetry or heartbeat fails. Such errors cannot return live owners to startup.
func (s *Supervisor) handoffRun(ctx context.Context, claimed claimedRun, launched launchedRun, cleanup *startRunCleanup) error {
	entry, proc := claimed.entry, launched.process
	run, routineWorkspace, duration := claimed.run, claimed.prepared.routineWorkspace, claimed.prepared.duration
	cancelRun, releasePower := cleanup.cancelRun, cleanup.releasePower
	cleanup.phase = startRunMonitored
	entry.mu.Lock()
	entry.process = proc
	entry.record.PID = proc.PID()
	entry.record.ProcessStartedAt = launched.startedAt
	if recovery, ok := proc.(RecoveryProcess); ok {
		if identity, e := recovery.Ownership(); e == nil && identity.RootPID == proc.PID() && identity.GroupID == proc.PID() {
			entry.record.ProcessGroupID = identity.GroupID
			entry.record.ProcessStartedAt = identity.StartedAt
		}
	}
	entry.record.State = "running"
	saveErr := s.journal.Put(entry.record)
	if routineWorkspace != nil {
		saveErr = errors.Join(saveErr, s.routineResources[run.ID].Running(proc.PID()))
	}
	runningRecord := entry.record
	entry.mu.Unlock()
	if s.ledger != nil {
		saveErr = errors.Join(saveErr, s.ledger.Running(s.ledgerBinding.Member.ID, runningRecord.LedgerGeneration, runningRecord.LedgerGroup, proc.PID(), runningRecord.ProcessStartedAt))
	}
	entry.mu.Lock()
	entry.monitorDone = make(chan struct{})
	done := entry.monitorDone
	entry.mu.Unlock()
	go func() { <-done; cancelRun() }()
	entry.budgetMu.Lock()
	entry.budgetProcess, entry.budgetTools = proc, entry.tools
	entry.budgetMu.Unlock()
	s.observeBudget(entry, AdapterEvent{})
	go s.runDeadline(entry, proc, done, time.Until(launched.startedAt.Add(duration)))
	saveErr = errors.Join(saveErr, s.acknowledgeRulesDelivery(ctx, entry, proc, launched.rules))
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
		entry.chat.close()
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
	if binder, ok := proc.(interface{ bindLifetime(context.Context) }); ok {
		binder.bindLifetime(s.lifetime)
	}
	go s.monitor(entry, releasePower)
	entry.mu.Lock()
	entry.harnessWake = make(chan struct{}, 1)
	entry.mu.Unlock()
	go s.heartbeat(entry)
	go s.watchHarness(entry)
	return startErr
}
