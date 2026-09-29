// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

var ErrDraining = errors.New("daemon is draining; active processes are preserved")
var ErrProcessesUnconfirmed = errors.New("prior process ownership is unconfirmed; cleanup is not authorized")

type DrainRequest struct {
	DaemonID  string `json:"daemon_id"`
	AccountID string `json:"account_id,omitempty"`
}

// LifecycleStatus does not conflate telemetry acceptance with observed exit.
// Only a drained status permits removing a pairing-owned service or credential.
type LifecycleStatus struct {
	ProfilePermissions      bool                                `json:"profile_permissions,omitempty"`
	HarnessFailed           bool                                `json:"harness_failed,omitempty"`
	HarnessFailedAccountIDs []string                            `json:"harness_failed_account_ids,omitempty"`
	BlockedAccounts         []agentsetup.BlockedAccount         `json:"blocked_accounts,omitempty"`
	HarnessDetails          map[string]agentsetup.HarnessDetail `json:"harness_details,omitempty"`
	HarnessStatuses         map[string]string                   `json:"harness_statuses,omitempty"`
	HarnessErrors           map[string]string                   `json:"harness_errors,omitempty"`
	LoginRequired           bool                                `json:"login_required"`
	VerificationUnavailable []string                            `json:"verification_unavailable_account_ids"`
	Ready                   bool                                `json:"ready"`
	DaemonID                string                              `json:"daemon_id"`
	Generation              string                              `json:"generation"`
	State                   string                              `json:"state"`
	ActiveRunIDs            []string                            `json:"active_run_ids"`
	UnconfirmedRunIDs       []string                            `json:"unconfirmed_run_ids"`
	SettlementPendingRunIDs []string                            `json:"settlement_pending_run_ids"`
	FencedAccountIDs        []string                            `json:"fenced_account_ids"`
	AllFenced               bool                                `json:"all_fenced"`
	VerificationResults     map[string]string                   `json:"verification_results"`
}

func fenceName(account string) string {
	if account == "" {
		return "dispatch-fence-all.json"
	}
	return "dispatch-fence-" + agentsetup.Hash([]byte(account)) + ".json"
}

// PersistFence is monotonic. Setup can call it while agentd is offline; a
// restarted daemon cannot interpret stale approved setup state as authority.
func PersistFence(stateRoot, daemonID, accountID string) error {
	s, err := agentsetup.OpenStore(stateRoot, false)
	if err != nil {
		return err
	}
	defer s.Close()
	raw, _ := json.Marshal(DrainRequest{DaemonID: daemonID, AccountID: accountID})
	err = s.Write(fenceName(accountID), raw, true)
	if errors.Is(err, agentsetup.ErrCollision) {
		existing, readErr := s.Read(fenceName(accountID), 4096)
		if readErr == nil && string(existing) == string(raw) {
			return nil
		}
	}
	return err
}

func (s *Supervisor) readFence(account string) (bool, error) {
	raw, err := s.state.Read(fenceName(account), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	var req DrainRequest
	if json.Unmarshal(raw, &req) != nil || req.DaemonID != s.daemonID || req.AccountID != account {
		return true, ErrScope
	}
	return true, nil
}

func (s *Supervisor) dispatchAllowed(account string) bool {
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return false
	}
	fenced, err := s.readFence("")
	if err != nil || fenced {
		return false
	}
	if account != "" {
		fenced, err = s.readFence(account)
	}
	return err == nil && !fenced
}

func (s *Supervisor) Drain(req DrainRequest) (LifecycleStatus, error) {
	if req.DaemonID != s.daemonID {
		return LifecycleStatus{}, ErrScope
	}
	s.mu.Lock()
	accounts := append([]EnrolledAccount(nil), s.accounts...)
	s.mu.Unlock()
	if req.AccountID != "" {
		found := false
		for _, a := range accounts {
			if a.ID == req.AccountID {
				found = true
				break
			}
		}
		if !found {
			return LifecycleStatus{}, ErrScope
		}
	}
	// Serialize with the complete launch critical section. The success receipt
	// proves every later StartRun observes this durable fence.
	s.dispatchMu.Lock()
	err := PersistFence(s.state.Path(), s.daemonID, req.AccountID)
	s.dispatchMu.Unlock()
	if err != nil {
		return LifecycleStatus{}, err
	}
	return s.Lifecycle(req.AccountID), nil
}

func (s *Supervisor) Lifecycle(accountID string) LifecycleStatus {
	v := LifecycleStatus{DaemonID: s.daemonID, Generation: s.generation, State: "drained", ActiveRunIDs: []string{}, UnconfirmedRunIDs: []string{}, SettlementPendingRunIDs: []string{}, FencedAccountIDs: []string{}, VerificationResults: map[string]string{}, HarnessErrors: map[string]string{}}
	s.mu.Lock()
	v.HarnessStatuses = map[string]string{}
	v.HarnessDetails = map[string]agentsetup.HarnessDetail{}
	v.AllFenced, _ = s.readFence("")
	launchable := 0
	perHarness := map[string][]harnessAccountState{}
	for _, a := range s.accounts {
		status, reason := "checking", "starting"
		fenced, e := s.readFence(a.ID)
		if a.DependencyBlocked {
			v.HarnessFailedAccountIDs = append(v.HarnessFailedAccountIDs, a.ID)
			v.BlockedAccounts = append(v.BlockedAccounts, blockedReport(a))
		}
		if fenced || e != nil {
			v.FencedAccountIDs = append(v.FencedAccountIDs, a.ID)
		}
		if v.AllFenced || fenced || e != nil {
			status, reason = "draining", ""
		} else {
			// Fenced accounts cannot launch, so only live ones report failures.
			v.ProfilePermissions = v.ProfilePermissions || s.profilePermissions[a.ID]
			v.HarnessFailed = v.HarnessFailed || s.harnessFailed[a.ID] && !a.DependencyBlocked
			if !a.DependencyBlocked {
				launchable++
			}
			issue := s.harnessHolds[a.Harness]
			reason = s.harnessHoldReasons[a.Harness]
			if issue == "" {
				issue = s.dependencyErrors[a.ID]
				reason = s.dependencyReasons[a.ID]
			}
			if issue != "" {
				v.HarnessErrors[a.Harness] = issue
				status = "blocked"
				if reason == "" {
					reason = "dependency_invalid"
				}
			} else if a.DependencyBlocked {
				status, reason = "blocked", blockedReport(a).Reason
			} else if s.profilePermissions[a.ID] {
				status, reason = "blocked", "profile_permissions"
				v.ProfilePermissions = true
			} else if s.harnessFailed[a.ID] {
				status, reason = "blocked", "harness_failed"
			} else if s.loginRequired[a.ID] {
				status, reason = "login_required", "login_required"
				v.LoginRequired = true
			} else if s.probedAccounts[a.ID] && !s.blockedAccounts[a.ID] {
				status, reason = "ready", ""
				v.Ready = true
			} else {
				reason = "starting"
			}
			// A pin-blocked account stays unlaunchable; its verification waits
			// for the repair instead of being refused.
			if adapter, ok := s.adapters[a.Harness].(VerificationAdapter); !a.DependencyBlocked && (!ok || !adapter.VerificationSupported()) {
				v.VerificationUnavailable = append(v.VerificationUnavailable, a.ID)
			}
		}
		perHarness[a.Harness] = append(perHarness[a.Harness], harnessAccountState{id: a.ID, status: status, reason: reason})
	}
	assignHarnessReports(&v, perHarness)
	// One ready harness keeps the computer ready. Blocks stay per account and
	// per harness; HarnessFailed only summarizes a computer that cannot work.
	if v.Ready {
		v.HarnessFailed = false
	} else if launchable == 0 && len(v.BlockedAccounts) > 0 {
		v.HarnessFailed = true
	}
	entries := make([]*owned, 0, len(s.runs))
	for _, e := range s.runs {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		if accountID != "" && e.record.AccountID != accountID && e.record.AccountID != "" {
			e.mu.Unlock()
			continue
		}
		if (e.record.State == "ownership_lost" || e.process == nil) && !noLocalProcess(e.record) {
			v.UnconfirmedRunIDs = append(v.UnconfirmedRunIDs, e.record.RunID)
		} else if e.process != nil && !e.record.ExitObserved {
			v.ActiveRunIDs = append(v.ActiveRunIDs, e.record.RunID)
		}
		if len(e.record.Pending) > 0 || e.record.SettlementGap || e.record.State == "claim_pending" {
			v.SettlementPendingRunIDs = append(v.SettlementPendingRunIDs, e.record.RunID)
		}
		if e.record.ExecutionMode == VerificationPurpose {
			state := e.record.State
			if e.record.LaunchState == launchRefused && !slices.Contains(v.VerificationUnavailable, e.record.AccountID) {
				v.VerificationUnavailable = append(v.VerificationUnavailable, e.record.AccountID)
			}
			if !noLocalProcess(e.record) && (state == "completed" || state == "failed" || state == "cancelled") {
				state = "unconfirmed"
			}
			if len(e.record.Pending) > 0 || e.record.SettlementGap {
				state = "settlement_pending"
			}
			v.VerificationResults[e.record.RunID] = state
		}
		e.mu.Unlock()
	}
	if len(v.ActiveRunIDs) > 0 {
		v.State = "draining"
	}
	if len(v.UnconfirmedRunIDs) > 0 {
		v.State = "unconfirmed"
	}
	sort.Strings(v.ActiveRunIDs)
	sort.Strings(v.UnconfirmedRunIDs)
	sort.Strings(v.SettlementPendingRunIDs)
	sort.Strings(v.FencedAccountIDs)
	sort.Strings(v.HarnessFailedAccountIDs)
	sort.Slice(v.BlockedAccounts, func(i, j int) bool {
		if v.BlockedAccounts[i].AccountID == v.BlockedAccounts[j].AccountID {
			return v.BlockedAccounts[i].Harness < v.BlockedAccounts[j].Harness
		}
		return v.BlockedAccounts[i].AccountID < v.BlockedAccounts[j].AccountID
	})
	return v
}

// harnessAccountState is one account's contribution to its harness report.
// The type stays at package scope because a function cannot declare a named type.
type harnessAccountState struct {
	id, status, reason string
}

// assignHarnessReports keeps a harness ready when any account can work, and
// records the blocked or unsigned siblings in attention_accounts. A healthy
// account must not erase those blocks. When nobody is ready, the most
// actionable state wins; equal ranks keep the first account. Checking,
// starting and draining are not attention. A fenced account cannot hide a live one.
func assignHarnessReports(v *LifecycleStatus, perHarness map[string][]harnessAccountState) {
	priority := map[string]int{"draining": 1, "checking": 2, "login_required": 3, "blocked": 4, "ready": 5}
	for harness, accounts := range perHarness {
		ids := make([]string, 0, len(accounts))
		ready := false
		var pending []agentsetup.AccountAttention
		for _, account := range accounts {
			ids = append(ids, account.id)
			if account.status == "ready" {
				ready = true
			}
		}
		if ready {
			for _, account := range accounts {
				if account.status != "blocked" && account.status != "login_required" {
					continue
				}
				reason := account.reason
				if reason == "" {
					reason = account.status
				}
				pending = append(pending, agentsetup.AccountAttention{AccountID: account.id, Reason: reason})
			}
			if attention := agentsetup.PartialAttention(harness, ids, "ready", pending); len(attention.Accounts) > 0 {
				if detail, ok := agentsetup.HarnessReport(harness, "ready", ""); ok {
					detail.Attention = attention.Accounts
					detail.AttentionCount = attention.Count
					detail.AttentionTruncated = attention.Truncated
					v.HarnessStatuses[harness] = "ready"
					v.HarnessDetails[harness] = detail
					continue
				}
			}
		}
		bestStatus, bestReason := "", ""
		bestPriority := 0
		for _, account := range accounts {
			rank := priority[account.status]
			if rank > bestPriority {
				bestPriority = rank
				bestStatus = account.status
				bestReason = account.reason
			}
		}
		if bestStatus == "" {
			continue
		}
		v.HarnessStatuses[harness] = bestStatus
		if detail, ok := agentsetup.HarnessReport(harness, bestStatus, bestReason); ok {
			v.HarnessDetails[harness] = detail
		} else {
			delete(v.HarnessDetails, harness)
		}
	}
}

func blockedReport(a EnrolledAccount) agentsetup.BlockedAccount {
	reason := a.PinReason
	if reason == "" {
		reason = agentsetup.PinMissing
	}
	return agentsetup.BlockedAccount{AccountID: a.ID, Harness: a.Harness, Reason: reason, Fix: agentsetup.RecoveryFix(a.Harness, reason)}
}

func (s *Supervisor) freezeOnError(account string) {
	s.mu.Lock()
	s.blockedAccounts[account] = true
	s.mu.Unlock()
}

// handleRunError fences dispatch on uncertain delivery or authority loss, but
// stops the exact owned child only for a confirmed telemetry protocol failure.
// Keep the rejected outbox intact: observing exit does not settle server usage.
func (s *Supervisor) handleRunError(entry *owned, err error) {
	if err == nil {
		return
	}
	entry.mu.Lock()
	account := entry.record.AccountID
	var proc Process
	if errors.Is(err, ErrTelemetryProtocol) && entry.record.Generation == s.generation {
		entry.protocolFailed = true
		if entry.process != nil && !entry.record.ExitObserved && !entry.protocolStopped {
			entry.protocolStopped = true
			proc = entry.process
		}
	}
	entry.mu.Unlock()
	s.freezeOnError(account)
	if proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = proc.Stop(ctx)
	}
}

// harnessHeld reports a harness hold, or a dependency failure or pin block on
// every enrolled account of the harness. Callers hold s.mu.
func (s *Supervisor) harnessHeld(harness string) bool {
	if s.harnessHolds[harness] != "" {
		return true
	}
	enrolled := false
	for _, a := range s.accounts {
		if a.Harness != harness {
			continue
		}
		enrolled = true
		if !a.DependencyBlocked && s.dependencyErrors[a.ID] == "" {
			return false
		}
	}
	return enrolled
}

func (s *Supervisor) accountAvailable(account string) bool {
	if !s.dispatchAllowed(account) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.accounts {
		if a.ID == account && (a.DependencyBlocked || s.harnessHolds[a.Harness] != "" || s.dependencyErrors[a.ID] != "") {
			return false
		}
	}
	return !s.blockedAccounts[account] && !s.blockedAccounts[""]
}

// SetHarnessHold pauses fresh work for one harness without changing durable
// lifecycle fences or touching processes. An empty reason releases the hold;
// a successful account probe is still required before dispatch resumes.
func (s *Supervisor) SetHarnessHold(harness, reason string) {
	s.SetHarnessHoldWithReason(harness, "dependency_invalid", reason)
}

// SetHarnessHoldWithReason keeps wire reason codes independent of local diagnostics.
func (s *Supervisor) SetHarnessHoldWithReason(harness, code, reason string) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.harnessHolds == nil {
		s.harnessHolds = map[string]string{}
	}
	if s.harnessHoldReasons == nil {
		s.harnessHoldReasons = map[string]string{}
	}
	if reason == "" {
		delete(s.harnessHolds, harness)
		delete(s.harnessHoldReasons, harness)
		return
	}
	s.harnessHolds[harness] = reason
	s.harnessHoldReasons[harness] = code
	for _, a := range s.accounts {
		if a.Harness == harness {
			delete(s.probedAccounts, a.ID)
			delete(s.loginRequired, a.ID)
			s.blockedAccounts[a.ID] = true
		}
	}
}

// settlePending retries the exact persisted sequence; it never restarts a run.
func (s *Supervisor) settlePending(ctx context.Context) {
	s.mu.Lock()
	entries := make([]*owned, 0, len(s.runs))
	for _, e := range s.runs {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		err := s.flushReports(ctx, e)
		e.mu.Unlock()
		s.handleRunError(e, err)
	}
}

func (s *Supervisor) flushReports(ctx context.Context, e *owned) error {
	for len(e.record.Pending) > 0 {
		t := e.record.Pending[0]
		var err error
		if reporter, ok := s.api.(interface {
			ReportForClaim(context.Context, string, string, string, Telemetry) error
		}); ok {
			err = reporter.ReportForClaim(ctx, e.record.RunID, s.daemonID, e.record.Generation, t)
		} else {
			err = s.api.Report(ctx, e.record.RunID, t)
		}
		if err != nil {
			return err
		}
		e.record.Pending = e.record.Pending[1:]
		if err := s.journal.Put(e.record); err != nil {
			return err
		}
	}
	return nil
}

// RefreshAccounts adds approved accounts without restarting the shared daemon.
// Removed accounts remain known locally so their tombstones can keep draining.
func (s *Supervisor) RefreshAccounts(accounts []EnrolledAccount, adapters []Adapter) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	configured := map[string]Adapter{}
	for name, a := range s.adapters {
		configured[name] = a
	}
	for _, a := range adapters {
		if a == nil || a.Name() == "" {
			return ErrScope
		}
		if _, ok := a.(AccountProber); !ok {
			return ErrScope
		}
		configured[a.Name()] = a
	}
	merged := append([]EnrolledAccount(nil), s.accounts...)
	for _, a := range accounts {
		if a.ID == "" || a.Key == "" || (configured[a.Harness] == nil && !a.DependencyBlocked) {
			return ErrScope
		}
		found := false
		for _, old := range merged {
			if old.ID == a.ID {
				if old.Key != a.Key || old.Harness != a.Harness {
					return ErrScope
				}
				found = true
				break
			}
			if old.Key == a.Key {
				return ErrScope
			}
		}
		if !found {
			merged = append(merged, a)
		} else {
			for i := range merged {
				if merged[i].ID != a.ID {
					continue
				}
				wasBlocked := merged[i].DependencyBlocked
				merged[i].DependencyBlocked = a.DependencyBlocked
				merged[i].PinReason = a.PinReason
				merged[i].PinFix = a.PinFix
				if wasBlocked && !a.DependencyBlocked {
					delete(s.blockedAccounts, a.ID)
					if s.harnessFailed != nil {
						delete(s.harnessFailed, a.ID)
					}
				}
			}
		}
		if a.DependencyBlocked {
			s.blockedAccounts[a.ID] = true
			if s.harnessFailed == nil {
				s.harnessFailed = map[string]bool{}
			}
			s.harnessFailed[a.ID] = true
		}
	}
	s.accounts = merged
	s.adapters = configured
	return nil
}

// RestartClaude replaces only the idle Claude adapter. Only live handles from
// this generation use that adapter. Historical ownership and pending accounting
// remain untouched and continue to reconcile independently of the replacement.
func (s *Supervisor) RestartClaude(ctx context.Context, adapter *ClaudeAdapter) error {
	if adapter == nil {
		return ErrScope
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.mu.Lock()
	accounts := append([]EnrolledAccount(nil), s.accounts...)
	entries := make([]*owned, 0, len(s.runs))
	for _, entry := range s.runs {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	for _, entry := range entries {
		entry.mu.Lock()
		claude := entry.record.AccountID == ""
		for _, account := range accounts {
			claude = claude || account.ID == entry.record.AccountID && account.Harness == Claude
		}
		active := claude && entry.record.Generation == s.generation && entry.process != nil && !entry.record.ExitObserved
		entry.mu.Unlock()
		if active {
			return ErrDraining
		}
	}
	adapter.Workspace = s.workspace
	if _, err := adapter.resolved(s.workspace); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A repin may install the adapter a pin block withheld at cold start, but
	// never one for a computer without an approved Claude enrollment.
	enrolled := false
	for _, account := range s.accounts {
		enrolled = enrolled || account.Harness == Claude
	}
	if s.closing || !enrolled {
		return ErrScope
	}
	s.adapters[Claude] = adapter
	for _, account := range accounts {
		if account.Harness == Claude {
			delete(s.probedAccounts, account.ID)
			delete(s.loginRequired, account.ID)
		}
	}
	return nil
}

// PinHealthMatches reports whether enrolled accounts already carry the same
// per-account pin block as the latest runtime classification.
func (s *Supervisor) PinHealthMatches(accounts []EnrolledAccount) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := make(map[string]EnrolledAccount, len(s.accounts))
	for _, account := range s.accounts {
		byID[account.ID] = account
	}
	for _, account := range accounts {
		old, ok := byID[account.ID]
		if !ok || old.DependencyBlocked != account.DependencyBlocked || old.PinReason != account.PinReason || old.PinFix != account.PinFix {
			return false
		}
	}
	return true
}
