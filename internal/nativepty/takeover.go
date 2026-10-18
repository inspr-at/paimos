// SPDX-License-Identifier: AGPL-3.0-only

// Package nativepty switches one owned conversation between structured and TUI
// runtimes. It does not grant authority, adopt PIDs or enable a provider.
package nativepty

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

const (
	MinCols     = 40
	MinRows     = 12
	MaxCols     = 320
	MaxRows     = 100
	DefaultCols = 120
	DefaultRows = 36
)

// Caller comes from the authenticated owner-socket transport. Local is kernel
// peer provenance, never a body field. A socket bearer alone is insufficient.
type Caller struct {
	Actor                nativecontract.Actor
	ClientID, Capability string
	Local                bool
}

func (Caller) MarshalJSON() ([]byte, error) {
	return nil, errors.New("local takeover authority cannot be serialized")
}
func (*Caller) UnmarshalJSON([]byte) error {
	return errors.New("local takeover authority cannot be decoded")
}
func (Caller) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, "[local takeover caller]") }

// Access is current server-authorized control, with a locally derived monotonic
// deadline. WithWrite holds the supervisor's shared native/access-change fence
// through the callback, rechecking person, client, capability, binding, policy
// and permissions immediately before native I/O. It must not hold a database
// transaction across native I/O. All other supervisor writes share this fence.
type Access struct {
	Controller nativecontract.ControllerView
	Deadline   time.Time
	Revision   int64 // current authoritative session revision, not a local counter
}
type Authority interface {
	WithWrite(context.Context, Caller, nativecontract.Binding, string, func(Access) error) error
}

// Qualification must prove the exact binary/profile/tools/settings/OS fence
// for the requested mode. An SDK-only approval callback cannot qualify a TUI.
// Nil (the default) keeps takeover unavailable. This is operator configuration,
// not a boolean accepted from a request or a successful fake test.
type Qualification interface {
	Require(context.Context, nativecontract.Actor, nativecontract.LaunchSpec, string) error
}

type Config struct {
	Current       nativecontract.LaunchSpec
	State         string
	Coordinator   nativecontract.WriterCoordinator
	Authority     Authority
	Qualification Qualification
	PTY           nativecontract.PTYLauncher
	Structured    nativecontract.Launcher
	Clock         nativecontract.Clock
}

// Status contains no private native resume references, process handles or proof.
// OccupancyHeld remains true on every uncertain transition, including launch or
// registration failures after a possible fork. Only the owning coordinator can
// reconcile it; constructing another Manager is not a recovery operation.
type Status struct {
	Binding                      nativecontract.Binding
	State, Mode                  string
	Cols, Rows                   int
	HasController, OccupancyHeld bool
}

type Manager struct {
	gate          chan struct{}
	config        Config
	validator     *nativecontract.Validator
	spec          nativecontract.LaunchSpec
	state, mode   string
	cols, rows    int
	holder        *Access
	retiredEpoch  string
	sequenceEpoch string
	sequence      uint64
	pty           nativecontract.PTYRuntime
	structured    nativecontract.NativeTransport // retained even if commit fails
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func New(config Config) (*Manager, error) {
	if config.Coordinator == nil || config.Authority == nil || config.PTY == nil || config.Structured == nil {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	if config.Clock == nil {
		config.Clock = realClock{}
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if _, err = nativestream.Encode(v, "NativeBinding", config.Current.Binding); err != nil {
		return nil, err
	}
	if _, err = nativestream.Encode(v, "NativePolicy", config.Current.Policy); err != nil {
		return nil, err
	}
	if config.Current.Mode != "structured" || config.Current.ResumeRef == "" || config.Current.OwnedWriterID == "" {
		return nil, refuse(nativecontract.ReasonIdentityMismatch)
	}
	if config.State != "idle" && config.State != "running" && config.State != "requires_action" {
		return nil, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return &Manager{gate: make(chan struct{}, 1), config: config, validator: v, spec: cloneSpec(config.Current), state: config.State, mode: "structured", cols: DefaultCols, rows: DefaultRows}, nil
}

func refuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}
func ValidSize(cols, rows int64) bool {
	return cols >= MinCols && cols <= MaxCols && rows >= MinRows && rows <= MaxRows
}

func (m *Manager) lock(ctx context.Context) error {
	select {
	case m.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) unlock() { <-m.gate }
func (m *Manager) status() Status {
	return Status{Binding: cloneBinding(m.spec.Binding), State: m.state, Mode: m.mode, Cols: m.cols, Rows: m.rows, HasController: m.holder != nil && m.config.Clock.Now().Before(m.holder.Deadline), OccupancyHeld: true}
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	if err := m.lock(ctx); err != nil {
		return Status{}, err
	}
	defer m.unlock()
	return m.status(), nil
}
func (m *Manager) recovering() { m.state = "recovering"; m.holder = nil }

func (m *Manager) validate(schema string, value any, cols, rows int64) error {
	if schema == "NativeTakeIntent" || schema == "NativeResize" {
		if !ValidSize(cols, rows) {
			return refuse(nativecontract.ReasonInvalidInput)
		}
	}
	_, err := nativestream.Encode(m.validator, schema, value)
	return err
}

func (m *Manager) authorize(ctx context.Context, caller Caller, binding nativecontract.Binding, epoch, operation string, expires time.Time, fn func(Access) error) error {
	if !caller.Local || caller.Actor.PrincipalKind != "person" || caller.Actor.TenantID != binding.TenantID || caller.Actor.PrincipalID == "" {
		return refuse(nativecontract.ReasonWrongPrincipal)
	}
	if len(caller.ClientID) != 36 || len(caller.Capability) == 0 || len(caller.Capability) > 4096 {
		return refuse(nativecontract.ReasonGrantMissing)
	}
	if !binding.Equal(m.spec.Binding) {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	called := false
	err := m.config.Authority.WithWrite(ctx, caller, cloneBinding(binding), operation, func(access Access) error {
		if called {
			return refuse(nativecontract.ReasonGrantMissing)
		}
		called = true
		if err := ctx.Err(); err != nil {
			return err
		}
		if access.Revision < 0 || access.Revision > 9007199254740991 {
			return refuse(nativecontract.ReasonStaleRevision)
		}
		now := m.config.Clock.Now()
		if !now.Before(expires) || !now.Before(access.Deadline) {
			return refuse(nativecontract.ReasonControllerExpired)
		}
		c := access.Controller
		if c.ActorID != caller.Actor.PrincipalID || c.ClientID != caller.ClientID || c.Epoch != epoch || c.Epoch == "" || c.ControllerID == "" || epoch == m.retiredEpoch {
			return refuse(nativecontract.ReasonControllerConflict)
		}
		return fn(access)
	})
	if err == nil && !called {
		return refuse(nativecontract.ReasonGrantMissing)
	}
	return err
}

// Take serializes the whole switch. Quiesce MUST fence structured input, expire
// process-bound callbacks and checkpoint only already-authorized queued inputs.
// Coordinator methods hold the existing owned-writer reservation throughout.
func (m *Manager) Take(ctx context.Context, caller Caller, in nativecontract.TakeIntent) (out Status, err error) {
	if err = m.validate("NativeTakeIntent", in, in.Cols, in.Rows); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	if err = m.lock(ctx); err != nil {
		return out, err
	}
	defer m.unlock()
	defer func() { out = m.status() }()
	if m.mode != "structured" || (m.state != "idle" && m.state != "running" && m.state != "requires_action") {
		return out, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if m.config.Qualification == nil {
		return out, refuse(nativecontract.ReasonQualificationMissing)
	}
	transitioning := false
	defer func() {
		if err != nil && transitioning {
			m.recovering()
		}
	}()
	var writer nativecontract.WriterRecord
	var exit nativecontract.ExitEvidence
	err = m.authorize(ctx, caller, in.ExpectedBinding, in.ControllerEpoch, "take", in.ExpiresAt, func(access Access) error {
		if in.ExpectedRevision != access.Revision {
			return refuse(nativecontract.ReasonStaleRevision)
		}
		ioCtx, finish := context.WithDeadline(ctx, minDeadline(access.Deadline, in.ExpiresAt))
		defer finish()
		if err := m.qualify(ioCtx, caller.Actor, m.spec, "terminal"); err != nil {
			return err
		}
		if err := ioCtx.Err(); err != nil {
			return err
		}
		if !m.config.Clock.Now().Before(access.Deadline) || !m.config.Clock.Now().Before(in.ExpiresAt) {
			return refuse(nativecontract.ReasonControllerExpired)
		}
		if !m.config.Clock.Now().Before(m.spec.Policy.ExpiresAt) {
			return refuse(nativecontract.ReasonGrantExpired)
		}
		m.state, m.holder, transitioning = "quiescing", nil, true
		var err error
		writer, err = m.config.Coordinator.Quiesce(ioCtx, cloneBinding(m.spec.Binding), "take")
		if err != nil {
			return err
		}
		if !writer.Binding.Equal(m.spec.Binding) || !validConversationIdentity(writer.Identity, m.spec) {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		exit, err = m.config.Coordinator.CloseStructured(ioCtx, writer)
		return err
	})
	if err != nil {
		return out, err
	}
	if err = m.confirm(ctx, writer, exit); err != nil {
		return out, err
	}
	m.state = "starting"
	spec, err := m.successor(ctx, caller, "terminal")
	if err != nil {
		return out, err
	}
	defer spec.Tools.Close()
	err = m.authorize(ctx, caller, in.ExpectedBinding, in.ControllerEpoch, "take", in.ExpiresAt, func(access Access) error {
		ioCtx, finish := context.WithDeadline(ctx, minDeadline(access.Deadline, in.ExpiresAt))
		defer finish()
		if err := m.qualify(ioCtx, caller.Actor, spec, "terminal"); err != nil {
			return err
		}
		if err := ioCtx.Err(); err != nil {
			return err
		}
		if !m.config.Clock.Now().Before(access.Deadline) || !m.config.Clock.Now().Before(in.ExpiresAt) {
			return refuse(nativecontract.ReasonControllerExpired)
		}
		if !m.config.Clock.Now().Before(spec.Policy.ExpiresAt) {
			return refuse(nativecontract.ReasonGrantExpired)
		}
		var openErr error
		m.pty, openErr = m.config.PTY.OpenPTY(ioCtx, nativecontract.PTYLaunchSpec{LaunchSpec: cloneSpec(spec), Cols: int(in.Cols), Rows: int(in.Rows)})
		if openErr != nil {
			return openErr
		}
		if m.pty == nil || !validSuccessorIdentity(m.pty.Identity(), writer.Identity, spec) {
			return refuse(nativecontract.ReasonOwnershipUnconfirmed)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if err = m.commit(ctx, spec, m.pty.Identity(), "terminal"); err != nil {
		return out, err
	}
	// No old controller/worker/callback crosses the generation boundary. The
	// local client must explicitly attach to the new binding and controller.
	m.retiredEpoch, m.state, m.mode = in.ControllerEpoch, "taken", "terminal"
	m.cols, m.rows, m.sequence = int(in.Cols), int(in.Rows), 0
	return out, nil
}
func (m *Manager) qualify(ctx context.Context, actor nativecontract.Actor, spec nativecontract.LaunchSpec, mode string) error {
	copy := cloneSpec(spec)
	defer copy.Tools.Close()
	return m.config.Qualification.Require(ctx, actor, copy, mode)
}

func validIdentity(identity nativecontract.NativeIdentity, binding nativecontract.Binding) bool {
	if identity.RuntimeEpoch != binding.RuntimeEpoch || identity.PID <= 0 || identity.StartedAt.IsZero() || len(identity.ProcessIdentityDigest) != 64 {
		return false
	}
	for _, c := range identity.ProcessIdentityDigest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validConversationIdentity(identity nativecontract.NativeIdentity, spec nativecontract.LaunchSpec) bool {
	return validIdentity(identity, spec.Binding) && identity.Provider != "" &&
		(identity.NativeSessionRef == spec.ResumeRef || identity.NativeThreadRef == spec.ResumeRef)
}
func validSuccessorIdentity(identity, predecessor nativecontract.NativeIdentity, spec nativecontract.LaunchSpec) bool {
	return validConversationIdentity(identity, spec) && identity.Provider == predecessor.Provider &&
		identity.ProcessIdentityDigest != predecessor.ProcessIdentityDigest && identity.StartedAt.After(predecessor.StartedAt)
}
func (m *Manager) confirm(ctx context.Context, writer nativecontract.WriterRecord, exit nativecontract.ExitEvidence) error {
	if !exit.RootExited || !exit.GroupAbsent || exit.ObservedAt.IsZero() || exit.ProcessIdentityDigest != writer.Identity.ProcessIdentityDigest {
		return refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return m.config.Coordinator.ConfirmExit(ctx, writer, exit)
}

func (m *Manager) successor(ctx context.Context, caller Caller, mode string) (nativecontract.LaunchSpec, error) {
	spec, err := m.config.Coordinator.PrepareSuccessor(ctx, caller.Actor, cloneBinding(m.spec.Binding), mode)
	if err != nil {
		return spec, err
	}
	a, b := m.spec, spec
	old, fresh := a.Binding, b.Binding
	if _, err = nativestream.Encode(m.validator, "NativeBinding", fresh); err != nil {
		return spec, err
	}
	if _, err = nativestream.Encode(m.validator, "NativePolicy", b.Policy); err != nil {
		return spec, err
	}
	if !sameConversationBinding(old, fresh) || fresh.RuntimeEpoch == old.RuntimeEpoch || fresh.HarnessSessionID == old.HarnessSessionID || fresh.BindingRevision <= old.BindingRevision ||
		b.Mode != mode || b.OwnedWriterID == "" || b.OwnedWriterID == a.OwnedWriterID || b.Lifetime == nil || b.InitialInput != nil ||
		b.ResumeRef != a.ResumeRef || b.Cwd != a.Cwd || b.Profile.AccountID != a.Profile.AccountID || b.Profile.AccountHome != a.Profile.AccountHome ||
		b.Profile.UserHome != a.Profile.UserHome || b.Profile.WorkspaceRoot != a.Profile.WorkspaceRoot || b.Profile.ConversationRoot != a.Profile.ConversationRoot ||
		b.Profile.ProfileBindingRevision != a.Profile.ProfileBindingRevision || !slices.Equal(b.Profile.Environment, a.Profile.Environment) ||
		b.Policy.ToolPolicyDigest != a.Policy.ToolPolicyDigest || b.Policy.SettingsPolicyDigest != a.Policy.SettingsPolicyDigest ||
		b.Policy.PermissionPolicy != a.Policy.PermissionPolicy || b.Policy.LocalHistory != "vendor_native" || b.Policy.Continuation != a.Policy.Continuation ||
		b.Policy.MaximumRuntimeSeconds > a.Policy.MaximumRuntimeSeconds || b.Policy.MaximumTurns > a.Policy.MaximumTurns || b.Policy.MaximumTokens > a.Policy.MaximumTokens ||
		!sameOptions(b.Options, a.Options) || b.Profile.Binary != a.Profile.Binary || b.Profile.SDK != a.Profile.SDK ||
		!b.Tools.Binding.Equal(fresh) || len(b.Tools.Token) != 32 || !m.config.Clock.Now().Before(b.Tools.ExpiresAt) ||
		b.Tools.ExpiresAt.After(b.Policy.ExpiresAt) || len(a.Tools.Token) == 32 && subtle.ConstantTimeCompare(a.Tools.Token, b.Tools.Token) == 1 {
		_ = spec.Tools.Close()
		return nativecontract.LaunchSpec{}, refuse(nativecontract.ReasonIdentityMismatch)
	}
	return spec, nil
}
func sameOptions(a, b nativecontract.LaunchConfig) bool {
	effort := a.Effort == nil && b.Effort == nil || a.Effort != nil && b.Effort != nil && *a.Effort == *b.Effort
	a.Effort, b.Effort = nil, nil
	return effort && a == b
}
func sameConversationBinding(a, b nativecontract.Binding) bool {
	run := a.RunID == nil && b.RunID == nil || a.RunID != nil && b.RunID != nil && *a.RunID == *b.RunID
	return run && a.TenantID == b.TenantID && a.ProjectID == b.ProjectID && a.OwnedSessionID == b.OwnedSessionID && a.DaemonID == b.DaemonID && a.DaemonGeneration == b.DaemonGeneration
}
func (m *Manager) commit(ctx context.Context, spec nativecontract.LaunchSpec, identity nativecontract.NativeIdentity, mode string) error {
	bound, err := m.config.Coordinator.CommitRuntime(ctx, cloneBinding(spec.Binding), identity, mode)
	if err != nil {
		return err
	}
	if !bound.Equal(spec.Binding) {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	_ = m.spec.Tools.Close()
	m.spec, m.holder = cloneSpec(spec), nil
	return nil
}

// Return does not start a successor after uncertain exit, cancelled admission,
// revoked authority or a failed rebind. The PTY handle and reservation stay held.
func (m *Manager) Return(ctx context.Context, caller Caller, in nativecontract.ReturnIntent) (out Status, err error) {
	if err = m.validate("NativeReturnIntent", in, 0, 0); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	if err = m.lock(ctx); err != nil {
		return out, err
	}
	defer m.unlock()
	defer func() { out = m.status() }()
	if m.state != "taken" || m.pty == nil {
		return out, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	transitioning := false
	defer func() {
		if err != nil && transitioning {
			m.recovering()
		}
	}()
	var writer nativecontract.WriterRecord
	var exit, waitExit nativecontract.ExitEvidence
	err = m.authorize(ctx, caller, in.ExpectedBinding, in.ControllerEpoch, "return", in.ExpiresAt, func(access Access) error {
		if in.ExpectedRevision != access.Revision {
			return refuse(nativecontract.ReasonStaleRevision)
		}
		if m.holder != nil {
			if err := m.requireHolder(access); err != nil {
				return err
			}
		}
		ioCtx, finish := context.WithDeadline(ctx, minDeadline(access.Deadline, in.ExpiresAt))
		defer finish()
		m.state, m.holder, transitioning = "returning", nil, true
		var err error
		writer, err = m.config.Coordinator.Quiesce(ioCtx, cloneBinding(m.spec.Binding), "return")
		if err != nil {
			return err
		}
		if !writer.Binding.Equal(m.spec.Binding) || writer.Identity != m.pty.Identity() {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		exit, err = m.pty.Close(ioCtx)
		if err != nil {
			return err
		}
		waitExit, err = m.pty.Wait(ioCtx)
		return err
	})
	if err != nil {
		return out, err
	}
	if !waitExit.RootExited || !waitExit.GroupAbsent || waitExit.ProcessIdentityDigest != writer.Identity.ProcessIdentityDigest || waitExit.ObservedAt.IsZero() {
		return out, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err = m.confirm(ctx, writer, exit); err != nil {
		return out, err
	}
	spec, err := m.successor(ctx, caller, "structured")
	if err != nil {
		return out, err
	}
	defer spec.Tools.Close()
	err = m.authorize(ctx, caller, in.ExpectedBinding, in.ControllerEpoch, "return", in.ExpiresAt, func(access Access) error {
		ioCtx, finish := context.WithDeadline(ctx, minDeadline(access.Deadline, in.ExpiresAt))
		defer finish()
		if err := m.qualify(ioCtx, caller.Actor, spec, "structured"); err != nil {
			return err
		}
		if err := ioCtx.Err(); err != nil {
			return err
		}
		if !m.config.Clock.Now().Before(access.Deadline) || !m.config.Clock.Now().Before(in.ExpiresAt) {
			return refuse(nativecontract.ReasonControllerExpired)
		}
		if !m.config.Clock.Now().Before(spec.Policy.ExpiresAt) {
			return refuse(nativecontract.ReasonGrantExpired)
		}
		var openErr error
		m.structured, openErr = m.config.Structured.Open(ioCtx, cloneSpec(spec))
		if openErr != nil {
			return openErr
		}
		if m.structured == nil || !validSuccessorIdentity(m.structured.Identity(), writer.Identity, spec) {
			return refuse(nativecontract.ReasonOwnershipUnconfirmed)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if err = m.commit(ctx, spec, m.structured.Identity(), "structured"); err != nil {
		return out, err
	}
	m.pty, m.retiredEpoch, m.state, m.mode, m.sequence = nil, in.ControllerEpoch, "idle", "structured", 0
	return out, nil
}

func (m *Manager) requireHolder(access Access) error {
	if m.holder == nil || !m.config.Clock.Now().Before(m.holder.Deadline) {
		m.holder = nil
		return refuse(nativecontract.ReasonControllerExpired)
	}
	if access.Controller.ControllerID != m.holder.Controller.ControllerID || access.Controller.ClientID != m.holder.Controller.ClientID || access.Controller.Epoch != m.holder.Controller.Epoch {
		return refuse(nativecontract.ReasonControllerConflict)
	}
	m.holder = &access
	return nil
}

// RegainControl is called only after an explicit new attach or authenticated
// reconnect. Reapply even an unchanged size; a viewer cannot use it. Reacquiring
// a new controller epoch resets input sequencing, never a runtime's lifetime.
func (m *Manager) RegainControl(ctx context.Context, caller Caller, in nativecontract.Resize) (nativecontract.NativeEvidence, error) {
	return m.resize(ctx, caller, in, true)
}
func (m *Manager) Resize(ctx context.Context, caller Caller, in nativecontract.Resize) (nativecontract.NativeEvidence, error) {
	return m.resize(ctx, caller, in, false)
}
func (m *Manager) resize(ctx context.Context, caller Caller, in nativecontract.Resize, regain bool) (evidence nativecontract.NativeEvidence, err error) {
	if err = m.validate("NativeResize", in, in.Cols, in.Rows); err != nil {
		return evidence, err
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	if err = m.lock(ctx); err != nil {
		return evidence, err
	}
	defer m.unlock()
	if m.state != "taken" || m.pty == nil {
		return evidence, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	err = m.authorize(ctx, caller, in.ExpectedBinding, in.ControllerEpoch, "resize", in.ExpiresAt, func(access Access) error {
		ioCtx, finish := context.WithDeadline(ctx, minDeadline(access.Deadline, in.ExpiresAt))
		defer finish()
		if !regain {
			if err := m.requireHolder(access); err != nil {
				return err
			}
		}
		if regain || m.cols != int(in.Cols) || m.rows != int(in.Rows) {
			var err error
			evidence, err = m.pty.Resize(ioCtx, in)
			if err != nil {
				m.holder = nil
				return err
			}
			if evidence.Stage != "applied" || evidence.RequestID != in.RequestID {
				m.holder = nil
				return refuse(nativecontract.ReasonNativeFailure)
			}
			m.cols, m.rows = int(in.Cols), int(in.Rows)
		} else {
			evidence = nativecontract.NativeEvidence{Stage: "applied", RequestID: in.RequestID, ObservedAt: m.config.Clock.Now()}
		}
		if regain && m.sequenceEpoch != access.Controller.Epoch {
			m.sequence, m.sequenceEpoch = 0, access.Controller.Epoch
		}
		m.holder = &access
		return nil
	})
	return evidence, err
}

func (m *Manager) Input(ctx context.Context, caller Caller, in nativecontract.PTYInput) (evidence nativecontract.NativeEvidence, err error) {
	if err = m.validate("NativePTYInput", in, 0, 0); err != nil {
		return evidence, err
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	if err = m.lock(ctx); err != nil {
		return evidence, err
	}
	defer m.unlock()
	if m.state != "taken" || m.pty == nil {
		return evidence, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	seq, parseErr := strconv.ParseUint(in.Sequence, 10, 64)
	if parseErr != nil || seq == 0 || seq != m.sequence+1 {
		return evidence, refuse(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	err = m.authorize(ctx, caller, in.ExpectedBinding, in.ControllerEpoch, "pty_input", in.ExpiresAt, func(access Access) error {
		ioCtx, finish := context.WithDeadline(ctx, minDeadline(access.Deadline, in.ExpiresAt))
		defer finish()
		if err := m.requireHolder(access); err != nil {
			return err
		}
		// Mark BEFORE I/O: partial/uncertain writes are never resent.
		m.sequence = seq
		var err error
		evidence, err = m.pty.Write(ioCtx, in)
		if err != nil || evidence.Stage != "write_acknowledged" || evidence.RequestID != in.RequestID {
			m.holder = nil
			if err == nil {
				err = refuse(nativecontract.ReasonInputOutcomeUnconfirmed)
			}
		}
		return err
	})
	return evidence, err
}
func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// ClientLost downgrades the exact local holder only. It never closes the PTY,
// changes its size, frees occupancy or starts a structured successor.
func (m *Manager) ClientLost(ctx context.Context, clientID, epoch string, binding nativecontract.Binding) error {
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.unlock()
	if !binding.Equal(m.spec.Binding) {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	if m.holder != nil && m.holder.Controller.ClientID == clientID && m.holder.Controller.Epoch == epoch {
		m.holder = nil
	}
	return nil
}

// Frames is an internal owner port, not a public viewer authorization. The
// stream integration must independently qualify export, consent and per-frame
// access. A login/authentication PTY must never be connected to that integration.
func (m *Manager) Frames(ctx context.Context, binding nativecontract.Binding) (<-chan nativecontract.TerminalFrame, error) {
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.unlock()
	if !binding.Equal(m.spec.Binding) {
		return nil, refuse(nativecontract.ReasonWrongGeneration)
	}
	if m.state != "taken" || m.pty == nil {
		return nil, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return m.pty.Frames(), nil
}
func cloneBinding(b nativecontract.Binding) nativecontract.Binding {
	if b.RunID != nil {
		id := *b.RunID
		b.RunID = &id
	}
	return b
}
func cloneSpec(s nativecontract.LaunchSpec) nativecontract.LaunchSpec {
	s.Binding = cloneBinding(s.Binding)
	s.Tools.Binding = cloneBinding(s.Tools.Binding)
	s.Tools.Token = slices.Clone(s.Tools.Token)
	s.Profile.Environment = slices.Clone(s.Profile.Environment)
	if s.Options.Effort != nil {
		effort := *s.Options.Effort
		s.Options.Effort = &effort
	}
	return s
}
