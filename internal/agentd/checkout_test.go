// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/localjournal"
)

func physicalTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func checkoutOwner(t *testing.T, tenant, principal, daemon, state string) []byte {
	t.Helper()
	raw, err := json.Marshal([]string{tenant, principal, daemon, state})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeCheckoutFile(t *testing.T, workspace, name string, raw []byte) {
	t.Helper()
	store, err := agentsetup.OpenStore(filepath.Join(workspace, ".aeon-agentd"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(name, raw, false); err != nil {
		t.Fatal(err)
	}
}

func readCheckoutFile(t *testing.T, workspace, name string) ([]byte, error) {
	t.Helper()
	store, err := agentsetup.OpenStore(filepath.Join(workspace, ".aeon-agentd"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	return store.Read(name, 1<<20)
}

func openProofJournal(t *testing.T, dir, daemon string) *localjournal.Journal[Record] {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := proofJournalConfig()
	cfg.Directory = dir
	cfg.Prefix = "aeon-agentd-" + daemon
	journal, err := localjournal.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

func confirmedProofRecord() Record {
	return Record{TenantID: "old-tenant", PrincipalID: "old-principal", RunID: "run-1", Generation: "old-gen", State: "completed", ExitObserved: true, LaunchState: launchAttempted, Sequence: 1}
}

func unconfirmedProofRecord() Record {
	return Record{TenantID: "old-tenant", PrincipalID: "old-principal", RunID: "run-1", Generation: "old-gen", State: "running", ExitObserved: false, LaunchState: launchAttempted, Sequence: 1, PID: 1}
}

func TestCheckoutHandoverProof(t *testing.T) {
	t.Run("confirmed journal hands over and audits both pairings", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		prior := filepath.Join(physicalTemp(t), "earlier")
		journal := openProofJournal(t, prior, "earlier")
		if err := journal.Put(confirmedProofRecord()); err != nil {
			t.Fatal(err)
		}
		before := checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior)
		writeCheckoutFile(t, s.workspace, "owner.json", before)
		if err := s.acquireCheckout("run"); err != nil {
			t.Fatal(err)
		}
		got, err := readCheckoutFile(t, s.workspace, "owner.json")
		if err != nil || string(got) != string(checkoutOwner(t, s.tenantID, s.principalID, s.daemonID, s.state.Path())) {
			t.Fatalf("owner after handover %s %v", got, err)
		}
		audit, err := readCheckoutFile(t, s.workspace, "handover.log")
		if err != nil || !strings.Contains(string(audit), "from_daemon=earlier") || !strings.Contains(string(audit), "to_daemon="+s.daemonID) || !strings.Contains(string(audit), "from_state="+prior) {
			t.Fatalf("audit %q %v", audit, err)
		}
	})

	t.Run("unconfirmed journal keeps the fence", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		prior := filepath.Join(physicalTemp(t), "earlier")
		journal := openProofJournal(t, prior, "earlier")
		if err := journal.Put(unconfirmedProofRecord()); err != nil {
			t.Fatal(err)
		}
		before := checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior)
		writeCheckoutFile(t, s.workspace, "owner.json", before)
		err := s.acquireCheckout("run")
		if !errors.Is(err, ErrProcessesUnconfirmed) || !errors.Is(err, errCheckoutHeldByEarlierPairing) {
			t.Fatalf("unconfirmed journal: %v", err)
		}
		got, readErr := readCheckoutFile(t, s.workspace, "owner.json")
		if readErr != nil || string(got) != string(before) {
			t.Fatalf("owner changed %s %v", got, readErr)
		}
		if _, logErr := readCheckoutFile(t, s.workspace, "handover.log"); !errors.Is(logErr, os.ErrNotExist) {
			t.Fatalf("audit written without proof: %v", logErr)
		}
	})

	t.Run("missing journal and missing directory refuse without creating them", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		prior := filepath.Join(physicalTemp(t), "earlier")
		if err := os.Mkdir(prior, 0o700); err != nil {
			t.Fatal(err)
		}
		before := checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior)
		writeCheckoutFile(t, s.workspace, "owner.json", before)
		err := s.acquireCheckout("run")
		if !errors.Is(err, ErrProcessesUnconfirmed) || !errors.Is(err, errCheckoutHeldByEarlierPairing) {
			t.Fatalf("missing journal: %v", err)
		}
		entries, readErr := os.ReadDir(prior)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("missing journal created files: %v %v", entries, readErr)
		}
		absent := filepath.Join(physicalTemp(t), "absent")
		absentOwner := checkoutOwner(t, "old-tenant", "old-principal", "earlier", absent)
		writeCheckoutFile(t, s.workspace, "owner.json", absentOwner)
		err = s.acquireCheckout("run")
		if !errors.Is(err, ErrProcessesUnconfirmed) || !errors.Is(err, errCheckoutHeldByEarlierPairing) {
			t.Fatalf("missing directory: %v", err)
		}
		if _, statErr := os.Stat(absent); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("missing directory was created: %v", statErr)
		}
	})

	t.Run("corrupt journal refuses and does not rewrite bytes", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		prior := filepath.Join(physicalTemp(t), "earlier")
		if err := os.Mkdir(prior, 0o700); err != nil {
			t.Fatal(err)
		}
		journalPath := filepath.Join(prior, "aeon-agentd-earlier.journal")
		corrupt := []byte("{not-a-journal\n")
		if err := os.WriteFile(journalPath, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		before := checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior)
		writeCheckoutFile(t, s.workspace, "owner.json", before)
		err := s.acquireCheckout("run")
		if !errors.Is(err, ErrProcessesUnconfirmed) || !errors.Is(err, errCheckoutHeldByEarlierPairing) {
			t.Fatalf("corrupt journal: %v", err)
		}
		got, readErr := os.ReadFile(journalPath)
		if readErr != nil || string(got) != string(corrupt) {
			t.Fatalf("corrupt journal rewritten %q %v", got, readErr)
		}
		owner, readErr := readCheckoutFile(t, s.workspace, "owner.json")
		if readErr != nil || string(owner) != string(before) {
			t.Fatalf("corrupt journal released owner %s %v", owner, readErr)
		}
	})

	t.Run("empty journal file is vacuous proof", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		prior := filepath.Join(physicalTemp(t), "earlier")
		if err := os.Mkdir(prior, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prior, "aeon-agentd-earlier.journal"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		writeCheckoutFile(t, s.workspace, "owner.json", checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior))
		if err := s.acquireCheckout("run"); err != nil {
			t.Fatal(err)
		}
		got, err := readCheckoutFile(t, s.workspace, "owner.json")
		if err != nil || string(got) != string(checkoutOwner(t, s.tenantID, s.principalID, s.daemonID, s.state.Path())) {
			t.Fatalf("empty journal did not hand over %s %v", got, err)
		}
	})

	t.Run("checkpoint without a journal file refuses", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		prior := filepath.Join(physicalTemp(t), "earlier")
		if err := os.Mkdir(prior, 0o700); err != nil {
			t.Fatal(err)
		}
		checkpoint := []byte(`{"version":7,"records":[]}`)
		if err := os.WriteFile(filepath.Join(prior, "aeon-agentd-earlier.checkpoint.json"), checkpoint, 0o600); err != nil {
			t.Fatal(err)
		}
		before := checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior)
		writeCheckoutFile(t, s.workspace, "owner.json", before)
		err := s.acquireCheckout("run")
		if !errors.Is(err, ErrProcessesUnconfirmed) || !errors.Is(err, errCheckoutHeldByEarlierPairing) {
			t.Fatalf("checkpoint only: %v", err)
		}
		got, readErr := readCheckoutFile(t, s.workspace, "owner.json")
		if readErr != nil || string(got) != string(before) {
			t.Fatalf("checkpoint only released the fence %s %v", got, readErr)
		}
	})

	t.Run("malformed owner stays generic", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		writeCheckoutFile(t, s.workspace, "owner.json", []byte("null"))
		err := s.acquireCheckout("run")
		if !errors.Is(err, ErrProcessesUnconfirmed) || errors.Is(err, errCheckoutHeldByEarlierPairing) {
			t.Fatalf("malformed owner: %v", err)
		}
		got, readErr := readCheckoutFile(t, s.workspace, "owner.json")
		if readErr != nil || string(got) != "null" {
			t.Fatalf("malformed owner rewritten %s %v", got, readErr)
		}
	})

	t.Run("same owner spacing rewrites without an audit", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		pretty, err := json.MarshalIndent([]string{s.tenantID, s.principalID, s.daemonID, s.state.Path()}, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		writeCheckoutFile(t, s.workspace, "owner.json", pretty)
		if err := s.acquireCheckout("run"); err != nil {
			t.Fatal(err)
		}
		got, err := readCheckoutFile(t, s.workspace, "owner.json")
		if err != nil || string(got) != string(checkoutOwner(t, s.tenantID, s.principalID, s.daemonID, s.state.Path())) {
			t.Fatalf("same owner %s %v", got, err)
		}
		if _, logErr := readCheckoutFile(t, s.workspace, "handover.log"); !errors.Is(logErr, os.ErrNotExist) {
			t.Fatalf("same owner wrote an audit: %v", logErr)
		}
	})

	t.Run("busy lock stays held and writes nothing", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		before := checkoutOwner(t, "old-tenant", "old-principal", "earlier", filepath.Join(physicalTemp(t), "earlier"))
		writeCheckoutFile(t, s.workspace, "owner.json", before)
		held, err := agentsetup.OpenStore(filepath.Join(s.workspace, ".aeon-agentd"), false)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		if err := held.Lock(); err != nil {
			t.Fatal(err)
		}
		err = s.acquireCheckout("run")
		if !errors.Is(err, agentsetup.ErrBusy) || errors.Is(err, errCheckoutHeldByEarlierPairing) {
			t.Fatalf("busy lock: %v", err)
		}
		got, readErr := readCheckoutFile(t, s.workspace, "owner.json")
		if readErr != nil || string(got) != string(before) {
			t.Fatalf("busy lock wrote owner %s %v", got, readErr)
		}
	})
}

func TestHandoverCheckoutReportsPairTime(t *testing.T) {
	root := physicalTemp(t)
	workspace := filepath.Join(root, "work")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	nextState := filepath.Join(root, "next-state")
	notice, err := HandoverCheckout(workspace, "tenant", "agent", "daemon", nextState)
	if err != nil || notice != "" {
		t.Fatalf("missing store %q %v", notice, err)
	}
	prior := filepath.Join(root, "earlier")
	journal := openProofJournal(t, prior, "earlier")
	if err := journal.Put(confirmedProofRecord()); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, workspace, "owner.json", checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior))
	notice, err = HandoverCheckout(workspace, "tenant", "agent", "daemon", nextState)
	if err != nil || notice != CheckoutHandedOverText {
		t.Fatalf("proved pair %q %v", notice, err)
	}
	notice, err = HandoverCheckout(workspace, "tenant", "agent", "daemon", nextState)
	if err != nil || notice != "" {
		t.Fatalf("second pair %q %v", notice, err)
	}
	heldOwner := checkoutOwner(t, "old-tenant", "old-principal", "earlier", filepath.Join(root, "unproved"))
	writeCheckoutFile(t, workspace, "owner.json", heldOwner)
	notice, err = HandoverCheckout(workspace, "tenant", "agent", "daemon", nextState)
	if err != nil || notice != CheckoutStillHeldText {
		t.Fatalf("unproved pair %q %v", notice, err)
	}
	got, readErr := readCheckoutFile(t, workspace, "owner.json")
	if readErr != nil || string(got) != string(heldOwner) {
		t.Fatalf("unproved pair wrote owner %s %v", got, readErr)
	}
	store, err := agentsetup.OpenStore(filepath.Join(workspace, ".aeon-agentd"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Lock(); err != nil {
		t.Fatal(err)
	}
	notice, err = HandoverCheckout(workspace, "tenant", "agent", "daemon", nextState)
	if err != nil || notice != CheckoutStillHeldText {
		t.Fatalf("busy pair %q %v", notice, err)
	}
	store.Unlock()
	writeCheckoutFile(t, workspace, "owner.json", []byte("null"))
	notice, err = HandoverCheckout(workspace, "tenant", "agent", "daemon", nextState)
	if err != nil || notice != "" {
		t.Fatalf("malformed pair %q %v", notice, err)
	}
	if got, readErr = readCheckoutFile(t, workspace, "owner.json"); readErr != nil || string(got) != "null" {
		t.Fatalf("malformed pair rewrote owner %s %v", got, readErr)
	}
	notice, err = HandoverCheckout(workspace, "tenant", "agent", "bad/daemon", nextState)
	if err != nil || notice != CheckoutStillHeldText {
		t.Fatalf("invalid identity %q %v", notice, err)
	}
}

func TestHandoverCheckoutBusySameOwnerSaysNothing(t *testing.T) {
	root := physicalTemp(t)
	workspace := filepath.Join(root, "work")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	nextState := filepath.Join(root, "next-state")
	hold := func(t *testing.T) {
		t.Helper()
		store, err := agentsetup.OpenStore(filepath.Join(workspace, ".aeon-agentd"), false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			store.Unlock()
			_ = store.Close()
		})
		if err := store.Lock(); err != nil {
			t.Fatal(err)
		}
	}
	quiet := func(t *testing.T, before []byte) {
		t.Helper()
		notice, err := HandoverCheckout(workspace, "tenant", "agent", "daemon", nextState)
		if err != nil || notice != "" {
			t.Fatalf("busy own checkout %q %v", notice, err)
		}
		got, readErr := readCheckoutFile(t, workspace, "owner.json")
		if before == nil {
			if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("busy empty checkout wrote owner %s %v", got, readErr)
			}
			return
		}
		if readErr != nil || string(got) != string(before) {
			t.Fatalf("busy checkout rewrote owner %s %v", got, readErr)
		}
	}
	t.Run("no record", func(t *testing.T) {
		store, err := agentsetup.OpenStore(filepath.Join(workspace, ".aeon-agentd"), true)
		if err != nil {
			t.Fatal(err)
		}
		_ = store.Close()
		hold(t)
		quiet(t, nil)
	})
	t.Run("same owner", func(t *testing.T) {
		owner := checkoutOwner(t, "tenant", "agent", "daemon", nextState)
		writeCheckoutFile(t, workspace, "owner.json", owner)
		hold(t)
		quiet(t, owner)
	})
	t.Run("same owner spaced", func(t *testing.T) {
		owner := []byte(fmt.Sprintf("[ \"tenant\" , \"agent\" , \"daemon\" , %q ]", nextState))
		writeCheckoutFile(t, workspace, "owner.json", owner)
		hold(t)
		quiet(t, owner)
	})
	t.Run("empty record", func(t *testing.T) {
		store, err := agentsetup.OpenStore(filepath.Join(workspace, ".aeon-agentd"), true)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Lock(); err != nil {
			t.Fatal(err)
		}
		if err := store.Write("owner.json", []byte("[]"), false); err != nil {
			t.Fatal(err)
		}
		store.Unlock()
		_ = store.Close()
		hold(t)
		quiet(t, []byte("[]"))
	})
}

func TestCheckoutHeldWaitReportsOnce(t *testing.T) {
	s, api, _ := testSupervisor(t)
	prior := filepath.Join(physicalTemp(t), "earlier")
	if err := os.Mkdir(prior, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, s.workspace, "owner.json", checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior))
	var diagnostics []string
	s.pollDiagnostic = func(reason string) { diagnostics = append(diagnostics, reason) }
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s.capacityNow = func() time.Time { return now }
	reporter := &waitReporter{fakeAPI: api, failFirst: true}
	s.api = reporter
	for range 3 {
		err := s.PollOnce(context.Background())
		if errors.Is(err, errCheckoutHeldByEarlierPairing) || errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatalf("poll published the checkout refusal: %v", err)
		}
	}
	held := 0
	for _, reason := range diagnostics {
		if reason == CheckoutHeldCause {
			held++
		}
	}
	if held != 1 {
		t.Fatalf("checkout diagnostic count %d in %v", held, diagnostics)
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.calls != 2 || len(reporter.reasons) != 1 || reporter.reasons[0] != api.run.ID+"="+CheckoutHeldCause {
		t.Fatalf("reports calls=%d reasons=%v", reporter.calls, reporter.reasons)
	}
	found := false
	for _, reason := range s.Lifecycle("").Diagnostics {
		if reason == CheckoutHeldCause {
			found = true
		}
	}
	if !found {
		t.Fatalf("status diagnostics %v", s.Lifecycle("").Diagnostics)
	}
}

func TestCheckoutHeldWaitReportsAgainAfterServerRestart(t *testing.T) {
	s, api, _ := testSupervisor(t)
	prior := filepath.Join(physicalTemp(t), "earlier")
	if err := os.Mkdir(prior, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, s.workspace, "owner.json", checkoutOwner(t, "old-tenant", "old-principal", "earlier", prior))
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s.capacityNow = func() time.Time { return now }
	reporter := &waitReporter{fakeAPI: api}
	s.api = reporter
	poll := func(wantCalls int, wantReasons ...string) {
		t.Helper()
		if err := s.PollOnce(context.Background()); errors.Is(err, errCheckoutHeldByEarlierPairing) || errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatalf("poll published the checkout refusal: %v", err)
		}
		reporter.mu.Lock()
		defer reporter.mu.Unlock()
		if reporter.calls != wantCalls || strings.Join(reporter.reasons, ",") != strings.Join(wantReasons, ",") {
			t.Fatalf("at %s reports calls=%d reasons=%v, want calls=%d reasons=%v", now.Format(time.TimeOnly), reporter.calls, reporter.reasons, wantCalls, wantReasons)
		}
	}
	held := api.run.ID + "=" + CheckoutHeldCause
	poll(1, held)
	// A server restart drops its in-memory reasons; the daemon still holds its own.
	reporter.mu.Lock()
	reporter.reasons = nil
	reporter.mu.Unlock()
	now = now.Add(daemonWaitRefresh - time.Second)
	poll(1)
	now = now.Add(time.Second)
	poll(2, held)
	now = now.Add(daemonWaitRefresh - time.Second)
	poll(2, held)
}

type waitReporter struct {
	*fakeAPI
	mu        sync.Mutex
	calls     int
	failFirst bool
	reasons   []string
}

func (a *waitReporter) ReportDaemonWait(_ context.Context, runID, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.failFirst && a.calls == 1 {
		return errors.New("report lost")
	}
	a.reasons = append(a.reasons, runID+"="+reason)
	return nil
}
