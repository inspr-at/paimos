// SPDX-License-Identifier: AGPL-3.0-only

// Package nativeinteraction holds live native callbacks. Nothing in this
// registry survives a host restart or can grant a permission-mode expansion.
package nativeinteraction

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// Authority verifies the exact server-issued decision and current native
// turn/grant under the host's owned-writer fence. It must not infer approval
// from a message, a public ID, or a saved decision. It performs no native I/O.
// The host holds its writer fence across Decide (and Activate/Close).
type Authority interface {
	Authorize(context.Context, nativecontract.Actor, nativecontract.Approval, nativecontract.ApprovalDecision) error
}

type Outcome struct {
	Decision string
	Answer   *string
	Reason   nativecontract.Reason
}

type pending struct {
	approval nativecontract.Approval
	deadline time.Time // Now carries a monotonic coordinate in production.
	result   chan Outcome
}

type Broker struct {
	mu        sync.Mutex
	authority Authority
	now       func() time.Time
	validator *nativecontract.Validator
	binding   *nativecontract.Binding
	pending   map[string]pending
	seen      map[string]bool
}

type Failure struct {
	Status int
	Reason nativecontract.Reason
}

func (e *Failure) Error() string                          { return string(e.Reason) }
func (e *Failure) HTTPStatus() int                        { return e.Status }
func Fail(status int, reason nativecontract.Reason) error { return &Failure{status, reason} }

func New(authority Authority, now func() time.Time) (*Broker, error) {
	if authority == nil {
		return nil, Fail(503, nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Broker{authority: authority, now: now, validator: v, pending: make(map[string]pending), seen: make(map[string]bool)}, nil
}

// Activate is explicit runtime ownership, never viewer attach/reconnect.
// Every activation, even an equal binding after a restart, expires callbacks.
func (b *Broker) Activate(binding nativecontract.Binding) error {
	if err := Validate(b.validator, "NativeBinding", binding); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireAll()
	clear(b.seen)
	copy := CloneBinding(binding)
	b.binding = &copy
	return nil
}

func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireAll()
	b.binding = nil
}

func (b *Broker) expireAll() {
	for id, p := range b.pending {
		p.result <- Outcome{Decision: "deny", Reason: nativecontract.ReasonApprovalExpired}
		close(p.result)
		delete(b.pending, id)
	}
}

// Expire is driven by the owning host's injected-clock lifecycle tick. A live
// waiting callback receives deny at its deadline even without another request.
func (b *Broker) Expire() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	now, count := b.now(), 0
	for id, p := range b.pending {
		if !now.Before(p.deadline) {
			p.result <- Outcome{Decision: "deny", Reason: nativecontract.ReasonApprovalExpired}
			close(p.result)
			delete(b.pending, id)
			count++
		}
	}
	return count
}

// Register accepts only privacy-filtered metadata already routed to the desk.
// requestStarted is captured BEFORE the server request, so transit time is
// subtracted from the server-derived duration; wall-clock expires_at is audit.
func (b *Broker) Register(a nativecontract.Approval, requestStarted time.Time) (<-chan Outcome, error) {
	if len(a.Summary) > 2048 || len(a.AllowedDecisions) > 3 {
		return nil, Fail(413, nativecontract.ReasonBufferFull)
	}
	if err := Validate(b.validator, "NativeApproval", a); err != nil {
		return nil, err
	}
	if a.DeskItemID == nil || !ValidChoices(a) {
		return nil, Fail(403, nativecontract.ReasonGrantMissing)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.binding == nil || !SameBinding(*b.binding, a.Binding) {
		return nil, Fail(409, nativecontract.ReasonWrongGeneration)
	}
	now := b.now()
	deadline := requestStarted.Add(time.Duration(a.ExpiresInMS) * time.Millisecond)
	if requestStarted.IsZero() || requestStarted.After(now) || !now.Before(deadline) {
		return nil, Fail(410, nativecontract.ReasonApprovalExpired)
	}
	for id, p := range b.pending {
		if !now.Before(p.deadline) {
			p.result <- Outcome{Decision: "deny", Reason: nativecontract.ReasonApprovalExpired}
			close(p.result)
			delete(b.pending, id)
		}
	}
	if b.seen[a.ApprovalID] {
		return nil, Fail(409, nativecontract.ReasonIdentityMismatch)
	}
	if len(b.pending) >= 32 {
		return nil, Fail(429, nativecontract.ReasonBufferFull)
	}
	// Bound callback tombstones for the whole runtime. Refuse rather than
	// forget a settled ID and make a replayed native request executable.
	if len(b.seen) >= 512 {
		return nil, Fail(429, nativecontract.ReasonBufferFull)
	}
	a.Binding = CloneBinding(a.Binding)
	a.AllowedDecisions = slices.Clone(a.AllowedDecisions)
	desk := *a.DeskItemID
	a.DeskItemID = &desk
	result := make(chan Outcome, 1)
	b.pending[a.ApprovalID] = pending{approval: a, deadline: deadline, result: result}
	b.seen[a.ApprovalID] = true
	return result, nil
}

// Decide resolves a callback once. A server acceptance/queue receipt alone is
// insufficient: Authority must verify the final claim under the writer fence.
func (b *Broker) Decide(ctx context.Context, actor nativecontract.Actor, d nativecontract.ApprovalDecision) error {
	if d.Answer != nil && len(*d.Answer) > 8192 {
		return Fail(413, nativecontract.ReasonBufferFull)
	}
	if err := Validate(b.validator, "NativeApprovalDecision", d); err != nil {
		return err
	}
	if actor.PrincipalKind != "person" {
		return Fail(403, nativecontract.ReasonWrongPrincipal)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending[d.ApprovalID]
	if !ok {
		return Fail(410, nativecontract.ReasonApprovalExpired)
	}
	if b.binding == nil || !SameBinding(*b.binding, d.ExpectedBinding) || !SameBinding(p.approval.Binding, d.ExpectedBinding) || p.approval.EffectDigest != d.EffectDigest {
		return Fail(409, nativecontract.ReasonWrongGeneration)
	}
	if actor.TenantID != d.ExpectedBinding.TenantID {
		return Fail(403, nativecontract.ReasonWrongInstance)
	}
	if !b.now().Before(p.deadline) {
		return Fail(410, nativecontract.ReasonApprovalExpired)
	}
	if !slices.Contains(p.approval.AllowedDecisions, d.Decision) {
		return Fail(400, nativecontract.ReasonUnsupported)
	}
	if err := b.authority.Authorize(ctx, actor, p.approval, d); err != nil {
		return err
	}
	// Verification may take time. Never deliver an allow after its deadline.
	if err := ctx.Err(); err != nil {
		return err
	}
	if !b.now().Before(p.deadline) {
		return Fail(410, nativecontract.ReasonApprovalExpired)
	}
	var answer *string
	if d.Answer != nil {
		copy := *d.Answer
		answer = &copy
	}
	delete(b.pending, d.ApprovalID)
	p.result <- Outcome{Decision: d.Decision, Answer: answer}
	close(p.result)
	return nil
}

// Validate bounds the marshalled shape before the C0 schema validator. Callers
// bound variable text/lists BEFORE calling this helper.
func Validate(v *nativecontract.Validator, name string, value any) error {
	// Bound typed ingress before MarshalJSON can allocate. Schema validation
	// then enforces exact formats/variants, including optional answer fields.
	var binding *nativecontract.Binding
	var stringsToBound []string
	switch in := value.(type) {
	case nativecontract.Binding:
		binding = &in
	case nativecontract.Approval:
		binding = &in.Binding
		stringsToBound = []string{in.RequestID, in.ApprovalID, in.TurnRef, in.NativeRequestRef, in.EffectDigest, in.Kind}
		if len(in.Summary) > 2048 || len(in.AllowedDecisions) > 3 {
			return Fail(413, nativecontract.ReasonBufferFull)
		}
		for _, choice := range in.AllowedDecisions {
			if len(choice) > 16 {
				return Fail(413, nativecontract.ReasonBufferFull)
			}
		}
		if in.DeskItemID != nil {
			stringsToBound = append(stringsToBound, *in.DeskItemID)
		}
	case nativecontract.ApprovalDecision:
		binding = &in.ExpectedBinding
		stringsToBound = []string{in.RequestID, in.ApprovalID, in.EffectDigest, in.Decision}
		if in.Answer != nil && len(*in.Answer) > 8192 {
			return Fail(413, nativecontract.ReasonBufferFull)
		}
		if in.StepUpAuthorizationID != nil {
			stringsToBound = append(stringsToBound, *in.StepUpAuthorizationID)
		}
	case nativecontract.AttachIntent:
		binding = &in.ExpectedBinding
		stringsToBound = []string{in.RequestID, in.ClientID, in.Mode, in.View}
		if in.ConsentID != nil {
			stringsToBound = append(stringsToBound, *in.ConsentID)
		}
	case nativecontract.AttachRenew:
		binding = &in.ExpectedBinding
		stringsToBound = []string{in.RequestID, in.ViewerID}
		if in.ControllerEpoch != nil {
			stringsToBound = append(stringsToBound, *in.ControllerEpoch)
		}
	case nativecontract.Detach:
		binding = &in.ExpectedBinding
		stringsToBound = []string{in.RequestID, in.ViewerID}
	case nativecontract.EndControl:
		binding = &in.ExpectedBinding
		stringsToBound = []string{in.RequestID, in.ControllerEpoch}
		if len(in.Reason) > 512 {
			return Fail(413, nativecontract.ReasonBufferFull)
		}
	}
	if binding != nil {
		stringsToBound = append(stringsToBound, binding.TenantID, binding.ProjectID, binding.OwnedSessionID, binding.HarnessSessionID, binding.RuntimeEpoch, binding.DaemonID, binding.DaemonGeneration)
		if binding.RunID != nil {
			stringsToBound = append(stringsToBound, *binding.RunID)
		}
	}
	for _, value := range stringsToBound {
		if len(value) > 128 {
			return Fail(413, nativecontract.ReasonBufferFull)
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Fail(400, nativecontract.ReasonInvalidInput)
	}
	return v.ValidateJSON(name, raw)
}

func SameBinding(a, b nativecontract.Binding) bool { return reflect.DeepEqual(a, b) }
func CloneBinding(b nativecontract.Binding) nativecontract.Binding {
	if b.RunID != nil {
		id := *b.RunID
		b.RunID = &id
	}
	return b
}

func ValidChoices(a nativecontract.Approval) bool {
	if a.Kind == "question" {
		return slices.Equal(a.AllowedDecisions, []string{"answer", "deny"})
	}
	return slices.Equal(a.AllowedDecisions, []string{"allow_once", "deny"})
}
