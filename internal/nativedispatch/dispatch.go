// SPDX-License-Identifier: AGPL-3.0-only

// Package nativedispatch prepares worker dispatch through the owning agentd.
// It does not spawn processes, register hook children or allocate capacity.
// The durable C0 start/claim path owns those operations, including replays.
package nativedispatch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

const maxTracked = 256

type Request struct {
	TenantID, ProjectID string
	Start               nativecontract.LocalStart
}

// Target comes from the authenticated assignment resolver, never from the
// agent's tool arguments. This is a preflight; Host must still reauthorize the
// current leaf/ancestry/grant inside the final server claim transaction.
type Target struct {
	Actor                                    nativecontract.Actor
	Policy                                   nativecontract.Policy
	WorkID, ParentHarnessSessionID, Worktree string
	Leaf                                     bool
}

type Authority interface {
	ResolveWorker(context.Context, Request) (Target, error)
}

// Host is a narrow subset of the frozen C0 port. Start must use P1b's durable
// idempotent intent/claim, register the child and reserve the existing ledger
// BEFORE possible fork, and retain uncertain holds. No permissive fallback.
type Host interface {
	Start(context.Context, nativecontract.LocalStart) (nativecontract.OperationResult, error)
	Input(context.Context, nativecontract.Input) (nativecontract.OperationResult, error)
	Interrupt(context.Context, nativecontract.Interrupt) (nativecontract.OperationResult, error)
	Stop(context.Context, nativecontract.Stop) (nativecontract.OperationResult, error)
}

// Ancestry can only update an already registered generation. Its implementation
// rechecks the source binding and immutable parent in the final transaction.
// There is intentionally no register/create operation here.
type Ancestry interface {
	UpdateExisting(context.Context, TaskEvent) error
}

type TaskEvent struct {
	Binding                nativecontract.Binding
	ParentHarnessSessionID string
	TaskRef, State         string
	Sequence               uint64
}

type Dispatcher struct {
	host      Host
	authority Authority
	ancestry  Ancestry
	validator *nativecontract.Validator
	now       func() time.Time
	mu        sync.Mutex
	workers   map[string]*Worker
	// Tests establish joins while the host is behind its pre-fork barrier.
	onWait func(string)
}

// Worker holds a frozen assignment, not a process PID or a transferable grant.
// One dispatcher belongs to one authenticated owner; reuse it for that owner's
// duplicate requests. Durable idempotency across restarts remains in C0.
type Worker struct {
	d         *Dispatcher
	request   Request
	target    Target
	digest    [32]byte
	done      chan struct{}
	mu        sync.Mutex
	result    nativecontract.OperationResult
	err       error
	held      bool
	uncertain bool
	task      TaskEvent
}

func New(host Host, authority Authority, ancestry Ancestry, now func() time.Time) (*Dispatcher, error) {
	if host == nil || authority == nil || ancestry == nil {
		return nil, refusal(nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Dispatcher{host: host, authority: authority, ancestry: ancestry, validator: v, now: now, workers: make(map[string]*Worker)}, nil
}

func refusal(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func validate(v *nativecontract.Validator, schema string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	return v.ValidateJSON(schema, raw)
}

// ValidateWorkerStart bounds the caller's strings before encoding or worktree
// resolution. Resume/after-exit are separate deliberate successor operations.
func ValidateWorkerStart(v *nativecontract.Validator, start nativecontract.LocalStart) error {
	i := start.Intent
	for _, s := range []string{i.RequestID, i.Harness, i.ComputerID, i.AccountID, i.WorkspaceID, i.ModelProfileID, i.PolicyID, start.AccountKey} {
		if len(s) > 128 {
			return refusal(nativecontract.ReasonBufferFull)
		}
	}
	if len(start.Cwd) > 4096 || i.WorkID != nil && len(*i.WorkID) > 36 || i.ParentHarnessSessionID != nil && len(*i.ParentHarnessSessionID) > 36 {
		return refusal(nativecontract.ReasonBufferFull)
	}
	if i.Role != "worker" || i.Profile != "native" || i.WorkID == nil || i.ParentHarnessSessionID == nil || !filepath.IsAbs(start.Cwd) {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	if start.AfterExit != nil || start.NativeResumeRef != nil || i.ResumeFromOwnedSessionID != nil {
		return refusal(nativecontract.ReasonUnsupported)
	}
	return validate(v, "NativeLocalStart", start)
}

func (d *Dispatcher) Dispatch(ctx context.Context, request Request) (*Worker, error) {
	if len(request.TenantID) != 36 || len(request.ProjectID) != 36 {
		return nil, refusal(nativecontract.ReasonInvalidInput)
	}
	if err := ValidateWorkerStart(d.validator, request.Start); err != nil {
		return nil, err
	}
	// Freeze caller-owned pointers before the resolver or another goroutine can
	// retain them. No credentials are present in this C0 local start DTO.
	raw, _ := json.Marshal(request)
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, refusal(nativecontract.ReasonInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	target, err := d.authority.ResolveWorker(ctx, request)
	if err != nil {
		return nil, err
	}
	if err = d.checkTarget(request, target); err != nil {
		return nil, err
	}
	target.Actor.KeyCeiling = append([]string(nil), target.Actor.KeyCeiling...)
	// Actor is deliberately local-only. Hash its explicit immutable authority
	// fields, including ceiling/owner, rather than trying to serialize Actor.
	fingerprint, _ := json.Marshal(struct {
		Request                       Request
		Principal, Kind, Owner, Grant string
		Revision                      int64
		Ceiling                       []string
		Policy                        nativecontract.Policy
	}{request, target.Actor.PrincipalID, target.Actor.PrincipalKind, target.Actor.OwnerPersonID, target.Actor.GrantID, target.Actor.GrantRevision, target.Actor.KeyCeiling, target.Policy})
	digest := sha256.Sum256(fingerprint)
	key := request.TenantID + "/" + request.Start.Intent.RequestID
	d.mu.Lock()
	if old := d.workers[key]; old != nil {
		d.mu.Unlock()
		if old.digest != digest {
			return nil, refusal(nativecontract.ReasonIdentityMismatch)
		}
		if d.onWait != nil {
			d.onWait("duplicate")
		}
		return old.wait(ctx)
	}
	if len(d.workers) >= maxTracked {
		d.mu.Unlock()
		return nil, refusal(nativecontract.ReasonBufferFull)
	}
	w := &Worker{d: d, request: request, target: target, digest: digest, done: make(chan struct{}), held: true}
	d.workers[key] = w
	d.mu.Unlock()
	// Registering/claiming/spawning is ONE host operation, never a second
	// harness registration followed by a subprocess launched by the dispatcher.
	result, err := d.host.Start(ctx, request.Start)
	if err == nil {
		err = w.checkResult(result, "start", request.Start.Intent.RequestID, nil)
	}
	w.mu.Lock()
	w.result, w.err = result, err
	w.uncertain = err != nil || result.Receipt.State == "uncertain"
	if err == nil && result.Receipt.State == "uncertain" {
		w.err = receiptFailure(result.Receipt)
	}
	if err == nil && (result.Receipt.State == "rejected" || result.Receipt.State == "expired") {
		w.held = false // authenticated start receipt proves no launch
		w.err = receiptFailure(result.Receipt)
	}
	w.mu.Unlock()
	close(w.done)
	return w.wait(ctx)
}

func (d *Dispatcher) checkTarget(r Request, t Target) error {
	i := r.Start.Intent
	if !t.Leaf || t.WorkID != *i.WorkID || t.ParentHarnessSessionID != *i.ParentHarnessSessionID || t.WorkID == t.ParentHarnessSessionID {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	if t.Actor.TenantID != r.TenantID || t.Actor.PrincipalID == "" || t.Actor.OwnerPersonID == "" || t.Actor.GrantID != t.Policy.GrantID || t.Actor.GrantRevision != t.Policy.GrantRevision {
		return refusal(nativecontract.ReasonGrantMissing)
	}
	if err := validate(d.validator, "NativePolicy", t.Policy); err != nil {
		return err
	}
	if !d.now().Before(t.Policy.ExpiresAt) {
		return refusal(nativecontract.ReasonGrantExpired)
	}
	if len(t.Worktree) > 4096 || !filepath.IsAbs(t.Worktree) || len(t.Actor.KeyCeiling) > 128 {
		return refusal(nativecontract.ReasonBufferFull)
	}
	for _, s := range append([]string{t.Actor.PrincipalID, t.Actor.OwnerPersonID, t.Actor.GrantID, t.Actor.PrincipalKind}, t.Actor.KeyCeiling...) {
		if len(s) > 128 {
			return refusal(nativecontract.ReasonBufferFull)
		}
	}
	assigned, err := filepath.EvalSymlinks(t.Worktree)
	if err != nil {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	cwd, err := filepath.EvalSymlinks(r.Start.Cwd)
	if err != nil || cwd != assigned {
		return refusal(nativecontract.ReasonPermissionRevoked)
	}
	return nil
}

func (w *Worker) wait(ctx context.Context) (*Worker, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.done:
		w.mu.Lock()
		defer w.mu.Unlock()
		return w, w.err
	}
}

func sameBinding(a, b nativecontract.Binding) bool { return reflect.DeepEqual(a, b) }

func (w *Worker) checkResult(r nativecontract.OperationResult, operation, requestID string, binding *nativecontract.Binding) error {
	if err := validate(w.d.validator, "NativeOperationResult", r); err != nil {
		return err
	}
	if r.Receipt.RequestID != requestID || r.Receipt.Operation != operation {
		return refusal(nativecontract.ReasonIdentityMismatch)
	}
	if operation == "start" {
		if r.Session == nil || r.Session.Role != "worker" || r.Session.Harness != w.request.Start.Intent.Harness || r.Session.OwnerPersonID != w.target.Actor.OwnerPersonID {
			return refusal(nativecontract.ReasonIdentityMismatch)
		}
		binding = r.Session.CurrentBinding
		if binding == nil && r.Receipt.State != "accepted" && r.Receipt.State != "rejected" && r.Receipt.State != "expired" {
			return refusal(nativecontract.ReasonIdentityMismatch)
		}
	}
	if binding != nil {
		if binding.TenantID != w.request.TenantID || binding.ProjectID != w.request.ProjectID || binding.RunID == nil || r.Receipt.Binding == nil || !sameBinding(*binding, *r.Receipt.Binding) || r.Session != nil && (r.Session.CurrentBinding == nil || !sameBinding(*binding, *r.Session.CurrentBinding)) {
			return refusal(nativecontract.ReasonIdentityMismatch)
		}
	}
	return nil
}

func receiptFailure(r nativecontract.Receipt) error {
	if r.Reason != nil {
		return refusal(*r.Reason)
	}
	return refusal(nativecontract.ReasonNativeFailure)
}

// Result returns a copy: callers cannot rebind later inbox/control operations.
func (w *Worker) Result() nativecontract.OperationResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	raw, _ := json.Marshal(w.result)
	var out nativecontract.OperationResult
	_ = json.Unmarshal(raw, &out)
	return out
}

// Reservation is a monitoring projection of this dispatch attempt, not another
// admission ledger. Server worker runs are counted once; native root sidecars
// contribute additional occupancy only when run_id IS NULL (C0/AEON-1251).
type Reservation struct {
	TenantID, AccountID, ComputerID, WorkspaceID, WorkID, ParentHarnessSessionID string
	Held, Uncertain                                                              bool
}

func (w *Worker) Reservation() Reservation {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := w.request.Start.Intent
	return Reservation{w.request.TenantID, i.AccountID, i.ComputerID, i.WorkspaceID, *i.WorkID, *i.ParentHarnessSessionID, w.held, w.uncertain}
}

// Forget reclaims bounded local bookkeeping only after confirmed no-fork or
// owned stop. Future replay still goes through durable server idempotency.
func (d *Dispatcher) Forget(w *Worker) error {
	if w == nil || w.d != d {
		return refusal(nativecontract.ReasonIdentityMismatch)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held {
		return refusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	key := w.request.TenantID + "/" + w.request.Start.Intent.RequestID
	if d.workers[key] == w {
		delete(d.workers, key)
	}
	return nil
}

// ChildEvent waits for the registered generation when it races with Start.
// Events from hooks must take this same path for owned children, not register
// a second unmanaged child. A task ending is never process exit evidence.
func (d *Dispatcher) ChildEvent(ctx context.Context, tenantID, requestID string, event TaskEvent) error {
	d.mu.Lock()
	w := d.workers[tenantID+"/"+requestID]
	d.mu.Unlock()
	if w == nil {
		return refusal(nativecontract.ReasonIdentityMismatch)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if d.onWait != nil {
		d.onWait("child_event")
	}
	if _, err := w.wait(ctx); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.result.Session == nil || w.result.Session.CurrentBinding == nil || !sameBinding(event.Binding, *w.result.Session.CurrentBinding) || event.ParentHarnessSessionID != w.target.ParentHarnessSessionID {
		return refusal(nativecontract.ReasonIdentityMismatch)
	}
	if event.Sequence == 0 || len(event.TaskRef) == 0 || len(event.TaskRef) > 256 || strings.ContainsRune(event.TaskRef, 0) || (event.State != "started" && event.State != "progress" && event.State != "completed" && event.State != "failed") {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	if event.Sequence < w.task.Sequence {
		return nil
	}
	if event.Sequence == w.task.Sequence {
		if !reflect.DeepEqual(event, w.task) {
			return refusal(nativecontract.ReasonIdentityMismatch)
		}
		return nil
	}
	if err := d.ancestry.UpdateExisting(ctx, event); err != nil {
		return err
	}
	w.task = event
	return nil
}

// command captures the registered runtime. Host performs the final ownership,
// permission and monotonic deadline check under its existing native write lock.
func (w *Worker) command(ctx context.Context, binding nativecontract.Binding, schema, operation, requestID string, value any, send func(context.Context) (nativecontract.OperationResult, error)) (nativecontract.OperationResult, error) {
	if err := validate(w.d.validator, schema, value); err != nil {
		return nativecontract.OperationResult{}, err
	}
	w.mu.Lock()
	current := w.result.Session != nil && w.result.Session.CurrentBinding != nil && sameBinding(binding, *w.result.Session.CurrentBinding) && w.held
	w.mu.Unlock()
	if !current {
		return nativecontract.OperationResult{}, refusal(nativecontract.ReasonWrongGeneration)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := send(ctx)
	if err == nil {
		err = w.checkResult(out, operation, requestID, &binding)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.uncertain = true
		return out, err
	}
	if out.Receipt.State == "uncertain" {
		w.uncertain = true
	}
	if operation == "stop" && out.Receipt.State == "exit_confirmed" {
		w.held, w.uncertain = false, false
	}
	if out.Receipt.State == "uncertain" || out.Receipt.State == "rejected" || out.Receipt.State == "expired" {
		return out, receiptFailure(out.Receipt)
	}
	return out, nil
}

func (w *Worker) Input(ctx context.Context, in nativecontract.Input) (nativecontract.OperationResult, error) {
	if len(in.Text) > 65536 {
		return nativecontract.OperationResult{}, refusal(nativecontract.ReasonBufferFull)
	}
	return w.command(ctx, in.ExpectedBinding, "NativeInput", "input", in.RequestID, in, func(ctx context.Context) (nativecontract.OperationResult, error) { return w.d.host.Input(ctx, in) })
}

func (w *Worker) Interrupt(ctx context.Context, in nativecontract.Interrupt) (nativecontract.OperationResult, error) {
	return w.command(ctx, in.ExpectedBinding, "NativeInterrupt", "interrupt", in.RequestID, in, func(ctx context.Context) (nativecontract.OperationResult, error) { return w.d.host.Interrupt(ctx, in) })
}

func (w *Worker) Stop(ctx context.Context, in nativecontract.Stop) (nativecontract.OperationResult, error) {
	return w.command(ctx, in.ExpectedBinding, "NativeStop", "stop", in.RequestID, in, func(ctx context.Context) (nativecontract.OperationResult, error) { return w.d.host.Stop(ctx, in) })
}
