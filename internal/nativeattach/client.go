// SPDX-License-Identifier: AGPL-3.0-only

// Package nativeattach implements a structured terminal viewer of an existing
// owned session. It never launches, stops or adopts a native process.
package nativeattach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

var (
	ErrReadOnly       = errors.New("this attach is read-only; attach again with explicit control")
	ErrLeaseExpired   = errors.New("attach lease expired; attach again explicitly")
	ErrBindingChanged = errors.New("session generation changed; attach again explicitly")
	ErrNoActiveTurn   = errors.New("no current turn is known to interrupt")
)

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options uses an authorized server projection, never a caller-invented binding.
// Server/socket authorization is still required for every request.
type Options struct {
	Snapshot nativecontract.SessionView
	Control  bool
	Clock    nativecontract.Clock
}

// Session has one foreground owner. Stream reads use an immutable copy of the
// binding; all actions keep the original binding even after a successor appears.
type Session struct {
	wire                                  *socketWire
	validator                             *nativecontract.Validator
	clock                                 nativecontract.Clock
	binding                               nativecontract.Binding
	generation                            int64
	snapshot                              nativecontract.SessionView
	clientID, viewerID, epoch, capability string
	deadline                              time.Time
	control, detached, authorityLost      bool
	turn                                  string
}

// Format prevents an accidental diagnostic from exposing the controller proof.
func (*Session) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, "[native session attach]") }

// Open attaches over the protected owner socket. Viewing is the default;
// controller conflicts are returned, never downgraded silently or force-taken.
func Open(ctx context.Context, socket string, opts Options) (*Session, error) {
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(opts.Snapshot)
	if err != nil {
		return nil, err
	}
	if err = v.ValidateJSON("NativeSessionView", raw); err != nil {
		return nil, safeValidation(err)
	}
	if opts.Snapshot.CurrentBinding == nil || !opts.Snapshot.Capabilities.EventsSupported {
		return nil, errors.New("session has no qualified structured stream")
	}
	if opts.Control && (!opts.Snapshot.Capabilities.Input || !opts.Snapshot.Capabilities.ChatSupported) {
		return nil, errors.New("session does not advertise input support")
	}
	wire, err := newSocketWire(socket, v)
	if err != nil {
		return nil, err
	}
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	s := &Session{wire: wire, validator: v, clock: clock, binding: copyBinding(*opts.Snapshot.CurrentBinding), generation: opts.Snapshot.Generation, snapshot: opts.Snapshot, clientID: uuid.NewString(), control: opts.Control}
	mode := "view"
	if s.control {
		mode = "control"
	}
	in := nativecontract.AttachIntent{RequestID: uuid.NewString(), ExpectedBinding: s.binding, ClientID: s.clientID, Mode: mode, View: "structured"}
	started := clock.Now()
	var result nativecontract.AttachResult
	err = wire.post(ctx, s.binding, "attach", in.RequestID, in, "NativeAttachResult", nil, &result)
	if err == nil {
		err = s.acceptAttach(result, started, false)
	}
	if err != nil {
		wire.close()
		return nil, err
	}
	return s, nil
}

func copyBinding(b nativecontract.Binding) nativecontract.Binding {
	if b.RunID != nil {
		id := *b.RunID
		b.RunID = &id
	}
	return b
}

func (s *Session) acceptAttach(out nativecontract.AttachResult, started time.Time, renewal bool) error {
	mode := "view"
	if s.control {
		mode = "control"
	}
	if !s.binding.Equal(out.Binding) || out.Snapshot.CurrentBinding == nil || !s.binding.Equal(*out.Snapshot.CurrentBinding) || out.Snapshot.Generation != s.generation || out.Snapshot.OwnedSessionID != s.binding.OwnedSessionID {
		return ErrBindingChanged
	}
	if out.Mode != mode || out.StreamPath != s.wire.path(s.binding, "stream") || (renewal && out.ViewerID != s.viewerID) {
		return errors.New("invalid attach identity or stream path")
	}
	if s.control {
		if out.Controller == nil || out.ControlCapability == nil || out.Controller.ClientID != s.clientID || out.Snapshot.Controller == nil || *out.Controller != *out.Snapshot.Controller || (renewal && out.Controller.Epoch != s.epoch) {
			return ErrReadOnly
		}
	} else if out.ControlCapability != nil {
		return errors.New("read-only attach returned a private control proof")
	}
	deadline := started.Add(time.Duration(out.ExpiresInMS) * time.Millisecond)
	if !s.clock.Now().Before(deadline) {
		return ErrLeaseExpired
	}
	s.deadline, s.viewerID, s.snapshot = deadline, out.ViewerID, out.Snapshot
	if s.control {
		s.epoch, s.capability = out.Controller.Epoch, *out.ControlCapability
	}
	return nil
}

func (s *Session) headers(control bool) map[string]string {
	h := map[string]string{nativecontract.ViewerHeader: s.viewerID}
	if control {
		h[nativecontract.ControlHeader] = s.capability
	}
	return h
}

func (s *Session) live() error {
	if s.detached || s.authorityLost {
		return ErrBindingChanged
	}
	if !s.clock.Now().Before(s.deadline) {
		return ErrLeaseExpired
	}
	return nil
}

func (s *Session) requireControl() error {
	if err := s.live(); err != nil {
		return err
	}
	if !s.control || s.capability == "" || s.epoch == "" {
		return ErrReadOnly
	}
	if s.snapshot.Mode != "structured" {
		return errors.New("structured input is paused during terminal takeover")
	}
	return nil
}

// Renew revalidates the same viewer and controller epoch. A refusal closes
// local authority immediately. expires_at is display-only; elapsed time is
// charged against the server-derived expires_in_ms monotonic budget.
func (s *Session) Renew(ctx context.Context) error {
	if err := s.live(); err != nil {
		return err
	}
	in := nativecontract.AttachRenew{RequestID: uuid.NewString(), ExpectedBinding: s.binding, ViewerID: s.viewerID}
	if s.control {
		epoch := s.epoch
		in.ControllerEpoch = &epoch
	}
	started := s.clock.Now()
	var out nativecontract.AttachResult
	err := s.wire.post(ctx, s.binding, "renew", in.RequestID, in, "NativeAttachResult", s.headers(s.control), &out)
	if err == nil {
		err = s.acceptAttach(out, started, true)
	}
	if err != nil {
		s.authorityLost = true
		s.capability = ""
	}
	return err
}

// Submit reports precisely the returned receipt; accepted/queued are not
// native delivery. An uncertain write is never retried automatically.
func (s *Session) Submit(ctx context.Context, text, mode string) (nativecontract.Receipt, error) {
	if err := s.requireControl(); err != nil {
		return nativecontract.Receipt{}, err
	}
	if !s.snapshot.Capabilities.Input || !s.snapshot.Capabilities.ChatSupported {
		return nativecontract.Receipt{}, ErrReadOnly
	}
	if len(text) == 0 || len(text) > nativecontract.InputUTF8Bytes || !utf8.ValidString(text) || (mode != "now" && mode != "after_turn") {
		return nativecontract.Receipt{}, errors.New("invalid or oversized input")
	}
	// The expiration is an intent bound, not a renewal or permission assertion.
	in := nativecontract.Input{RequestID: uuid.NewString(), InputID: uuid.NewString(), ExpectedBinding: s.binding, Mode: mode, Text: text, ExpiresAt: s.deadline}
	out, err := s.operation(ctx, "input", in.RequestID, in, true)
	if err == nil && (out.InputID == nil || *out.InputID != in.InputID) {
		s.authorityLost = true
		s.capability = ""
		err = errors.New("input receipt correlation mismatch")
	}
	return out, err
}

// Interrupt is a separate deliberate action, fenced to a known active turn.
func (s *Session) Interrupt(ctx context.Context) (nativecontract.Receipt, error) {
	if err := s.requireControl(); err != nil {
		return nativecontract.Receipt{}, err
	}
	if !s.snapshot.Capabilities.Interrupt || !s.snapshot.Capabilities.InterruptSupported {
		return nativecontract.Receipt{}, errors.New("interrupt is unsupported")
	}
	if s.turn == "" {
		return nativecontract.Receipt{}, ErrNoActiveTurn
	}
	in := nativecontract.Interrupt{RequestID: uuid.NewString(), ExpectedBinding: s.binding, ExpectedTurnRef: s.turn, ExpiresAt: s.deadline}
	return s.operation(ctx, "interrupt", in.RequestID, in, true)
}

func (s *Session) operation(ctx context.Context, method, requestID string, params any, control bool) (receipt nativecontract.Receipt, result error) {
	defer func() {
		if result != nil {
			s.authorityLost = true
			s.capability = ""
		}
	}()
	var out nativecontract.OperationResult
	err := s.wire.post(ctx, s.binding, method, requestID, params, "NativeOperationResult", s.headers(control), &out)
	if err != nil {
		s.authorityLost = true
		s.capability = ""
		return out.Receipt, err
	}
	if out.Receipt.RequestID != requestID || out.Receipt.Operation != method || out.Receipt.Binding == nil || !s.binding.Equal(*out.Receipt.Binding) {
		return out.Receipt, errors.New("operation receipt correlation mismatch")
	}
	if out.Session != nil && (out.Session.CurrentBinding == nil || !s.binding.Equal(*out.Session.CurrentBinding) || out.Session.Generation != s.generation) {
		return out.Receipt, ErrBindingChanged
	}
	if out.Receipt.State == "rejected" || out.Receipt.State == "expired" || out.Receipt.State == "uncertain" {
		return out.Receipt, errors.New("native operation " + out.Receipt.State + "; do not resend automatically")
	}
	s.observeReceipt(out.Receipt)
	return out.Receipt, nil
}

func safeValidation(err error) error {
	var v *nativecontract.ValidationError
	if errors.As(err, &v) {
		return &nativecontract.ValidationError{Reason: v.Reason, Path: "$"}
	}
	return errors.New("invalid native session response")
}

func (s *Session) observeReceipt(r nativecontract.Receipt) {
	if r.TurnRef != nil && r.State == "turn_started" {
		s.turn = *r.TurnRef
	}
	if r.State == "turn_completed" && r.TurnRef != nil && *r.TurnRef == s.turn {
		s.turn = ""
	}
}

// Detach relinquishes only this viewer. No stop, signal or writer transition
// exists in this package's transport. Cleanup is bounded even after cancellation.
func (s *Session) Detach(ctx context.Context) error {
	if s.detached {
		return nil
	}
	defer func() { s.detached = true; s.capability = ""; s.wire.close() }()
	in := nativecontract.Detach{RequestID: uuid.NewString(), ExpectedBinding: s.binding, ViewerID: s.viewerID}
	_, err := s.operation(ctx, "detach", in.RequestID, in, false)
	return err
}

// Receipts reads one bounded page. Truncation and the next cursor stay explicit.
func (s *Session) Receipts(ctx context.Context, after string) (nativecontract.ReceiptPage, error) {
	var out nativecontract.ReceiptPage
	if err := s.live(); err != nil {
		return out, err
	}
	if len(after) > 512 {
		return out, errors.New("receipt cursor too large")
	}
	err := s.wire.receipts(ctx, s.binding, s.headers(false), after, &out)
	return out, err
}
