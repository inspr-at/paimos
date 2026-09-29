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
	HarnessErrors           map[string]string `json:"harness_errors,omitempty"`
	LoginRequired           bool              `json:"login_required"`
	VerificationUnavailable []string          `json:"verification_unavailable_account_ids"`
	Ready                   bool              `json:"ready"`
	DaemonID                string            `json:"daemon_id"`
	Generation              string            `json:"generation"`
	State                   string            `json:"state"`
	ActiveRunIDs            []string          `json:"active_run_ids"`
	UnconfirmedRunIDs       []string          `json:"unconfirmed_run_ids"`
	SettlementPendingRunIDs []string          `json:"settlement_pending_run_ids"`
	FencedAccountIDs        []string          `json:"fenced_account_ids"`
	AllFenced               bool              `json:"all_fenced"`
	VerificationResults     map[string]string `json:"verification_results"`
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
	v.Ready = len(s.accounts) > 0
	v.AllFenced, _ = s.readFence("")
	for _, a := range s.accounts {
		fenced, e := s.readFence(a.ID)
		if fenced || e != nil {
			v.FencedAccountIDs = append(v.FencedAccountIDs, a.ID)
		} else {
			issue := s.harnessHolds[a.Harness]
			if issue == "" {
				issue = s.dependencyErrors[a.Harness]
			}
			if issue != "" {
				v.HarnessErrors[a.Harness] = issue
				v.Ready = false
			}
			if !s.probedAccounts[a.ID] || s.blockedAccounts[a.ID] {
				v.Ready = false
			}
			if s.loginRequired[a.ID] && issue == "" {
				v.LoginRequired = true
			}
			if adapter, ok := s.adapters[a.Harness].(VerificationAdapter); !ok || !adapter.VerificationSupported() {
				v.VerificationUnavailable = append(v.VerificationUnavailable, a.ID)
			}
		}
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
	return v
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

func (s *Supervisor) accountAvailable(account string) bool {
	if !s.dispatchAllowed(account) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.accounts {
		if a.ID == account && (s.harnessHolds[a.Harness] != "" || s.dependencyErrors[a.Harness] != "") {
			return false
		}
	}
	return !s.blockedAccounts[account] && !s.blockedAccounts[""]
}

// SetHarnessHold pauses fresh work for one harness without changing durable
// lifecycle fences or touching processes. An empty reason releases the hold;
// a successful account probe is still required before dispatch resumes.
func (s *Supervisor) SetHarnessHold(harness, reason string) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.harnessHolds == nil {
		s.harnessHolds = map[string]string{}
	}
	if reason == "" {
		delete(s.harnessHolds, harness)
		return
	}
	s.harnessHolds[harness] = reason
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
		if a.ID == "" || a.Key == "" || configured[a.Harness] == nil {
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
	if s.closing || s.adapters[Claude] == nil {
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
