// SPDX-License-Identifier: AGPL-3.0-only

package nativepty

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

// TakeWire is a protected owner-socket client, never a network terminal writer.
// Refresh reads an authorized server projection, not a caller-supplied binding.
type TakeWire interface {
	Post(context.Context, nativecontract.Binding, string, string, any, string, string, string, any) error
	Watch(context.Context, nativecontract.SessionView, string, func(nativecontract.StreamEvent) error) error
	Close()
}
type ClientConfig struct {
	Snapshot nativecontract.SessionView
	Refresh  func(context.Context) (nativecontract.SessionView, error)
	Wire     TakeWire
	Clock    nativecontract.Clock
}
type TakeClient struct {
	wire                                  TakeWire
	refresh                               func(context.Context) (nativecontract.SessionView, error)
	validator                             *nativecontract.Validator
	clock                                 nativecontract.Clock
	view                                  nativecontract.SessionView
	binding                               nativecontract.Binding
	clientID, viewerID, epoch, capability string
	deadline                              time.Time
	cols, rows                            int
	sequence                              uint64
	lost, detached                        bool
}

func (*TakeClient) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, "[native takeover client]") }

func OpenClient(ctx context.Context, config ClientConfig) (*TakeClient, error) {
	if config.Wire == nil || config.Refresh == nil {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	if config.Clock == nil {
		config.Clock = realClock{}
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if _, err = nativestream.Encode(v, "NativeSessionView", config.Snapshot); err != nil {
		return nil, err
	}
	s := config.Snapshot
	if s.CurrentBinding == nil || s.Profile != "native" || (s.Mode != "structured" && s.Mode != "terminal") {
		return nil, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if s.Mode == "structured" && (!s.Capabilities.Take || !s.Capabilities.TakeSupported) || s.Mode == "terminal" && (!s.Capabilities.TerminalView || s.State != "taken") {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	c := &TakeClient{wire: config.Wire, refresh: config.Refresh, validator: v, clock: config.Clock, view: s, binding: cloneBinding(*s.CurrentBinding), clientID: uuid.NewString()}
	if err = c.attach(ctx, ""); err != nil {
		c.wire.Close()
		return nil, err
	}
	return c, nil
}
func (c *TakeClient) attach(ctx context.Context, retiredEpoch string) error {
	in := nativecontract.AttachIntent{RequestID: uuid.NewString(), ExpectedBinding: c.binding, ClientID: c.clientID, Mode: "control", View: c.view.Mode}
	var out nativecontract.AttachResult
	started := c.clock.Now()
	if err := c.wire.Post(ctx, c.binding, "attach", in.RequestID, in, "NativeAttachResult", "", "", &out); err != nil {
		return err
	}
	return c.acceptAttach(out, started, retiredEpoch, false)
}
func (c *TakeClient) acceptAttach(out nativecontract.AttachResult, started time.Time, retired string, renew bool) error {
	if !out.Binding.Equal(c.binding) || out.Snapshot.CurrentBinding == nil || !out.Snapshot.CurrentBinding.Equal(c.binding) || out.Snapshot.Generation != c.view.Generation || out.Snapshot.OwnedSessionID != c.binding.OwnedSessionID || out.Snapshot.Mode != c.view.Mode {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	if out.Mode != "control" || out.Controller == nil || out.ControlCapability == nil || *out.ControlCapability == "" || out.Controller.ClientID != c.clientID || out.Snapshot.Controller == nil || *out.Controller != *out.Snapshot.Controller || out.Controller.Epoch == retired && retired != "" || renew && (out.Controller.Epoch != c.epoch || out.ViewerID != c.viewerID) {
		return refuse(nativecontract.ReasonControllerConflict)
	}
	if out.StreamPath != localPath(c.binding, "stream") || out.ExpiresInMS <= 0 || out.ExpiresInMS > 30000 {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	deadline := started.Add(time.Duration(out.ExpiresInMS) * time.Millisecond)
	if !c.clock.Now().Before(deadline) {
		return refuse(nativecontract.ReasonControllerExpired)
	}
	c.view, c.deadline, c.viewerID, c.epoch, c.capability = out.Snapshot, deadline, out.ViewerID, out.Controller.Epoch, *out.ControlCapability
	return nil
}
func (c *TakeClient) live() error {
	if c.lost || c.detached || c.capability == "" {
		return refuse(nativecontract.ReasonControllerExpired)
	}
	if !c.clock.Now().Before(c.deadline) {
		c.lose()
		return refuse(nativecontract.ReasonControllerExpired)
	}
	return nil
}
func (c *TakeClient) lose() { c.lost, c.capability = true, "" }
func (c *TakeClient) operation(ctx context.Context, method, requestID string, in any) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	if err := c.live(); err != nil {
		return out, err
	}
	err := c.wire.Post(ctx, c.binding, method, requestID, in, "NativeOperationResult", c.viewerID, c.capability, &out)
	if err == nil && (out.Receipt.RequestID != requestID || out.Receipt.Operation != method || out.Receipt.Binding == nil || !out.Receipt.Binding.Equal(c.binding)) {
		err = refuse(nativecontract.ReasonIdentityMismatch)
	}
	if err == nil && (out.Receipt.State == "uncertain" || out.Receipt.State == "expired" || out.Receipt.State == "rejected") {
		err = refuse(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if err != nil {
		c.lose()
	}
	return out, err
}

// Begin may explicitly reconnect to an already-owned taken PTY; it never
// launches another one. Accepted/queued take is followed by a bounded authorized
// projection read until the new runtime is actually registered.
func (c *TakeClient) Begin(ctx context.Context, cols, rows int) error {
	if !ValidSize(int64(cols), int64(rows)) {
		return refuse(nativecontract.ReasonInvalidInput)
	}
	if c.view.Mode == "terminal" {
		return c.Resize(ctx, cols, rows, true)
	}
	in := nativecontract.TakeIntent{RequestID: uuid.NewString(), ExpectedBinding: c.binding, ControllerEpoch: c.epoch, ExpectedRevision: c.view.Revision, Cols: int64(cols), Rows: int64(rows), ExpiresAt: c.deadline}
	if _, err := c.operation(ctx, "take", in.RequestID, in); err != nil {
		return err
	}
	oldEpoch := c.epoch
	if err := c.awaitSuccessor(ctx, "terminal"); err != nil {
		c.lose()
		return err
	}
	c.capability, c.viewerID, c.epoch, c.cols, c.rows, c.sequence = "", "", "", 0, 0, 0
	if err := c.attach(ctx, oldEpoch); err != nil {
		c.lose()
		return err
	}
	return c.Resize(ctx, cols, rows, true)
}
func (c *TakeClient) awaitSuccessor(ctx context.Context, mode string) error {
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	old, generation, revision := c.binding, c.view.Generation, c.view.Revision
	for {
		view, err := c.refresh(ctx)
		if err != nil {
			return err
		}
		if _, err = nativestream.Encode(c.validator, "NativeSessionView", view); err != nil {
			return err
		}
		if view.CurrentBinding == nil || !sameConversationBinding(old, *view.CurrentBinding) || view.OwnedSessionID != old.OwnedSessionID || view.ConversationRef != c.view.ConversationRef || view.OwnerPersonID != c.view.OwnerPersonID {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		if view.State == "recovering" || view.State == "ownership_lost" || view.State == "stopped" || view.State == "failed" {
			return refuse(nativecontract.ReasonOwnershipUnconfirmed)
		}
		if view.Mode == mode && (mode != "terminal" || view.State == "taken") {
			b := *view.CurrentBinding
			if b.RuntimeEpoch == old.RuntimeEpoch || b.HarnessSessionID == old.HarnessSessionID || b.BindingRevision <= old.BindingRevision || view.Generation <= generation || view.Revision <= revision {
				return refuse(nativecontract.ReasonWrongGeneration)
			}
			c.view, c.binding = view, cloneBinding(b)
			return nil
		}
		if !view.CurrentBinding.Equal(old) {
			return refuse(nativecontract.ReasonWrongGeneration)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.clock.After(150 * time.Millisecond):
		}
	}
}
func (c *TakeClient) HandBack(ctx context.Context) error {
	if c.view.Mode != "terminal" || c.view.State != "taken" {
		return refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	in := nativecontract.ReturnIntent{RequestID: uuid.NewString(), ExpectedBinding: c.binding, ControllerEpoch: c.epoch, ExpectedRevision: c.view.Revision, ExpiresAt: c.deadline}
	if _, err := c.operation(ctx, "return", in.RequestID, in); err != nil {
		return err
	}
	if err := c.awaitSuccessor(ctx, "structured"); err != nil {
		c.lose()
		return err
	}
	// The fresh structured controller is deliberately not claimed by hand-back.
	c.capability, c.viewerID, c.epoch, c.detached = "", "", "", true
	return nil
}
func (c *TakeClient) Renew(ctx context.Context) error {
	if err := c.live(); err != nil {
		return err
	}
	epoch := c.epoch
	in := nativecontract.AttachRenew{RequestID: uuid.NewString(), ExpectedBinding: c.binding, ViewerID: c.viewerID, ControllerEpoch: &epoch}
	var out nativecontract.AttachResult
	started := c.clock.Now()
	err := c.wire.Post(ctx, c.binding, "renew", in.RequestID, in, "NativeAttachResult", c.viewerID, c.capability, &out)
	if err == nil {
		err = c.acceptAttach(out, started, "", true)
	}
	if err != nil {
		c.lose()
	}
	return err
}
func (c *TakeClient) Resize(ctx context.Context, cols, rows int, force bool) error {
	if err := c.live(); err != nil {
		return err
	}
	if c.view.Mode != "terminal" || !ValidSize(int64(cols), int64(rows)) {
		return refuse(nativecontract.ReasonInvalidInput)
	}
	if !force && c.cols == cols && c.rows == rows {
		return nil
	}
	in := nativecontract.Resize{RequestID: uuid.NewString(), ExpectedBinding: c.binding, ControllerEpoch: c.epoch, Cols: int64(cols), Rows: int64(rows), ExpiresAt: c.deadline}
	out, err := c.operation(ctx, "resize", in.RequestID, in)
	if err == nil && out.Receipt.State != "applied" {
		c.lose()
		return refuse(nativecontract.ReasonNativeFailure)
	}
	if err == nil {
		c.cols, c.rows = cols, rows
	}
	return err
}
func (c *TakeClient) Write(ctx context.Context, data []byte) error {
	if err := c.live(); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > nativecontract.PTYInputDecodedBytes || c.view.Mode != "terminal" {
		return refuse(nativecontract.ReasonInvalidInput)
	}
	c.sequence++
	in := nativecontract.PTYInput{RequestID: uuid.NewString(), ExpectedBinding: c.binding, ControllerEpoch: c.epoch, Sequence: strconv.FormatUint(c.sequence, 10), DataBase64: base64.StdEncoding.EncodeToString(data), ExpiresAt: c.deadline}
	out, err := c.operation(ctx, "pty_input", in.RequestID, in)
	if err == nil && out.Receipt.State != "write_acknowledged" {
		c.lose()
		return refuse(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	return err
}
func (c *TakeClient) Detach(ctx context.Context) error {
	defer c.wire.Close()
	if c.detached {
		return nil
	}
	c.detached = true
	in := nativecontract.Detach{RequestID: uuid.NewString(), ExpectedBinding: c.binding, ViewerID: c.viewerID}
	var out nativecontract.OperationResult
	err := c.wire.Post(ctx, c.binding, "detach", in.RequestID, in, "NativeOperationResult", c.viewerID, "", &out)
	c.lose()
	return err
}

func safeError(err error) error {
	var validation *nativecontract.ValidationError
	if errors.As(err, &validation) {
		return refuse(validation.Reason)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("native terminal operation not confirmed")
}
