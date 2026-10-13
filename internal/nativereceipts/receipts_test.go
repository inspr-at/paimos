// SPDX-License-Identifier: AGPL-3.0-only

package nativereceipts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type testAuthority struct{ denied bool }

func (a *testAuthority) RequireWrite(context.Context, nativecontract.Binding, Intent) error {
	if a.denied {
		return Fail(nativecontract.ReasonPermissionRevoked)
	}
	return nil
}

type testTransport struct {
	identity         nativecontract.NativeIdentity
	writes           atomic.Int32
	entered, release chan struct{}
	crash            bool
	now              func() time.Time
}

func (f *testTransport) Send(_ context.Context, in nativecontract.Input) (nativecontract.NativeEvidence, error) {
	f.writes.Add(1)
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	if f.crash {
		panic("fixture crash before native acknowledgement")
	}
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", RequestID: in.RequestID, InputID: in.InputID, SourceSequence: 1, ObservedAt: f.now()}, nil
}
func (f *testTransport) Identity() nativecontract.NativeIdentity { return f.identity }
func (*testTransport) Capabilities() nativecontract.Capabilities {
	return nativecontract.Capabilities{}
}
func (*testTransport) Events() <-chan nativecontract.ProviderEvent { return nil }
func (*testTransport) Interrupt(context.Context, nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	panic("unexpected interrupt")
}
func (*testTransport) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	panic("unexpected decision")
}
func (*testTransport) Close(context.Context) (nativecontract.ExitEvidence, error) {
	return nativecontract.ExitEvidence{}, nil
}
func (*testTransport) Wait(context.Context) (nativecontract.ExitEvidence, error) {
	return nativecontract.ExitEvidence{}, nil
}

func testBinding() nativecontract.Binding {
	return nativecontract.Binding{TenantID: uuid.NewString(), ProjectID: uuid.NewString(), OwnedSessionID: uuid.NewString(), HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: "fixture-daemon", DaemonGeneration: "fixture-generation", BindingRevision: 1}
}
func testInput(b nativecontract.Binding, now time.Time) nativecontract.Input {
	return nativecontract.Input{RequestID: uuid.NewString(), ExpectedBinding: b, InputID: uuid.NewString(), Mode: "now", Text: "private prompt canary", ExpiresAt: now.Add(time.Hour)}
}
func checkReason(t *testing.T, err error, reason nativecontract.Reason) {
	t.Helper()
	var fault *Error
	if !errors.As(err, &fault) || fault.Reason != reason {
		t.Fatalf("reason: got %v, want %s", err, reason)
	}
}
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-t.Context().Done():
		t.Fatal("barrier did not complete")
	}
}
func openStore(t *testing.T, path string, now func() time.Time) *Store {
	t.Helper()
	s, err := Open(path, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

// Risk: a crash after a possible stdin write but before its correlated ack can
// make a RAM relay resend an input. The barrier proves the marker existed before
// the fake transport touched stdin, and recovery never permits another write.
func TestCrashBeforeNativeAckHoldsInputAndReplaysOnlyMetadata(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "receipts")
	s := openStore(t, path, clock)
	b := testBinding()
	in := testInput(b, now)
	op, actor := uuid.NewString(), uuid.NewString()
	transport := &testTransport{identity: nativecontract.NativeIdentity{RuntimeEpoch: b.RuntimeEpoch, ProcessIdentityDigest: strings.Repeat("a", 64)}, now: clock, entered: make(chan struct{}), release: make(chan struct{}), crash: true}
	writer := &InputWriter{Journal: s, Authority: &testAuthority{}, Transport: transport}
	done := make(chan struct{})
	panicked := make(chan any, 1)
	go func() {
		defer close(done)
		defer func() { panicked <- recover() }()
		_, _ = writer.Send(t.Context(), actor, op, in)
	}()
	await(t, transport.entered)
	r, err := s.Get(b, op)
	if err != nil || r.State != "write_started" || transport.writes.Load() != 1 {
		t.Fatalf("pre-ack barrier: %+v %v", r, err)
	}
	raw, err := os.ReadFile(filepath.Join(path, "receipts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), in.Text) || !strings.Contains(string(raw), "write_started") {
		t.Fatal("journal contains prompt or lacks pre-I/O marker")
	}
	close(transport.release)
	await(t, done)
	if got := <-panicked; got != "fixture crash before native acknowledgement" {
		t.Fatalf("wrong crash reason: %v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, path, clock)
	r, err = s.Get(b, op)
	if err != nil || r.State != "uncertain" || r.Reason == nil || *r.Reason != nativecontract.ReasonInputOutcomeUnconfirmed {
		t.Fatalf("recovery: %+v %v", r, err)
	}
	writer.Journal = s
	_, err = writer.Send(t.Context(), actor, op, in)
	checkReason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
	// Neither fresh operation/request IDs nor a different prompt evade the hold.
	in.RequestID = uuid.NewString()
	_, err = writer.Send(t.Context(), actor, uuid.NewString(), in)
	checkReason(t, err, nativecontract.ReasonIdentityMismatch)
	if transport.writes.Load() != 1 {
		t.Fatal("uncertain input was automatically reinjected")
	}
	reporter := &testReporter{}
	count, err := s.FlushReports(t.Context(), b, 100, reporter)
	if err != nil || count != 3 || !reflect.DeepEqual(reporter.states, []string{"accepted", "write_started", "uncertain"}) {
		t.Fatalf("metadata replay: %d %v %v", count, reporter.states, err)
	}
	if transport.writes.Load() != 1 {
		t.Fatal("receipt replay wrote native stdin")
	}
}

type testReporter struct {
	states []string
	lose   bool
}

func (f *testReporter) Report(_ context.Context, r nativecontract.Receipt) (nativecontract.Receipt, error) {
	f.states = append(f.states, r.State)
	if f.lose {
		f.lose = false
		return nativecontract.Receipt{}, errors.New("fixture API ack lost")
	}
	return r, nil
}

func completedInput(t *testing.T, s *Store, b nativecontract.Binding, now time.Time) (nativecontract.Input, nativecontract.Receipt) {
	t.Helper()
	in := testInput(b, now)
	op, actor := uuid.NewString(), uuid.NewString()
	tr := &testTransport{identity: nativecontract.NativeIdentity{RuntimeEpoch: b.RuntimeEpoch, ProcessIdentityDigest: strings.Repeat("a", 64)}, now: s.now}
	w := &InputWriter{Journal: s, Authority: &testAuthority{}, Transport: tr}
	r, err := w.Send(t.Context(), actor, op, in)
	if err != nil || r.State != "write_acknowledged" {
		t.Fatalf("input: %+v %v", r, err)
	}
	for i, stage := range []string{"turn_started", "turn_completed"} {
		r, err = s.Observe(t.Context(), b, op, nativecontract.NativeEvidence{Stage: stage, InputID: in.InputID, RequestID: in.RequestID, TurnRef: "opaque-turn", SourceSequence: uint64(i + 2), ObservedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	return in, r
}

func eligibleFinal(b nativecontract.Binding, in nativecontract.Input, now time.Time) (nativecontract.FinalPublish, nativecontract.FinalDrainGrant, RetentionApproval) {
	grant := nativecontract.FinalDrainGrant{GrantID: uuid.NewString(), Binding: b, ConversationRef: "opaque-conversation", ExpiresAt: now.Add(24 * time.Hour), MaximumFinals: 64, MaximumBytes: MaxFinalBytes}
	f := nativecontract.FinalPublish{RequestID: uuid.NewString(), ExpectedBinding: b, FinalID: uuid.NewString(), InputID: in.InputID, TurnRef: "opaque-turn", Text: "approved explicit final canary", DrainGrantID: grant.GrantID}
	a := RetentionApproval{OwnerPersonID: uuid.NewString(), Approved: true, Until: grant.ExpiresAt}
	return f, grant, a
}

type finalSink struct {
	now                                func() time.Time
	ids                                map[string]string
	calls                              int
	lose, unavailable, reject, wrongID bool
}

func (f *finalSink) Persist(_ context.Context, in nativecontract.FinalDelivery) (nativecontract.FinalReceipt, error) {
	f.calls++
	if f.unavailable {
		return nativecontract.FinalReceipt{}, errors.New("fixture API outage")
	}
	if f.reject {
		r := nativecontract.ReasonPermissionRevoked
		return nativecontract.FinalReceipt{FinalID: in.FinalID, State: "rejected", Reason: &r, UpdatedAt: f.now()}, nil
	}
	if f.ids == nil {
		f.ids = map[string]string{}
	}
	id, exists := f.ids[in.FinalID]
	if !exists {
		id = uuid.NewString()
		f.ids[in.FinalID] = id
	}
	if f.lose {
		f.lose = false
		return nativecontract.FinalReceipt{}, errors.New("fixture lost ack after persistence")
	}
	finalID := in.FinalID
	if f.wrongID {
		finalID = uuid.NewString()
	}
	return nativecontract.FinalReceipt{FinalID: finalID, MessageID: &id, State: "persisted", UpdatedAt: f.now()}, nil
}

// Risk: an API outage/exit/restart loses a completed final or renews its privacy
// grant. The outbox survives close/reopen, reuses one ID after a lost commit ack,
// and drops text only on a correlated receipt or explicit expiry/rejection.
func TestFinalOutboxSurvivesOutageExitAndLostAck(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "receipts")
	s := openStore(t, path, clock)
	b := testBinding()
	in, completed := completedInput(t, s, b, now)
	f, grant, approval := eligibleFinal(b, in, now)
	unapproved := approval
	unapproved.Approved = false
	_, err := s.RetainFinal(t.Context(), f, grant, unapproved, completed)
	checkReason(t, err, nativecontract.ReasonRetentionUnapproved)
	status, err := s.RetainFinal(t.Context(), f, grant, approval, completed)
	if err != nil || status.State != "pending" {
		t.Fatalf("retain %+v %v", status, err)
	}
	sink := &finalSink{now: clock, unavailable: true}
	status, err = s.RetryFinal(t.Context(), f.FinalID, sink)
	if err == nil || err.Error() != "fixture API outage" || status.State != "pending" {
		t.Fatalf("outage %+v %v", status, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	} // native writer also exited; it is not final ack.
	now = now.Add(2 * time.Second)
	s = openStore(t, path, clock)
	sink.unavailable, sink.lose = false, true
	status, err = s.RetryFinal(t.Context(), f.FinalID, sink)
	if err == nil || err.Error() != "fixture lost ack after persistence" || status.State != "pending" {
		t.Fatalf("lost ack %+v %v", status, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Second)
	s = openStore(t, path, clock)
	status, err = s.RetryFinal(t.Context(), f.FinalID, sink)
	if err != nil || status.State != "persisted" || len(sink.ids) != 1 || status.MessageID == nil {
		t.Fatalf("final replay %+v %v %+v", status, err, sink.ids)
	}
	raw, err := os.ReadFile(filepath.Join(path, "finals.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), f.Text) {
		t.Fatal("acknowledged final text retained")
	}
	conflict := f
	conflict.Text = "different answer"
	_, err = s.RetainFinal(t.Context(), conflict, grant, approval, completed)
	checkReason(t, err, nativecontract.ReasonIdentityMismatch)
	for _, expire := range []bool{false, true} {
		f.FinalID, f.RequestID = uuid.NewString(), uuid.NewString()
		_, err = s.RetainFinal(t.Context(), f, grant, approval, completed)
		if err != nil {
			t.Fatal(err)
		}
		before := sink.calls
		if expire {
			now = grant.ExpiresAt
		} else {
			sink.reject = true
		}
		status, err = s.RetryFinal(t.Context(), f.FinalID, sink)
		want := "rejected"
		if expire {
			want = "expired"
		}
		if err != nil || status.State != want || status.Reason == nil {
			t.Fatalf("closed final %+v %v", status, err)
		}
		if expire && sink.calls != before {
			t.Fatal("expired final attempted publication")
		}
	}
}

func TestReceiptEvidenceRejectsWrongInputAndFalseExit(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	s := openStore(t, filepath.Join(t.TempDir(), "receipts"), func() time.Time { return now })
	b := testBinding()
	in := testInput(b, now)
	op := uuid.NewString()
	digest, _ := Digest(in)
	r := nativecontract.Receipt{RequestID: in.RequestID, OperationID: op, Binding: &b, Operation: "input", State: "accepted", InputID: &in.InputID, SourceSequence: "0", UpdatedAt: now}
	_, err := s.Accept(t.Context(), Intent{Receipt: r, ActorID: uuid.NewString(), PayloadSHA256: digest, ExpiresAt: in.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	e := nativecontract.NativeEvidence{Stage: "write_acknowledged", RequestID: in.RequestID, InputID: in.InputID, SourceSequence: 1, ObservedAt: now}
	_, err = s.Observe(t.Context(), b, op, e)
	checkReason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
	_, err = s.BeginWrite(t.Context(), b, op)
	if err != nil {
		t.Fatal(err)
	}
	e.InputID = uuid.NewString()
	_, err = s.Observe(t.Context(), b, op, e)
	checkReason(t, err, nativecontract.ReasonIdentityMismatch)
	stop := Intent{Receipt: nativecontract.Receipt{RequestID: uuid.NewString(), OperationID: uuid.NewString(), Binding: &b, Operation: "stop", State: "accepted", SourceSequence: "0", UpdatedAt: now}, ActorID: uuid.NewString(), PayloadSHA256: strings.Repeat("b", 64), ExpiresAt: in.ExpiresAt, ProcessIdentityDigest: strings.Repeat("a", 64)}
	_, err = s.Accept(t.Context(), stop)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginWrite(t.Context(), b, stop.Receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Observe(t.Context(), b, stop.Receipt.OperationID, nativecontract.NativeEvidence{Stage: "exit_confirmed", RequestID: stop.Receipt.RequestID, SourceSequence: 2, ObservedAt: now})
	checkReason(t, err, nativecontract.ReasonInvalidInput)
	exit := nativecontract.ExitEvidence{RootExited: true, GroupAbsent: false, ProcessIdentityDigest: stop.ProcessIdentityDigest, ObservedAt: now}
	_, err = s.ConfirmExit(t.Context(), b, stop.Receipt.OperationID, 2, exit)
	checkReason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
	exit.GroupAbsent = true
	exit.ProcessIdentityDigest = strings.Repeat("c", 64)
	_, err = s.ConfirmExit(t.Context(), b, stop.Receipt.OperationID, 2, exit)
	checkReason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
	exit.ProcessIdentityDigest = stop.ProcessIdentityDigest
	got, err := s.ConfirmExit(t.Context(), b, stop.Receipt.OperationID, 2, exit)
	if err != nil || got.State != "exit_confirmed" {
		t.Fatalf("owned exit %+v %v", got, err)
	}
}

func TestPrivateStoreBoundsAndRejectsSymlinkOrSharedState(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "receipts")
	s := openStore(t, path, clock)
	if second, err := Open(path, clock); err == nil {
		second.Close()
		t.Fatal("two stores acquired writer lock")
	}
	b := testBinding()
	for i := 0; i < MaxPendingInputs; i++ {
		in := testInput(b, now)
		digest, _ := Digest(in)
		_, err := s.Accept(t.Context(), Intent{Receipt: nativecontract.Receipt{RequestID: in.RequestID, OperationID: uuid.NewString(), Binding: &b, Operation: "input", State: "accepted", InputID: &in.InputID, SourceSequence: "0", UpdatedAt: now}, ActorID: uuid.NewString(), PayloadSHA256: digest, ExpiresAt: in.ExpiresAt})
		if err != nil {
			t.Fatal(err)
		}
	}
	in := testInput(b, now)
	digest, _ := Digest(in)
	_, err := s.Accept(t.Context(), Intent{Receipt: nativecontract.Receipt{RequestID: in.RequestID, OperationID: uuid.NewString(), Binding: &b, Operation: "input", State: "accepted", InputID: &in.InputID, SourceSequence: "0", UpdatedAt: now}, ActorID: uuid.NewString(), PayloadSHA256: digest, ExpiresAt: in.ExpiresAt})
	checkReason(t, err, nativecontract.ReasonBufferFull)
	page, err := s.Page(b, "", 1)
	if err != nil || len(page.Items) != 1 || !page.Truncated || page.NextCursor == nil {
		t.Fatalf("bounded page %+v %v", page, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(shared, 0755); err != nil {
		t.Fatal(err)
	}
	_, err = Open(shared, clock)
	checkReason(t, err, nativecontract.ReasonRetentionUnapproved)
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(link, clock); err == nil {
		other.Close()
		t.Fatal("followed linked state root")
	}
}
