// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimeconsumer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureDriver struct {
	works                                          []Work
	effects, acks, polls                           int
	verifyErr, executeErr, completeErr, prepareErr error
	afterEffect                                    func()
}

func (d *fixtureDriver) Verify(context.Context, Binding) error { return d.verifyErr }
func (d *fixtureDriver) Poll(context.Context, Binding) (*Work, error) {
	d.polls++
	if len(d.works) == 0 {
		return nil, nil
	}
	return &d.works[0], nil
}
func (d *fixtureDriver) Prepare(context.Context, Binding, Work) error { return d.prepareErr }
func (d *fixtureDriver) Execute(context.Context, Binding, Work) (Outcome, error) {
	d.effects++
	if d.afterEffect != nil {
		d.afterEffect()
	}
	return Outcome{Level: "simple"}, d.executeErr
}
func (d *fixtureDriver) Complete(context.Context, Binding, Work, Outcome) error {
	d.acks++
	if d.completeErr != nil {
		return d.completeErr
	}
	d.works = d.works[1:]
	return nil
}
func fixtureBinding() Binding {
	return Binding{Instance: "fixture", Machine: "fixture-host", Generation: "daemon-one", Session: "session-one", Address: "codex:fixture", Project: 42, Kind: "primary", Revision: "target-one"}
}
func fixtureSupervisor(t *testing.T, dir string, d Driver) *Supervisor {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(s.Stop)
	return s
}
func advance(s *Supervisor) { now := s.now().Add(time.Minute); s.now = func() time.Time { return now } }

func TestConsumerRecoveryPreservesFIFOAndDoesNotRepeatAppliedEffect(t *testing.T) {
	dir := t.TempDir()
	d := &fixtureDriver{works: []Work{{ID: "first", Cursor: 1, Payload: "private-fixture-payload"}, {ID: "second", Cursor: 2}}, completeErr: errors.New("lost ack")}
	b := fixtureBinding()
	s := fixtureSupervisor(t, dir, d)
	if err := s.Step(context.Background(), b); err == nil || d.effects != 1 {
		t.Fatal("first effect missing")
	}
	s.Stop()
	d.completeErr = nil
	s = fixtureSupervisor(t, dir, d)
	advance(s)
	if err := s.Step(context.Background(), b); err != nil || d.effects != 1 || len(d.works) != 1 {
		t.Fatal("ack recovery repeated the side effect or lost FIFO")
	}
	if err := s.Step(context.Background(), b); err != nil || d.effects != 2 || len(d.works) != 0 {
		t.Fatal("next FIFO work did not complete")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "consumer-*"))
	for _, file := range files {
		raw, _ := os.ReadFile(file)
		if strings.Contains(string(raw), "private-fixture-payload") || strings.Contains(string(raw), b.Address) || strings.Contains(string(raw), b.Machine) {
			t.Fatal("receipt state persisted private work")
		}
		info, _ := os.Stat(file)
		if info.Mode().Perm() != 0600 {
			t.Fatal("receipt state mode unsafe")
		}
	}
}
func TestConsumerAmbiguousEffectIsQuarantinedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	d := &fixtureDriver{works: []Work{{ID: "one", Cursor: 1}}, executeErr: errors.New("response lost")}
	b := fixtureBinding()
	s := fixtureSupervisor(t, dir, d)
	if !errors.Is(s.Step(context.Background(), b), ErrUnknown) {
		t.Fatal("ambiguous effect did not fail closed")
	}
	s.Stop()
	s = fixtureSupervisor(t, dir, d)
	advance(s)
	for range 5 {
		_ = s.Step(context.Background(), b)
	}
	if d.effects != 1 || d.acks != 0 {
		t.Fatal("unknown effect was repeated or acknowledged")
	}
	evidence := s.Snapshot()
	if len(evidence) != 1 || !evidence[0].Attention || evidence[0].State != "circuit_open" {
		t.Fatal("bounded attention missing")
	}
	if len(s.circuits.Snapshot()) != 1 {
		t.Fatal("attention repeated")
	}
}
func TestConsumerTargetRevisionAndGenerationFenceCompletion(t *testing.T) {
	for _, change := range []string{"revision", "generation"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			d := &fixtureDriver{works: []Work{{ID: "one", Cursor: 1}}, completeErr: errors.New("lost ack")}
			b := fixtureBinding()
			s := fixtureSupervisor(t, dir, d)
			_ = s.Step(context.Background(), b)
			s.Stop()
			s = fixtureSupervisor(t, dir, d)
			advance(s)
			d.completeErr = nil
			if change == "revision" {
				b.Revision = "replacement-target"
			} else {
				b.Generation = "replacement-daemon"
			}
			if !errors.Is(s.Step(context.Background(), b), ErrOwnership) || d.effects != 1 || d.acks != 1 {
				t.Fatal("replacement advanced old generation")
			}
		})
	}
	d := &fixtureDriver{works: []Work{{ID: "one", Cursor: 1}}}
	s := fixtureSupervisor(t, t.TempDir(), d)
	d.afterEffect = func() { d.verifyErr = ErrOwnership }
	if !errors.Is(s.Step(context.Background(), fixtureBinding()), ErrOwnership) || d.acks != 0 {
		t.Fatal("ownership change between effect and ack was not fenced")
	}
}
func TestConsumerBackoffCircuitAndSingleton(t *testing.T) {
	dir := t.TempDir()
	d := &fixtureDriver{verifyErr: errors.New("offline")}
	s := fixtureSupervisor(t, dir, d)
	b := fixtureBinding()
	if _, err := New(dir, d); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate supervisor admitted")
	}
	for attempt := 1; attempt <= 3; attempt++ {
		_ = s.Step(context.Background(), b)
		e := s.Snapshot()[0]
		if e.Failures != attempt {
			t.Fatal("retry count incorrect")
		}
		for range 5 {
			_ = s.Step(context.Background(), b)
		}
		if s.Snapshot()[0].Failures != attempt {
			t.Fatal("backoff hot loop")
		}
		advance(s)
	}
	s.Stop()
	s = fixtureSupervisor(t, dir, d)
	advance(s)
	_ = s.Step(context.Background(), b)
	if s.Snapshot()[0].State != "circuit_open" || len(s.circuits.Snapshot()) != 1 {
		t.Fatal("restart reset circuit")
	}
	s.Stop()
	if !errors.Is(s.Step(context.Background(), b), ErrOwnership) {
		t.Fatal("stopped supervisor executed")
	}
}
func TestConsumerUnsupportedPrepareHasNoEffectIntent(t *testing.T) {
	d := &fixtureDriver{works: []Work{{ID: "one", Cursor: 1}}, prepareErr: ErrUnsupported}
	s := fixtureSupervisor(t, t.TempDir(), d)
	if !errors.Is(s.Step(context.Background(), fixtureBinding()), ErrUnsupported) || d.effects != 0 || len(s.receipts.Snapshot()) != 0 {
		t.Fatal("unsupported primitive reserved or executed")
	}
}

func TestConsumerSelectedTargetChangeFencesAppliedReceipt(t *testing.T) {
	dir := t.TempDir()
	d := &fixtureDriver{works: []Work{{ID: "one", Cursor: 1, Revision: "target-one"}}, completeErr: errors.New("lost ack")}
	s := fixtureSupervisor(t, dir, d)
	b := fixtureBinding()
	_ = s.Step(context.Background(), b)
	s.Stop()
	d.works[0].Revision = "target-two"
	d.completeErr = nil
	s = fixtureSupervisor(t, dir, d)
	advance(s)
	if !errors.Is(s.Step(context.Background(), b), ErrOwnership) || d.effects != 1 || d.acks != 1 {
		t.Fatal("new target completed old effect")
	}
}

func TestTransientRepairKeepsDurableEffectsAndUnknownQuarantined(t *testing.T) {
	d := &fixtureDriver{verifyErr: errors.New("offline")}
	s := fixtureSupervisor(t, t.TempDir(), d)
	b := fixtureBinding()
	for range 3 {
		_ = s.Step(context.Background(), b)
		advance(s)
	}
	if s.Snapshot()[0].State != "circuit_open" {
		t.Fatal("fixture circuit did not open")
	}
	d.verifyErr = nil
	if err := s.Repair(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	d.works = []Work{{ID: "one", Cursor: 1}}
	d.executeErr = ErrUnknown
	if !errors.Is(s.Step(context.Background(), b), ErrUnknown) {
		t.Fatal("effect not quarantined")
	}
	if !errors.Is(s.Repair(context.Background(), b), ErrUnknown) || len(s.receipts.Snapshot()) != 1 || d.effects != 1 {
		t.Fatal("repair discarded ambiguous effect")
	}
}
func TestRepairReopensOwnershipChangedStreamForVerifiedSuccessor(t *testing.T) {
	dir := t.TempDir()
	d := &fixtureDriver{verifyErr: ErrOwnership}
	s := fixtureSupervisor(t, dir, d)
	b := fixtureBinding()
	if !errors.Is(s.Step(context.Background(), b), ErrOwnership) {
		t.Fatal("ownership loss did not fence the stream")
	}
	if got := s.Snapshot(); len(got) != 1 || got[0].State != "circuit_open" || got[0].Reason != "ownership_changed" {
		t.Fatal("ownership loss did not persist a closed circuit")
	}
	if !errors.Is(s.Repair(context.Background(), b), ErrOwnership) {
		t.Fatal("repair admitted an unverified binding")
	}
	s.Stop()
	d.verifyErr = nil
	b.Generation = "daemon-two"
	b.Session = "session-two"
	b.Revision = "target-two"
	s = fixtureSupervisor(t, dir, d)
	if err := s.Repair(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if d.effects != 0 || len(s.receipts.Snapshot()) != 0 {
		t.Fatal("successor repair created an effect without work")
	}
	if got := s.Snapshot(); len(got) != 1 || got[0].State != "ready" || got[0].Failures != 0 || got[0].Generation != "daemon-two" {
		t.Fatal("verified successor did not reopen the address stream")
	}
}
func TestRepairKeepsPendingEffectWhenOwnershipChanged(t *testing.T) {
	dir := t.TempDir()
	d := &fixtureDriver{verifyErr: ErrOwnership}
	s := fixtureSupervisor(t, dir, d)
	b := fixtureBinding()
	_ = s.Step(context.Background(), b)
	s.Stop()
	d.verifyErr = nil
	b.Generation = "daemon-two"
	b.Session = "session-two"
	b.Revision = "target-two"
	s = fixtureSupervisor(t, dir, d)
	pending := checkpoint{Key: digest(struct{ ID string }{"pending"}), Binding: b.Key(), Phase: "pending"}
	if err := s.receipts.Put(pending); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(s.Repair(context.Background(), b), ErrUnknown) {
		t.Fatal("repair discarded a pending effect after ownership changed")
	}
	if got := s.receipts.Snapshot(); len(got) != 1 || got[0] != pending || d.effects != 0 {
		t.Fatal("repair mutated a pending effect or executed work")
	}
}
func TestBusyDeferralDoesNotReserveReceiptOrCountFailure(t *testing.T) {
	d := &fixtureDriver{works: []Work{{ID: "one", Cursor: 1}}, prepareErr: ErrDeferred}
	s := fixtureSupervisor(t, t.TempDir(), d)
	b := fixtureBinding()
	for range 8 {
		if err := s.Step(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.receipts.Snapshot()) != 0 || s.Snapshot()[0].Failures != 0 || d.effects != 0 {
		t.Fatal("busy FIFO created ambiguous receipt or opened circuit")
	}
}

type stalledStreamDriver struct {
	fixtureDriver
	entered chan string
	release chan struct{}
}

func (d *stalledStreamDriver) Poll(ctx context.Context, b Binding) (*Work, error) {
	d.entered <- b.Address
	if b.Address == "codex:stalled" {
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, nil
}

func TestConsumerIndependentStreamProgressAndLifecycleDrain(t *testing.T) {
	d := &stalledStreamDriver{entered: make(chan string, 8), release: make(chan struct{})}
	s := fixtureSupervisor(t, t.TempDir(), d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := fixtureBinding()
	a.Address = "codex:stalled"
	aDone := make(chan error, 1)
	go func() { aDone <- s.Step(ctx, a) }()
	select {
	case <-d.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first stream never entered")
	}
	b := a
	b.Address, b.Session = "codex:healthy", "session-two"
	bDone := make(chan error, 1)
	go func() { bDone <- s.Step(ctx, b) }()
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("healthy stream blocked behind stalled stream")
	}
	<-d.entered
	replacement := a
	replacement.Generation, replacement.Revision = "new-daemon", "new-target"
	waiting, stopWaiting := context.WithCancel(ctx)
	nextDone := make(chan error, 1)
	go func() { nextDone <- s.Step(waiting, replacement) }()
	select {
	case <-d.entered:
		t.Fatal("replacement overtook stalled generation")
	case <-nextDone:
		t.Fatal("replacement completed before its predecessor")
	case <-time.After(30 * time.Millisecond):
	}
	stopWaiting()
	select {
	case err := <-nextDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("same-stream wait ignored cancellation")
	}
	select {
	case <-d.entered:
		t.Fatal("replacement overtook stalled generation")
	default:
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stop returned before active stream drained")
	case <-time.After(30 * time.Millisecond):
	}
	close(d.release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not drain")
	}
	if err := s.Step(ctx, b); !errors.Is(err, ErrOwnership) {
		t.Fatal("stopped supervisor admitted work", err)
	}
}
