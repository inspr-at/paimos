// SPDX-License-Identifier: AGPL-3.0-only

package nativerecovery

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
)

type inputAuthority struct{}

func (inputAuthority) RequireWrite(context.Context, nativecontract.Binding, nativereceipts.Intent) error {
	return nil
}

type inputTransport struct {
	identity         nativecontract.NativeIdentity
	now              func() time.Time
	writes           atomic.Int32
	entered, release chan struct{}
	crash            bool
}

func (p *inputTransport) Send(_ context.Context, in nativecontract.Input) (nativecontract.NativeEvidence, error) {
	p.writes.Add(1)
	close(p.entered)
	<-p.release
	if p.crash {
		panic("fixture crash before input acknowledgement")
	}
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", RequestID: in.RequestID, InputID: in.InputID, SourceSequence: 1, ObservedAt: p.now()}, nil
}
func (p *inputTransport) Identity() nativecontract.NativeIdentity { return p.identity }
func (*inputTransport) Capabilities() nativecontract.Capabilities {
	return nativecontract.Capabilities{}
}
func (*inputTransport) Events() <-chan nativecontract.ProviderEvent { return nil }
func (*inputTransport) Interrupt(context.Context, nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	panic("unexpected native interrupt")
}
func (*inputTransport) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	panic("old approval must never be replayed")
}
func (*inputTransport) Close(context.Context) (nativecontract.ExitEvidence, error) {
	panic("saved process must never be signalled")
}
func (*inputTransport) Wait(context.Context) (nativecontract.ExitEvidence, error) {
	panic("recovery uses qualified owner provenance")
}

func reopenReceipts(t *testing.T, f *recoveryFixture) {
	t.Helper()
	if err := f.receipts.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.receipts, err = nativereceipts.Open(f.receiptPath, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.restart(t)
}

// Risk: resume mistakes an ack for exactly-once external effects and reinjects
// input or old approval callbacks. Before/after-ack barriers retain P3a's exact
// durable metadata; an automatic continuation cannot cross that uncertainty.
func TestInputAckCrashKeepsEffectsVisibleAndNeverReinjects(t *testing.T) {
	for _, beforeAck := range []bool{true, false} {
		name := "after_ack"
		if beforeAck {
			name = "before_ack"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, true)
			f.startOld(t)
			in := nativecontract.Input{RequestID: uuid.NewString(), ExpectedBinding: f.oldBinding, InputID: uuid.NewString(), Mode: "now", Text: "authorized fixture input", ExpiresAt: f.now.Add(time.Hour)}
			op := uuid.NewString()
			transport := &inputTransport{identity: f.snapshot.Writer.Identity, now: func() time.Time { return f.now }, entered: make(chan struct{}), release: make(chan struct{}), crash: beforeAck}
			writer := &nativereceipts.InputWriter{Journal: f.receipts, Authority: inputAuthority{}, Transport: transport}
			done, crashed := make(chan struct{}), make(chan any, 1)
			var receipt nativecontract.Receipt
			var sendErr error
			go func() {
				defer close(done)
				defer func() { crashed <- recover() }()
				receipt, sendErr = writer.Send(t.Context(), f.actor.PrincipalID, op, in)
			}()
			await(t, transport.entered)
			marker, err := f.receipts.Get(f.oldBinding, op)
			if err != nil || marker.State != "write_started" || transport.writes.Load() != 1 {
				t.Fatalf("ack barrier lacks possible write: %+v, %v", marker, err)
			}
			close(transport.release)
			await(t, done)
			panicValue := <-crashed
			if beforeAck && panicValue != "fixture crash before input acknowledgement" || !beforeAck && (panicValue != nil || sendErr != nil || receipt.State != "write_acknowledged") {
				t.Fatalf("wrong ack outcome: %v, %+v, %v", panicValue, receipt, sendErr)
			}
			reopenReceipts(t, f)
			f.absent = false
			status, err := f.coordinator.Reconcile(t.Context(), f.oldBinding, DaemonLost)
			reason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
			state := "write_acknowledged"
			if beforeAck {
				state = "uncertain"
			}
			if !status.ReconciliationRequired || len(status.Effects.Items) != 1 || status.Effects.Items[0].State != state || f.held(t) != 1 {
				t.Fatalf("crash erased effect/occupancy: %+v", status)
			}
			writer.Journal = f.receipts
			_, err = writer.Send(t.Context(), f.actor.PrincipalID, op, in)
			reason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
			f.absent = true
			f.snapshot.Policy.Continuation = "bounded_preapproved"
			_, err = f.coordinator.Resume(t.Context(), f.actor, f.intent("approved_restart"))
			reason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
			manual := f.intent("person_continue")
			_, err = f.coordinator.Resume(t.Context(), f.actor, manual)
			reason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
			// Simulate a CURRENT owner reconciliation tied to the exact visible
			// effect challenge, not a caller-provided approval or old callback.
			effects, err := f.coordinator.effects(f.oldBinding)
			if err != nil {
				t.Fatal(err)
			}
			f.prepareEdit = func(s *Successor) { s.ReconciledEffectsDigest = effects.Digest }
			status, err = f.coordinator.Resume(t.Context(), f.actor, manual)
			if err != nil || status.State != "resumed" || !status.ReconciliationRequired || f.spawns != 1 || transport.writes.Load() != 1 || f.approvalLive || f.controllerLive {
				t.Fatalf("manual reconciliation lost uncertainty or replayed: %+v, %v", status, err)
			}
		})
	}
}

type persistenceSink struct {
	now              func() time.Time
	entered, release chan struct{}
	crashAfterCommit bool
	messages         map[string]string
	calls            int
}

func (s *persistenceSink) Persist(_ context.Context, in nativecontract.FinalDelivery) (nativecontract.FinalReceipt, error) {
	s.calls++
	if s.messages == nil {
		s.messages = map[string]string{}
	}
	if s.crashAfterCommit {
		s.messages[in.FinalID] = uuid.NewString()
	}
	if s.entered != nil {
		close(s.entered)
		<-s.release
		panic("fixture final publication crash")
	}
	id, ok := s.messages[in.FinalID]
	if !ok {
		id = uuid.NewString()
		s.messages[in.FinalID] = id
	}
	return nativecontract.FinalReceipt{FinalID: in.FinalID, State: "persisted", MessageID: &id, UpdatedAt: s.now()}, nil
}

func retainFinal(t *testing.T, f *recoveryFixture) nativecontract.FinalPublish {
	t.Helper()
	in := nativecontract.Input{RequestID: uuid.NewString(), ExpectedBinding: f.oldBinding, InputID: uuid.NewString(), Mode: "now", Text: "private input", ExpiresAt: f.now.Add(time.Hour)}
	b := cloneBinding(f.oldBinding)
	op := uuid.NewString()
	digest, err := nativereceipts.Digest(in)
	if err != nil {
		t.Fatal(err)
	}
	r := nativecontract.Receipt{RequestID: in.RequestID, OperationID: op, Binding: &b, Operation: "input", State: "accepted", InputID: &in.InputID, SourceSequence: "0", UpdatedAt: f.now}
	_, err = f.receipts.Accept(t.Context(), nativereceipts.Intent{Receipt: r, ActorID: f.actor.PrincipalID, PayloadSHA256: digest, ExpiresAt: in.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.receipts.BeginWrite(t.Context(), b, op); err != nil {
		t.Fatal(err)
	}
	for i, stage := range []string{"write_acknowledged", "turn_started", "turn_completed"} {
		r, err = f.receipts.Observe(t.Context(), b, op, nativecontract.NativeEvidence{Stage: stage, InputID: in.InputID, RequestID: in.RequestID, TurnRef: "qualified-turn", SourceSequence: uint64(i + 1), ObservedAt: f.now})
		if err != nil {
			t.Fatal(err)
		}
	}
	grant := nativecontract.FinalDrainGrant{GrantID: uuid.NewString(), Binding: b, ConversationRef: f.snapshot.History.ConversationRef, ExpiresAt: f.now.Add(time.Hour), MaximumFinals: 64, MaximumBytes: 4 << 20}
	delivery := nativecontract.FinalPublish{RequestID: uuid.NewString(), ExpectedBinding: b, FinalID: uuid.NewString(), InputID: in.InputID, TurnRef: "qualified-turn", Text: "approved explicit final", DrainGrantID: grant.GrantID}
	_, err = f.receipts.RetainFinal(t.Context(), delivery, grant, nativereceipts.RetentionApproval{OwnerPersonID: f.snapshot.OwnerPersonID, Approved: true, Until: grant.ExpiresAt}, r)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

// Risk: final persistence/lost ack is mistaken for child exit or lost final text.
// Barriers on both sides of the durable sink commit retain the actual final and
// shared ledger fixture; replay uses the same ID and no native write authority.
func TestFinalPersistenceCrashRetainsOutboxAndUncertainSlot(t *testing.T) {
	for _, afterCommit := range []bool{false, true} {
		name := "before_persistence"
		if afterCommit {
			name = "after_persistence"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false)
			f.startOld(t)
			f.absent = false
			delivery := retainFinal(t, f)
			sink := &persistenceSink{now: func() time.Time { return f.now }, entered: make(chan struct{}), release: make(chan struct{}), crashAfterCommit: afterCommit}
			done, crashed := make(chan struct{}), make(chan any, 1)
			go func() {
				defer close(done)
				defer func() { crashed <- recover() }()
				_, _ = f.receipts.RetryFinal(t.Context(), delivery.FinalID, sink)
			}()
			await(t, sink.entered)
			if got := f.receipts.FinalStatuses(); len(got) != 1 || got[0].State != "pending" || f.held(t) != 1 || len(sink.messages) != map[bool]int{false: 0, true: 1}[afterCommit] {
				t.Fatalf("persistence barrier: %+v", got)
			}
			close(sink.release)
			await(t, done)
			if got := <-crashed; got != "fixture final publication crash" {
				t.Fatalf("wrong persistence crash: %v", got)
			}
			f.now = f.now.Add(2 * time.Second) // injected clock, no retry sleep
			reopenReceipts(t, f)
			status, err := f.coordinator.Reconcile(t.Context(), f.oldBinding, DaemonLost)
			reason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
			if status.PendingFinals != 1 || f.held(t) != 1 || f.spawns != 0 {
				t.Fatal("final persistence released uncertainty or lost the outbox")
			}
			sink.entered, sink.crashAfterCommit = nil, false
			final, err := f.receipts.RetryFinal(t.Context(), delivery.FinalID, sink)
			if err != nil || final.State != "persisted" || len(sink.messages) != 1 || sink.calls != 2 || f.held(t) != 1 || f.spawns != 0 {
				t.Fatalf("idempotent final-only replay: %+v, %v", final, err)
			}
			f.absent = true
			status, err = f.coordinator.Reconcile(t.Context(), f.oldBinding, ChildExited)
			if err != nil || !status.ExitConfirmed || status.PendingFinals != 0 || f.held(t) != 0 || f.spawns != 0 {
				t.Fatalf("exit independent of final persistence: %+v, %v", status, err)
			}
		})
	}
}
