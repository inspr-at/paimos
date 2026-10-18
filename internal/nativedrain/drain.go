// SPDX-License-Identifier: AGPL-3.0-only

// Package nativedrain prepares an owned native host for a deliberate upgrade.
// It never restarts a daemon, adopts a saved PID or launches a successor.
package nativedrain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

const (
	MaxSessions = 128
	MaxDuration = nativecontract.LocalOperationSeconds * time.Second
)

var (
	ErrDraining              = errors.New("native host is draining; new admission is closed")
	ErrPilotFrozen           = errors.New("native upgrade deferred; coordinator has not lifted the pilot freeze")
	ErrLaunchPending         = errors.New("native upgrade deferred; admission is still in progress")
	ErrBusy                  = errors.New("native upgrade deferred; active or background work is retained")
	ErrOwnershipUnconfirmed  = errors.New("native upgrade deferred; owned writer exit is unconfirmed")
	ErrIdentityMismatch      = errors.New("native upgrade deferred; writer identity does not match")
	ErrCheckpointUnconfirmed = errors.New("native upgrade deferred; checkpoint is unconfirmed")
	ErrFlushUnconfirmed      = errors.New("native upgrade deferred; receipt or final flush is unconfirmed")
	ErrInventoryUnconfirmed  = errors.New("native upgrade deferred; writer inventory is unconfirmed")
	ErrDeadline              = errors.New("native upgrade deferred; drain deadline expired")
)

// Runtime is supplied by the owning supervisor, never by a wire request.
// List includes live, possible-fork and uncertain writers from the shared ledger;
// it bounds work before allocation and reports truncation rather than hiding it.
// QuiesceAtBoundary atomically checks foreground/background work and fences new
// input/tasks under the existing writer lock. Busy or unknown work returns
// ErrBusy WITHOUT closing the runtime. Close is graceful on the owned handle;
// ConfirmExit independently verifies root/group absence under that same fence.
// Checkpoint durably records the exact native conversation/profile and recovery
// metadata, never raw output or credentials. Flush retains eligible receipts and
// finals on failure, uses existing publication grants, and returns nil only after
// acknowledgement or durable, explicit expiry/rejection. All calls honor ctx.
type Runtime interface {
	List(context.Context, int) ([]nativecontract.Binding, bool, error)
	QuiesceAtBoundary(context.Context, nativecontract.Binding) (nativecontract.WriterRecord, error)
	Close(context.Context, nativecontract.WriterRecord) (nativecontract.ExitEvidence, error)
	ConfirmExit(context.Context, nativecontract.WriterRecord, nativecontract.ExitEvidence) error
	Checkpoint(context.Context, nativecontract.WriterRecord) error
	Flush(context.Context) error
}

type Config struct {
	Runtime  Runtime
	Clock    nativecontract.Clock
	Duration time.Duration
	// CoordinatorApproved is immutable local configuration. Only the coordinator
	// may supply it after lifting the old live pilot freeze; the default is closed.
	CoordinatorApproved bool
}

// Status is content-free local owner status, not a new HTTP or C0 wire schema.
type Status struct {
	State           string
	Reason          string
	AdmissionClosed bool
	PendingLaunches int
	FlushConfirmed  bool
	StartedAt       time.Time
	Deadline        time.Time
	Sessions        []SessionStatus
}

type SessionStatus struct {
	OwnedSessionID string
	RuntimeEpoch   string
	State          string
	Reason         string
}

type session struct {
	binding      nativecontract.Binding
	writer       nativecontract.WriterRecord
	exited       bool
	checkpointed bool
	status       SessionStatus
}

type Coordinator struct {
	mu          sync.Mutex
	runtime     Runtime
	clock       nativecontract.Clock
	validator   *nativecontract.Validator
	duration    time.Duration
	approved    bool
	running     bool
	attemptDone chan struct{}
	launches    int
	status      Status
	sessions    map[string]*session
}

type wallClock struct{}

func (wallClock) Now() time.Time                         { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func New(cfg Config) (*Coordinator, error) {
	if cfg.Runtime == nil {
		return nil, errors.New("native drain requires the owning runtime")
	}
	if cfg.Duration == 0 {
		cfg.Duration = MaxDuration
	}
	if cfg.Duration < 0 || cfg.Duration > MaxDuration {
		return nil, errors.New("native drain duration is outside its bound")
	}
	if cfg.Clock == nil {
		cfg.Clock = wallClock{}
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	return &Coordinator{runtime: cfg.Runtime, clock: cfg.Clock, validator: v,
		duration: cfg.Duration, approved: cfg.CoordinatorApproved,
		status: Status{State: "serving"}, sessions: make(map[string]*session)}, nil
}

// WithAdmission encloses the ENTIRE start/resume/dispatch or runtime replacement:
// current admission, possible-fork journal, native open and owned registration.
// The caller must retain every uncertain fork in Runtime.List before returning.
// Begin serializes with entry into this section; earlier admissions are counted
// until publication, so a drain can never miss a writer registered after List.
// This barrier does not replace server grants, RequireTx or ledger admission.
func (c *Coordinator) WithAdmission(ctx context.Context, launch func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if launch == nil {
		return errors.New("native admission requires a launch operation")
	}
	c.mu.Lock()
	if c.status.AdmissionClosed {
		c.mu.Unlock()
		return ErrDraining
	}
	c.launches++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.launches--; c.mu.Unlock() }()
	return launch(ctx)
}

// Begin closes new admission monotonically. Even a failed/expired attempt keeps
// it closed; it neither resumes sessions nor clears any occupancy or pilot hold.
func (c *Coordinator) Begin() (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.approved {
		c.status.State, c.status.Reason = "deferred", "pilot_frozen"
		return c.snapshotLocked(), ErrPilotFrozen
	}
	if !c.status.AdmissionClosed {
		c.status.AdmissionClosed = true
		c.status.State, c.status.Reason = "draining", "draining"
		c.status.StartedAt = c.clock.Now()
	}
	return c.snapshotLocked(), nil
}

func (c *Coordinator) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *Coordinator) snapshotLocked() Status {
	s := c.status
	s.PendingLaunches = c.launches
	s.Sessions = make([]SessionStatus, 0, len(c.sessions))
	for _, entry := range c.sessions {
		s.Sessions = append(s.Sessions, entry.status)
	}
	sort.Slice(s.Sessions, func(i, j int) bool { return s.Sessions[i].OwnedSessionID < s.Sessions[j].OwnedSessionID })
	return s
}

// Drain makes one bounded attempt. Busy sessions are retained and visibly defer
// the upgrade; a later deliberate retry may observe their safe boundary. An
// uncooperative callback cannot block the owner forever or overlap a second
// attempt, and its late return cannot publish upgrade readiness after timeout.
func (c *Coordinator) Drain(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return c.Status(), err
	}
	if status, err := c.Begin(); err != nil {
		return status, err
	}
	c.mu.Lock()
	if c.running {
		status := c.snapshotLocked()
		c.mu.Unlock()
		return status, ErrDraining
	}
	if c.status.State == "ready" {
		status := c.snapshotLocked()
		c.mu.Unlock()
		return status, nil
	}
	c.running = true
	c.attemptDone = make(chan struct{})
	c.status.State, c.status.Reason, c.status.FlushConfirmed = "draining", "draining", false
	deadline := c.clock.Now().Add(c.duration)
	c.status.Deadline = deadline
	c.mu.Unlock()
	op, cancel := context.WithCancel(ctx)
	// Keep retries excluded until BOTH the caller and worker have finished.
	// Otherwise an older caller's timeout could overwrite a newer attempt.
	callerDone, workerDone := false, false // guarded by c.mu
	defer func() {
		cancel()
		c.mu.Lock()
		callerDone = true
		if workerDone {
			c.running = false
		}
		c.mu.Unlock()
	}()
	timer := c.clock.After(c.duration)
	done := make(chan error, 1)
	go func() {
		err := c.attempt(op, deadline)
		c.mu.Lock()
		if !c.clock.Now().Before(deadline) {
			err = ErrDeadline
		} else if op.Err() != nil {
			err = op.Err()
		}
		workerDone = true
		if callerDone {
			c.running = false
		}
		if err == nil {
			c.status.State, c.status.Reason = "ready", ""
		} else {
			c.status.State, c.status.Reason = "deferred", reason(err)
		}
		close(c.attemptDone)
		c.mu.Unlock()
		done <- err
	}()
	select {
	case err := <-done:
		return c.Status(), err
	case <-timer:
		cancel()
		c.deferAttempt(ErrDeadline)
		return c.Status(), ErrDeadline
	case <-ctx.Done():
		cancel()
		c.deferAttempt(ctx.Err())
		return c.Status(), ctx.Err()
	}
}

func (c *Coordinator) deferAttempt(err error) {
	c.mu.Lock()
	c.status.State, c.status.Reason = "deferred", reason(err)
	c.mu.Unlock()
}

func (c *Coordinator) check(ctx context.Context, deadline time.Time) error {
	if !c.clock.Now().Before(deadline) {
		return ErrDeadline
	}
	return ctx.Err()
}

func (c *Coordinator) attempt(ctx context.Context, deadline time.Time) error {
	c.mu.Lock()
	pending := c.launches != 0
	c.mu.Unlock()
	var drainErr error
	if pending {
		drainErr = ErrLaunchPending
	} else {
		drainErr = c.collect(ctx, deadline)
		if drainErr == nil {
			c.mu.Lock()
			entries := make([]*session, 0, len(c.sessions))
			for _, entry := range c.sessions {
				entries = append(entries, entry)
			}
			c.mu.Unlock()
			sort.Slice(entries, func(i, j int) bool { return entries[i].binding.OwnedSessionID < entries[j].binding.OwnedSessionID })
			for _, entry := range entries {
				if err := c.closeSession(ctx, deadline, entry); err != nil && drainErr == nil {
					drainErr = err
				}
			}
		}
	}
	// Active/background work still gets a bounded delivery flush. Retained data
	// and explicit failures remain the existing receipt/outbox owner's concern.
	if err := c.check(ctx, deadline); err != nil {
		return err
	}
	if err := c.runtime.Flush(ctx); err != nil {
		return ErrFlushUnconfirmed
	}
	if err := c.check(ctx, deadline); err != nil {
		return err
	}
	c.mu.Lock()
	c.status.FlushConfirmed = true
	c.mu.Unlock()
	return drainErr
}

func copyBinding(b nativecontract.Binding) nativecontract.Binding {
	if b.RunID != nil {
		run := *b.RunID
		b.RunID = &run
	}
	return b
}

func (c *Coordinator) collect(ctx context.Context, deadline time.Time) error {
	if err := c.check(ctx, deadline); err != nil {
		return err
	}
	bindings, truncated, err := c.runtime.List(ctx, MaxSessions)
	if err != nil || truncated || len(bindings) > MaxSessions {
		return ErrInventoryUnconfirmed
	}
	seen := make(map[string]nativecontract.Binding, len(bindings))
	for _, b := range bindings {
		// Check lengths before JSON allocation/validation; the runtime port is
		// bounded too, but malformed local values must not allocate huge frames.
		if len(b.TenantID) != 36 || len(b.ProjectID) != 36 || len(b.OwnedSessionID) != 36 ||
			len(b.HarnessSessionID) != 36 || len(b.RuntimeEpoch) != 36 || len(b.DaemonID) > 128 ||
			len(b.DaemonGeneration) > 128 || b.RunID != nil && len(*b.RunID) != 36 {
			return ErrIdentityMismatch
		}
		raw, err := json.Marshal(b)
		if err != nil || c.validator.ValidateJSON("NativeBinding", raw) != nil {
			return ErrIdentityMismatch
		}
		if _, duplicate := seen[b.OwnedSessionID]; duplicate {
			return ErrInventoryUnconfirmed
		}
		seen[b.OwnedSessionID] = copyBinding(b)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	count := len(c.sessions)
	for id, b := range seen {
		if entry, ok := c.sessions[id]; ok {
			if !entry.binding.Equal(b) {
				return ErrIdentityMismatch
			}
		} else {
			count++
		}
	}
	if count > MaxSessions {
		return ErrInventoryUnconfirmed
	}
	for id, b := range seen {
		if _, ok := c.sessions[id]; !ok {
			c.sessions[id] = &session{binding: b, status: SessionStatus{OwnedSessionID: id, RuntimeEpoch: b.RuntimeEpoch, State: "pending"}}
		}
	}
	// Missing entries are retained: disappearing from a registry never confirms
	// an exit or discards a checkpoint that failed after confirmed exit.
	return nil
}

func (c *Coordinator) sessionState(entry *session, state string, err error) {
	c.mu.Lock()
	entry.status.State, entry.status.Reason = state, reason(err)
	c.mu.Unlock()
}

func (c *Coordinator) closeSession(ctx context.Context, deadline time.Time, entry *session) error {
	if entry.checkpointed {
		return nil
	}
	if err := c.check(ctx, deadline); err != nil {
		return err
	}
	if !entry.exited {
		writer, err := c.runtime.QuiesceAtBoundary(ctx, copyBinding(entry.binding))
		if err != nil {
			if errors.Is(err, ErrBusy) {
				c.sessionState(entry, "retained", ErrBusy)
				return ErrBusy
			}
			c.sessionState(entry, "unconfirmed", ErrOwnershipUnconfirmed)
			return ErrOwnershipUnconfirmed
		}
		identity := writer.Identity
		if len(identity.ProcessIdentityDigest) != 64 || len(writer.AttemptID) > 128 ||
			len(identity.NativeSessionRef) > 256 || len(identity.NativeThreadRef) > 256 ||
			len(identity.NativeTurnRef) > 256 || len(identity.ResumeCursor) > 256 {
			c.sessionState(entry, "unconfirmed", ErrIdentityMismatch)
			return ErrIdentityMismatch
		}
		digest, err := hex.DecodeString(identity.ProcessIdentityDigest)
		if !writer.Binding.Equal(entry.binding) || identity.RuntimeEpoch != entry.binding.RuntimeEpoch ||
			err != nil || len(digest) != 32 || strings.ToLower(identity.ProcessIdentityDigest) != identity.ProcessIdentityDigest ||
			identity.PID < 1 || identity.StartedAt.IsZero() || identity.StartedAt.After(c.clock.Now()) {
			c.sessionState(entry, "unconfirmed", ErrIdentityMismatch)
			return ErrIdentityMismatch
		}
		writer.Binding = copyBinding(writer.Binding)
		entry.writer = writer
		c.sessionState(entry, "closing", nil)
		if err := c.check(ctx, deadline); err != nil {
			return err
		}
		exit, err := c.runtime.Close(ctx, writer)
		if err != nil || !exit.RootExited || !exit.GroupAbsent || exit.ObservedAt.IsZero() || exit.ObservedAt.Before(identity.StartedAt) || exit.ObservedAt.After(c.clock.Now()) {
			c.sessionState(entry, "unconfirmed", ErrOwnershipUnconfirmed)
			return ErrOwnershipUnconfirmed
		}
		if exit.ProcessIdentityDigest != identity.ProcessIdentityDigest {
			c.sessionState(entry, "unconfirmed", ErrIdentityMismatch)
			return ErrIdentityMismatch
		}
		if err := c.check(ctx, deadline); err != nil {
			return err
		}
		if err := c.runtime.ConfirmExit(ctx, writer, exit); err != nil {
			c.sessionState(entry, "unconfirmed", ErrOwnershipUnconfirmed)
			return ErrOwnershipUnconfirmed
		}
		entry.exited = true
	}
	c.sessionState(entry, "checkpoint_pending", nil)
	if err := c.check(ctx, deadline); err != nil {
		return err
	}
	if err := c.runtime.Checkpoint(ctx, entry.writer); err != nil {
		c.sessionState(entry, "checkpoint_pending", ErrCheckpointUnconfirmed)
		return ErrCheckpointUnconfirmed
	}
	entry.checkpointed = true
	c.sessionState(entry, "checkpointed", nil)
	return nil
}

func reason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrPilotFrozen):
		return "pilot_frozen"
	case errors.Is(err, ErrLaunchPending):
		return "admission_pending"
	case errors.Is(err, ErrBusy):
		return "active_or_background_work"
	case errors.Is(err, ErrOwnershipUnconfirmed):
		return "ownership_unconfirmed"
	case errors.Is(err, ErrIdentityMismatch):
		return "identity_mismatch"
	case errors.Is(err, ErrCheckpointUnconfirmed):
		return "checkpoint_unconfirmed"
	case errors.Is(err, ErrFlushUnconfirmed):
		return "flush_unconfirmed"
	case errors.Is(err, ErrInventoryUnconfirmed):
		return "inventory_unconfirmed"
	case errors.Is(err, ErrDeadline), errors.Is(err, context.DeadlineExceeded):
		return "deadline_expired"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "draining"
	}
}
