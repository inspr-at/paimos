// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
)

// Only the vendor is synthetic. Queue projection, account probing, routing,
// reservations, claim, harness registration and telemetry use the real HTTP
// handlers and isolated Postgres through Remote, with the paired runtime key.
func TestPairingSupervisorHTTPVerificationRoutesUnboundQueue(t *testing.T) {
	for _, family := range []string{agentd.Claude, agentd.Grok} {
		t.Run(family, func(t *testing.T) {
			f := newFixture(t)
			p := f.propose(family)
			f.approve(p, "one_per_harness")
			v := f.redeem(p)
			e := v.Enrollments[0]
			remote := agentd.NewRemote(origin, "aeon_"+v.RuntimePrefix+"_"+p.runtime)
			remote.Client.HTTP.Transport = handlerTransport{f.h}
			queued, err := remote.Queued(t.Context())
			if err != nil || len(queued) != 1 {
				t.Fatalf("genuine queue: count=%d error=%v", len(queued), err)
			}
			if queued[0].ID != *e.VerificationRunID || queued[0].AccountID != "" || queued[0].RequestedAccountID != e.AccountID {
				t.Fatal("fixture must exercise the genuine unbound queued verification")
			}
			workspace := physicalSetupTemp(t)
			stateRoot := filepath.Join(physicalSetupTemp(t), "state")
			if err := os.Mkdir(stateRoot, 0700); err != nil {
				t.Fatal(err)
			}
			adapter := &pairingVerificationAdapter{family: family, account: e.AccountID, key: e.AccountKey,
				queued: queued[0], workspace: workspace, process: &pairingVerificationProcess{done: make(chan struct{})}}
			s, err := agentd.NewSupervisor(t.Context(), agentd.Config{API: remote, DaemonID: *v.DaemonID,
				Workspace: workspace, StateRoot: stateRoot, Adapters: []agentd.Adapter{adapter},
				Accounts:       []agentd.EnrolledAccount{{ID: e.AccountID, Key: e.AccountKey, Harness: family}},
				EstimatedUnits: map[string]int64{"requests": 1}, HeartbeatInterval: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				adapter.process.exit()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				for len(s.Lifecycle("").ActiveRunIDs) != 0 && ctx.Err() == nil {
					time.Sleep(time.Millisecond)
				}
				if err := s.Close(ctx); err != nil {
					t.Errorf("close synthetic supervisor: %v", err)
				}
			})
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if adapter.starts != 1 || adapter.probes != 1 {
				t.Fatalf("starts/probes=%d/%d, want 1/1", adapter.starts, adapter.probes)
			}
			var window string
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT id::text FROM account_allowance_windows WHERE account_id=$1 AND pairing_verification`, e.AccountID).Scan(&window); err != nil {
				t.Fatal(err)
			}
			// Production settlement updates the same reservation on every
			// accepted sequence, including zero-usage started telemetry.
			assertWindowLedger(t, f, window, 0, 0)
			// Replay the original queue receipt while the synthetic child is live.
			if err := s.StartRun(t.Context(), queued[0]); err != nil || adapter.starts != 1 {
				t.Fatal("receipt replay duplicated or rejected an already owned run", err)
			}
			adapter.observe(agentd.AdapterEvent{Kind: "turn", TurnCountDelta: 1})
			adapter.process.exit()
			deadline := time.Now().Add(5 * time.Second)
			for {
				run, err := remote.GetRun(t.Context(), queued[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				if run.Status == "completed" {
					if run.AccountID != e.AccountID {
						t.Fatal("completed run lost the approved account binding")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("synthetic completion did not settle: %s", run.Status)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := s.PollOnce(t.Context()); err != nil || adapter.starts != 1 {
				t.Fatal("completed verification repeated", err)
			}
			assertWindowLedger(t, f, window, 1, 0)
			var windows, reservations, turns int
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM account_allowance_windows WHERE account_id=$1), (SELECT count(*) FROM account_reservations WHERE run_id=$2), (SELECT coalesce(sum(turn_count_delta),0) FROM run_telemetry WHERE run_id=$2)`, e.AccountID, queued[0].ID).Scan(&windows, &reservations, &turns); err != nil {
				t.Fatal(err)
			}
			if windows != 1 || reservations != 1 || turns != 1 {
				t.Fatalf("windows/reservations/turns=%d/%d/%d, want 1/1/1", windows, reservations, turns)
			}
			if st := s.Lifecycle(""); len(st.SettlementPendingRunIDs) != 0 || len(st.UnconfirmedRunIDs) != 0 {
				t.Fatal("completion left unconfirmed local settlement")
			}
		})
	}
}

type pairingVerificationAdapter struct {
	family, account, key, workspace string
	queued                          agentd.Run
	process                         *pairingVerificationProcess
	observe                         func(agentd.AdapterEvent)
	starts, probes                  int
}

func (a *pairingVerificationAdapter) Name() string              { return a.family }
func (*pairingVerificationAdapter) VerificationSupported() bool { return true }
func (a *pairingVerificationAdapter) Probe(_ context.Context, key string) bool {
	a.probes++
	return key == a.key
}
func (a *pairingVerificationAdapter) Start(_ context.Context, r agentd.StartRequest, observe func(agentd.AdapterEvent)) (agentd.Process, error) {
	// Independently enforce the strict adapter boundary: routing may fill only
	// AccountID, never weaken any of the server's immutable verification terms.
	want := a.queued
	want.AccountID = a.account
	if r.Run.AccountID == "" || !reflect.DeepEqual(r.Run, want) || r.AccountKey != a.key ||
		r.Run.Purpose != agentd.VerificationPurpose || r.Run.VerificationTask != agentd.VerificationTask ||
		r.Run.MaxDurationSeconds == nil || *r.Run.MaxDurationSeconds != 60 || r.Run.VerificationPolicy != "read_only" ||
		r.Run.RepositoryMutationAllowed == nil || *r.Run.RepositoryMutationAllowed || r.Tools != nil || r.Prompt != agentd.VerificationTask {
		return nil, errors.New("synthetic adapter rejected incomplete or changed verification binding")
	}
	rel, err := filepath.Rel(a.workspace, r.Workspace)
	if err != nil || !filepath.IsAbs(r.Workspace) || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("synthetic adapter rejected repository workspace")
	}
	a.starts++
	a.observe = observe
	return a.process, nil
}

type pairingVerificationProcess struct {
	done chan struct{}
	once sync.Once
}

func (*pairingVerificationProcess) PID() int      { return 0 } // No operating system process exists.
func (p *pairingVerificationProcess) Wait() error { <-p.done; return nil }
func (*pairingVerificationProcess) Control(context.Context, string, string) error {
	return agentd.ErrUnsupported
}
func (p *pairingVerificationProcess) Stop(context.Context) error { p.exit(); return nil }
func (p *pairingVerificationProcess) exit()                      { p.once.Do(func() { close(p.done) }) }
