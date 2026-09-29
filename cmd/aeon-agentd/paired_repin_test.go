// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/localjournal"
)

type repinSupervisor struct {
	holds           map[string]string
	reasons         map[string]string
	restartErr      error
	restarts, polls int
}

func (s *repinSupervisor) SetHarnessHoldWithReason(harness, code, reason string) {
	if s.holds == nil {
		s.holds = map[string]string{}
		s.reasons = map[string]string{}
	}
	s.holds[harness] = reason
	s.reasons[harness] = code
}
func (*repinSupervisor) PinHealthMatches([]agentd.EnrolledAccount) bool                   { return true }
func (*repinSupervisor) RefreshAccounts([]agentd.EnrolledAccount, []agentd.Adapter) error { return nil }
func (s *repinSupervisor) RestartClaude(context.Context, *agentd.ClaudeAdapter) error {
	s.restarts++
	return s.restartErr
}
func (s *repinSupervisor) PollOnce(context.Context) error { s.polls++; return nil }

func appliedRepinFixture(t *testing.T) (*agentsetup.Engine, agentsetup.RuntimeConfig, agentsetup.RuntimeConfig) {
	t.Helper()
	e, pins := repinCommandFixture(t)
	old, err := agentsetup.ReadRuntimeConfig(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := e.PrepareClaudeRepin(t.Context(), agentsetup.Discovery{}, pins)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyClaudeRepin(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	next, err := agentsetup.ReadRuntimeConfig(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	return e, old, next
}

func TestPendingOrFailedClaudeRepinKeepsPollingAndRetries(t *testing.T) {
	for _, restartErr := range []error{agentd.ErrDraining, errors.New("fixture adapter failure")} {
		e, old, next := appliedRepinFixture(t)
		s := &repinSupervisor{restartErr: restartErr}
		current, err := pollPairedRuntime(t.Context(), s, e.Store.Path(), old, next)
		if err != nil || current.ClaudeRepinID != old.ClaudeRepinID || s.polls != 1 || s.holds[agentd.Claude] == "" || len(s.holds) != 1 {
			t.Fatal("pending repin stopped polling or lost its Claude hold", err)
		}
		wantReason := "dependency_invalid"
		if errors.Is(restartErr, agentd.ErrDraining) {
			wantReason = "repin_pending"
		}
		if s.reasons[agentd.Claude] != wantReason {
			t.Fatal("repin wait classified as fault")
		}
		if applied, err := agentsetup.ClaudeRepinApplied(e.Store, next.ClaudeRepinID); err != nil || applied {
			t.Fatal("busy/failed adapter falsely acknowledged", err)
		}
		s.restartErr = nil
		current, err = pollPairedRuntime(t.Context(), s, e.Store.Path(), current, next)
		if err != nil || current.ClaudeRepinID != next.ClaudeRepinID || s.holds[agentd.Claude] != "" || s.polls != 2 || s.restarts != 2 {
			t.Fatal("pending repin did not resume", err)
		}
		if applied, err := agentsetup.ClaudeRepinApplied(e.Store, next.ClaudeRepinID); err != nil || !applied {
			t.Fatal("applied repin lacks acknowledgement", err)
		}
		if _, err := pollPairedRuntime(t.Context(), s, e.Store.Path(), current, next); err != nil || s.restarts != 2 {
			t.Fatal("acknowledgement restarted the adapter twice", err)
		}
	}
}

func TestBrokenClaudeDependencyKeepsPollingAndRecovers(t *testing.T) {
	for _, pending := range []bool{false, true} {
		e, old, next := appliedRepinFixture(t)
		current := next
		if pending {
			current = old
		}
		if err := os.Rename(next.ClaudeSDKPath, next.ClaudeSDKPath+".retired"); err != nil {
			t.Fatal(err)
		}
		s := &repinSupervisor{}
		current, err := pollPairedRuntime(t.Context(), s, e.Store.Path(), current, next)
		if err != nil || s.polls != 1 || s.restarts != 0 || !strings.Contains(s.holds[agentd.Claude], "repin") {
			t.Fatal("mid-run dependency failure stopped polling or was hidden", err)
		}
		if err := os.Rename(next.ClaudeSDKPath+".retired", next.ClaudeSDKPath); err != nil {
			t.Fatal(err)
		}
		if _, err := pollPairedRuntime(t.Context(), s, e.Store.Path(), current, next); err != nil || s.holds[agentd.Claude] != "" || s.polls != 2 {
			t.Fatal("repaired dependency did not recover", err)
		}
	}
}

type repinIdentityAPI struct {
	agentd.API
	c agentsetup.RuntimeConfig
}

func (a repinIdentityAPI) Identity(context.Context) (string, string, error) {
	return a.c.TenantID, a.c.PrincipalID, nil
}

func TestColdRepinAcknowledgesLoadedPinsAndPreservesPriorOwnership(t *testing.T) {
	e, _, c := appliedRepinFixture(t)
	stateRoot := filepath.Join(e.Store.Path(), "daemon")
	store, err := agentsetup.OpenStore(stateRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	j, err := localjournal.Open(localjournal.Config[agentd.Record]{Directory: stateRoot, Prefix: "aeon-agentd-" + c.DaemonID, Version: 2, MaxBytes: 4 << 20, MaxRecords: 4096, Key: func(r agentd.Record) (string, error) { return r.RunID, nil }, Validate: func(agentd.Record) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Put(agentd.Record{TenantID: c.TenantID, PrincipalID: c.PrincipalID, AccountID: c.Accounts[0].AccountID, RunID: "prior-run", Generation: "prior-generation", State: "running"}); err != nil {
		t.Fatal(err)
	}
	accounts, adapters, err := pairedAdapters(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := agentd.NewSupervisor(t.Context(), agentd.Config{API: repinIdentityAPI{c: c}, StateRoot: stateRoot, DaemonID: c.DaemonID, Workspace: c.Workspace, Accounts: accounts, Adapters: adapters})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(t.Context())
	if _, err := refreshPairedRuntime(t.Context(), s, e.Store.Path(), c, c); err != nil {
		t.Fatal(err)
	}
	if applied, err := agentsetup.ClaudeRepinApplied(e.Store, c.ClaudeRepinID); err != nil || !applied {
		t.Fatal("cold repin was not acknowledged", err)
	}
	status := s.Lifecycle("")
	if len(status.UnconfirmedRunIDs) != 1 || status.UnconfirmedRunIDs[0] != "prior-run" || status.State != "unconfirmed" || len(status.HarnessErrors) != 0 {
		t.Fatal("repin lost prior process evidence", status)
	}
}

func TestRepinReceiptFailureHoldsOnlyClaudeAndResumesWithoutRestart(t *testing.T) {
	e, _, c := appliedRepinFixture(t)
	stale := c
	stale.ClaudeRepinID = "55555555-5555-4555-8555-555555555555"
	raw, _ := json.Marshal(stale)
	if err := e.Store.Write(agentsetup.RuntimeName, raw, false); err != nil {
		t.Fatal(err)
	}
	s := &repinSupervisor{}
	if _, err := pollPairedRuntime(t.Context(), s, e.Store.Path(), c, c); err != nil || s.polls != 1 || s.restarts != 0 || !strings.Contains(s.holds[agentd.Claude], "acknowledgement") {
		t.Fatal("receipt failure not visible/resumable", err)
	}
	raw, _ = json.Marshal(c)
	if err := e.Store.Write(agentsetup.RuntimeName, raw, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pollPairedRuntime(t.Context(), s, e.Store.Path(), c, c); err != nil || s.polls != 2 || s.restarts != 0 || s.holds[agentd.Claude] != "" {
		t.Fatal("receipt retry restarted adapter or retained hold", err)
	}
}

func TestRepinDoesNotPermitIdentityChanges(t *testing.T) {
	e, old, next := appliedRepinFixture(t)
	next.PrincipalID = "55555555-5555-4555-8555-555555555555"
	s := &repinSupervisor{}
	if _, err := pollPairedRuntime(t.Context(), s, e.Store.Path(), old, next); err == nil || s.polls != 0 || s.restarts != 0 {
		t.Fatal("repin bypassed identity binding")
	}
}

func TestLocalStatusPreservesHarnessDetails(t *testing.T) {
	detail := agentsetup.HarnessDetail{State: "blocked", Reason: "pin_missing", Fix: agentsetup.RecoveryFix("codex", "pin_missing")}
	local := localStatus(agentd.LifecycleStatus{Ready: true, HarnessDetails: map[string]agentsetup.HarnessDetail{"codex": detail}})
	if !local.Ready || local.HarnessDetails["codex"] != detail {
		t.Fatal("CLI status discarded harness reason/fix")
	}
}

func TestUnavailableClaudeExecutableReportsRestoreInsteadOfRepin(t *testing.T) {
	for _, pending := range []bool{false, true} {
		e, old, next := appliedRepinFixture(t)
		current := next
		if pending {
			current = old
		}
		var path string
		for _, a := range next.Accounts {
			if a.Harness == agentd.Claude {
				path = a.Path
			}
		}
		if path == "" {
			t.Fatal("Claude fixture missing")
		}
		if err := os.Rename(path, path+".retired"); err != nil {
			t.Fatal(err)
		}
		s := &repinSupervisor{}
		_, err := pollPairedRuntime(t.Context(), s, e.Store.Path(), current, next)
		if err != nil || s.polls != 1 || s.reasons[agentd.Claude] != "cli_unavailable" {
			t.Fatal("executable failure misclassified or stopped other polling", err)
		}
	}
}
