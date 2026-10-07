// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentpairing"
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

// A qualified server approval can meet an older/unqualified local adapter.
// Only verification fails; the normal account probe must still make it ready.
type unavailablePairingAdapter struct{ *pairingVerificationAdapter }

func (*unavailablePairingAdapter) VerificationSupported() bool { return false }

func TestPairingUnsupportedVerificationFailsAndComputerBecomesReady(t *testing.T) {
	f := newFixture(t)
	p := f.propose(agentd.Claude)
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	e := v.Enrollments[0]
	remote := agentd.NewRemote(origin, "aeon_"+v.RuntimePrefix+"_"+p.runtime)
	remote.Client.HTTP.Transport = handlerTransport{f.h}
	root := filepath.Join(physicalSetupTemp(t), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	adapter := &unavailablePairingAdapter{&pairingVerificationAdapter{family: agentd.Claude, key: e.AccountKey, account: e.AccountID}}
	s, err := agentd.NewSupervisor(t.Context(), agentd.Config{API: remote, DaemonID: *v.DaemonID, Workspace: physicalSetupTemp(t), StateRoot: root, Adapters: []agentd.Adapter{adapter}, Accounts: []agentd.EnrolledAccount{{ID: e.AccountID, Key: e.AccountKey, Harness: agentd.Claude}}, EstimatedUnits: map[string]int64{"requests": 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(t.Context())
	if err = s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	run, err := remote.GetRun(t.Context(), *e.VerificationRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !s.Lifecycle("").Ready || adapter.probes != 1 || adapter.starts != 0 {
		t.Fatal("verification deadlock or unsafe launch", run.Status)
	}
	if err = remote.RefuseVerification(t.Context(), run.ID, *v.DaemonID, "same-owner-retry", "adapter_unsupported"); err != nil {
		t.Fatal("idempotent refusal", err)
	}
	if err = remote.RefuseVerification(t.Context(), run.ID, "another-daemon", "test", "adapter_unsupported"); err == nil {
		t.Fatal("foreign daemon refused work")
	}
	var detail string
	var started bool
	if err = f.db.Admin.QueryRow(t.Context(), `SELECT verification_unavailable_reason,started_at IS NOT NULL FROM agent_runs WHERE id=$1`, run.ID).Scan(&detail, &started); err != nil {
		t.Fatal(err)
	}
	if detail != "adapter_unsupported" || started {
		t.Fatal("missing no-launch reason")
	}
	var view agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &view)
	if view.Enrollments[0].VerificationError != "verification_unavailable" || view.Enrollments[0].VerificationReason != "adapter_unsupported" {
		t.Fatal("computer row lost refusal cause")
	}
}

func TestLaterConnectOnlyApprovalCancelsQueuedVerification(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	first := f.approve(p, "one_per_harness")
	run := *first.Enrollments[0].VerificationRunID
	view := f.approve(p, "connect_only")
	if view.Enrollments[0].VerificationState != "cancelled" {
		t.Fatal("queued verification survived Connect only")
	}
	f.approve(p, "connect_only")
	var count int
	var state string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM agent_runs WHERE purpose='pairing_verification') FROM agent_runs WHERE id=$1`, run).Scan(&state, &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || state != "cancelled" {
		t.Fatal("approval minted or kept verification")
	}
	fresh := f.propose("claude")
	second := f.approve(fresh, "connect_only")
	if second.Enrollments[0].VerificationRunID != nil {
		t.Fatal("Verify unticked queued a run")
	}
}

func TestVerificationRefusalReleasesOnlyOwnedUnclaimedRun(t *testing.T) {
	for _, cause := range []string{"adapter_unsupported", "binding_incomplete", "local_binding_missing"} {
		t.Run(cause, func(t *testing.T) {
			f := newFixture(t)
			p := f.propose("claude")
			f.approve(p, "one_per_harness")
			v := f.redeem(p)
			e := v.Enrollments[0]
			key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
			f.probe(v, e, key, 200)
			f.reserve(v, e, key, 200)
			body := map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": []string{}, "verification_unavailable": cause}
			path := "/api/runs/" + *e.VerificationRunID + "/claim"
			body["verification_unavailable"] = "raw diagnostics must not be persisted"
			f.call("POST", path, body, false, key, 400)
			body["verification_unavailable"] = cause
			other := f.propose("claude")
			f.approve(other, "connect_only")
			ov := f.redeem(other)
			f.call("POST", path, body, false, "aeon_"+ov.RuntimePrefix+"_"+other.runtime, 403)
			f.call("POST", path, body, false, key, 200)
			f.call("POST", path, body, false, key, 200)
			var state, hold string
			var used, reserved int64
			var started bool
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,res.state,w.used,w.reserved,r.started_at IS NOT NULL FROM agent_runs r JOIN account_reservations res ON res.run_id=r.id JOIN account_allowance_windows w ON w.id=res.window_id WHERE r.id=$1`, *e.VerificationRunID).Scan(&state, &hold, &used, &reserved, &started); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || hold != "released" || used != 0 || reserved != 0 || started {
				t.Fatal("no-launch refusal changed usage or retained hold")
			}
		})
	}
}

func TestAddHarnessApprovalSupersedesOnlyQueuedVerification(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(fmt.Sprint("claimed=", claimed), func(t *testing.T) {
			f := newFixture(t)
			p := f.propose("claude")
			f.approve(p, "one_per_harness")
			v := f.redeem(p)
			e := v.Enrollments[0]
			key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
			f.probe(v, e, key, 200)
			ids := f.reserve(v, e, key, 200)
			if claimed {
				f.claim(v, e, key, ids, 200)
			}
			q := &proposal{id: uuid(t, f.db), device: nonce(), runtime: p.runtime, lifecycle: p.lifecycle, request: map[string]any{}}
			for k, x := range p.request {
				q.request[k] = x
			}
			q.request["request_id"], q.request["device_hash"] = q.id, hash(q.device)
			q.request["existing_computer_id"], q.request["existing_lifecycle_secret"] = *v.ComputerID, p.lifecycle
			q.request["accounts"] = []map[string]string{{"account_key": "claude-new", "harness": "claude", "label": "New account", "model_profile_id": f.profiles["claude"]}}
			f.submit(q)
			f.approve(q, "connect_only")
			var state string
			var held int64
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.status,w.reserved FROM agent_runs r JOIN account_allowance_windows w ON w.account_id=r.requested_account_id WHERE r.id=$1`, *e.VerificationRunID).Scan(&state, &held); err != nil {
				t.Fatal(err)
			}
			if claimed {
				if state != "starting" || held != 1 {
					t.Fatal("later approval disturbed claimed work")
				}
				f.call("POST", "/api/runs/"+*e.VerificationRunID+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": []string{}, "verification_unavailable": "binding_incomplete"}, false, key, 403)
			} else if state != "cancelled" || held != 0 {
				t.Fatal("later approval kept queued verification or its hold")
			}
		})
	}
}
