// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"

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
	if req.AccountID != "" {
		found := false
		for _, a := range s.accounts {
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
	v := LifecycleStatus{DaemonID: s.daemonID, Generation: s.generation, State: "drained", ActiveRunIDs: []string{}, UnconfirmedRunIDs: []string{}, SettlementPendingRunIDs: []string{}, FencedAccountIDs: []string{}, VerificationResults: map[string]string{}}
	v.AllFenced, _ = s.readFence("")
	for _, a := range s.accounts {
		fenced, e := s.readFence(a.ID)
		if fenced || e != nil {
			v.FencedAccountIDs = append(v.FencedAccountIDs, a.ID)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.runs {
		e.mu.Lock()
		if accountID != "" && e.record.AccountID != accountID && e.record.AccountID != "" {
			e.mu.Unlock()
			continue
		}
		if e.record.State == "ownership_lost" && !e.record.ExitObserved {
			v.UnconfirmedRunIDs = append(v.UnconfirmedRunIDs, e.record.RunID)
		} else if e.process != nil && !e.record.ExitObserved {
			v.ActiveRunIDs = append(v.ActiveRunIDs, e.record.RunID)
		}
		if len(e.record.Pending) > 0 || e.record.SettlementGap {
			v.SettlementPendingRunIDs = append(v.SettlementPendingRunIDs, e.record.RunID)
		}
		if e.record.ExecutionMode == VerificationPurpose {
			state := e.record.State
			if !e.record.ExitObserved && (state == "completed" || state == "failed" || state == "cancelled") {
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

func (s *Supervisor) accountAvailable(account string) bool {
	if !s.dispatchAllowed(account) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.blockedAccounts[account] && !s.blockedAccounts[""]
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
		_ = s.flushReports(ctx, e)
		e.mu.Unlock()
	}
}

func (s *Supervisor) flushReports(ctx context.Context, e *owned) error {
	for len(e.record.Pending) > 0 {
		t := e.record.Pending[0]
		if err := s.api.Report(ctx, e.record.RunID, t); err != nil {
			return err
		}
		e.record.Pending = e.record.Pending[1:]
		if err := s.journal.Put(e.record); err != nil {
			return err
		}
	}
	return nil
}
