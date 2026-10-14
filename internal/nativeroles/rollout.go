// SPDX-License-Identifier: AGPL-3.0-only

package nativeroles

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativequalification"
)

// Scope is authenticated transport context, never decoded from role flags.
type Scope struct {
	Instance, ProjectID string
	Actor               nativecontract.Actor
}

// Evidence is resolved from the role owner's qualification records. A fixture
// or another role's live run cannot create it. IDs contain no transcript data.
type Evidence struct {
	Role, Instance, TenantID, PrincipalID, ComputerID, AccountID, WorkspaceID string
	Platform, ToolPolicyDigest, SettingsPolicyDigest                          string
	RestrictedRunID, NativeRunID, LeadSoakID, RoleApprovalID                  string
	OperatorApprovalID, TerminalExportID, TakeApprovalID                      string
	Capabilities                                                              nativecontract.Capabilities
}

// Grant is an explicit current role grant, supplied only by Authority. Tool
// names are an allowlist, not inherited from Recipe, a display name or LEAD.
type Grant struct {
	Scope
	Role, ComputerID, AccountID, AccountKey, WorkspaceID, WorkspaceRoot string
	Platform                                                            string
	ModelProfileID, PolicyID, Harness, NativeHistoryGrantID             string
	DaemonID, DaemonGeneration                                          string
	Policy                                                              nativecontract.Policy
	Tools                                                               []string
	Evidence                                                            Evidence
	PairedLocally, OutboundRelay, Production                            bool
}

// Current is captured under the existing conversation writer fence. History
// proof is metadata only; approval callbacks still require their exact current
// runtime/effect and cannot be answered with predecessor approval authority.
type Current struct {
	Grant                 Grant
	PredecessorGrant      Grant // immutable source assignment, not re-derived from its name
	Predecessor           nativecontract.WriterRecord
	Revision              int64
	ApprovalHistoryDigest string
	Exit                  *nativecontract.ExitEvidence
}

// Request contains only the caller's expected identity and one frozen C0
// operation. Start is deliberate same-conversation after-exit cutover; Resume
// is recovery of an already exited exact generation, never a new conversation.
type Request struct {
	Role                  string
	ExpectedBinding       nativecontract.Binding
	ExpectedRevision      int64
	ApprovalHistoryDigest string
	Start                 *nativecontract.LocalStart
	Resume                *nativecontract.ResumeIntent
}

// Route is the existing owned-session path. It must recheck current server
// authorization with RequireTx in the final claim, then admission, process
// provenance and the local writer lock before possible fork. Start must retain
// the after-exit barrier and uncertain occupancy; it never opens a second writer.
type Route interface {
	Start(context.Context, nativecontract.LocalStart) (nativecontract.OperationResult, error)
	Resume(context.Context, nativecontract.ResumeIntent) (nativecontract.OperationResult, error)
}

// Authority holds the existing LOCAL conversation fence across the callback,
// resolves the current immutable role/host/history grant, and supplies a Route
// bound to that authority and predecessor. It must call the callback exactly
// once on success; it is not a database transaction around network I/O. I2 owns
// the adapter. Without it, recipes are displayable but cutover is unavailable.
type Authority interface {
	WithCurrent(context.Context, Scope, string, func(Current, Route) error) error
}

type Switcher struct {
	authority Authority
	validator *nativecontract.Validator
	now       func() time.Time
}

func New(authority Authority, now func() time.Time) (*Switcher, error) {
	if authority == nil || now == nil {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	return &Switcher{authority: authority, validator: v, now: now}, nil
}

func refuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func (s *Switcher) validate(schema string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return refuse(nativecontract.ReasonInvalidInput)
	}
	if len(raw) > 65536 {
		return refuse(nativecontract.ReasonBufferFull)
	}
	return s.validator.ValidateJSON(schema, raw)
}

// Switch submits one exact operation. No stop, signal, account rebind, raw
// socket forwarding, transcript import or unrelated session enumeration exists.
func (s *Switcher) Switch(ctx context.Context, scope Scope, request Request) (nativecontract.OperationResult, error) {
	var result nativecontract.OperationResult
	if _, err := Lookup(request.Role); err != nil {
		return result, err
	}
	if err := bound(scope, request); err != nil {
		return result, err
	}
	if err := s.validate("NativeBinding", request.ExpectedBinding); err != nil {
		return result, err
	}
	if (request.Start == nil) == (request.Resume == nil) {
		return result, refuse(nativecontract.ReasonInvalidInput)
	}
	if request.Start != nil {
		if err := s.validate("NativeLocalStart", request.Start); err != nil {
			return result, err
		}
	} else if err := s.validate("NativeResumeIntent", request.Resume); err != nil {
		return result, err
	}
	// Freeze pointers before handing the request to asynchronous host code.
	raw, _ := json.Marshal(request)
	var frozen Request
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return result, refuse(nativecontract.ReasonInvalidInput)
	}
	request = frozen
	scope.Actor.KeyCeiling = append([]string(nil), scope.Actor.KeyCeiling...)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	called := false
	var callbackErr error
	err := s.authority.WithCurrent(ctx, scope, request.Role, func(current Current, route Route) error {
		callbackErr = s.apply(ctx, scope, request, current, route, &called, &result)
		return callbackErr
	})
	if err == nil {
		err = callbackErr
	}
	if err == nil && !called {
		err = refuse(nativecontract.ReasonQualificationMissing)
	}
	if err != nil {
		// A foreign/malformed host response must not leak its session/history
		// through a partial result. Reconcile the unchanged request ID instead.
		result = nativecontract.OperationResult{}
	}
	return result, err
}

func (s *Switcher) apply(ctx context.Context, scope Scope, request Request, current Current, route Route, called *bool, result *nativecontract.OperationResult) error {
	if *called || route == nil {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	*called = true
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkCurrent(scope, request, current); err != nil {
		return err
	}
	operation, requestID := "resume", ""
	var err error
	if request.Start != nil {
		operation, requestID = "start", request.Start.Intent.RequestID
		*result, err = route.Start(ctx, *request.Start)
	} else {
		requestID = request.Resume.RequestID
		*result, err = route.Resume(ctx, *request.Resume)
	}
	if err != nil {
		return err // possible effect remains unconfirmed; never retry here
	}
	return s.checkResult(current, *result, operation, requestID)
}

func bound(scope Scope, r Request) error {
	for _, value := range []string{scope.Instance, scope.ProjectID, scope.Actor.TenantID, scope.Actor.PrincipalID, scope.Actor.OwnerPersonID, scope.Actor.PrincipalKind, scope.Actor.GrantID,
		r.ExpectedBinding.TenantID, r.ExpectedBinding.ProjectID, r.ExpectedBinding.OwnedSessionID, r.ExpectedBinding.HarnessSessionID, r.ExpectedBinding.RuntimeEpoch, r.ExpectedBinding.DaemonID, r.ExpectedBinding.DaemonGeneration} {
		if len(value) == 0 || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return refuse(nativecontract.ReasonInvalidInput)
		}
	}
	if len(scope.Actor.KeyCeiling) > 128 {
		return refuse(nativecontract.ReasonBufferFull)
	}
	for _, value := range scope.Actor.KeyCeiling {
		if len(value) > 128 {
			return refuse(nativecontract.ReasonBufferFull)
		}
	}
	if r.ExpectedBinding.RunID != nil || r.ExpectedRevision < 0 || r.ExpectedRevision > 1<<53-1 || !digest(r.ApprovalHistoryDigest) {
		return refuse(nativecontract.ReasonInvalidInput)
	}
	if r.ExpectedBinding.TenantID != scope.Actor.TenantID || r.ExpectedBinding.ProjectID != scope.ProjectID {
		return refuse(nativecontract.ReasonWrongInstance)
	}
	if scope.Actor.PrincipalKind != "person" && scope.Actor.PrincipalKind != "agent" {
		return refuse(nativecontract.ReasonWrongPrincipal)
	}
	if r.Start != nil {
		x := r.Start
		if len(x.Cwd) > 4096 || len(x.AccountKey) > 128 || x.NativeResumeRef != nil && len(*x.NativeResumeRef) > 256 {
			return refuse(nativecontract.ReasonBufferFull)
		}
		for _, value := range []string{x.Intent.RequestID, x.Intent.Role, x.Intent.Profile, x.Intent.Harness, x.Intent.ComputerID, x.Intent.AccountID, x.Intent.WorkspaceID, x.Intent.ModelProfileID, x.Intent.PolicyID} {
			if len(value) > 128 {
				return refuse(nativecontract.ReasonBufferFull)
			}
		}
		if x.Intent.WorkID != nil || x.Intent.ParentHarnessSessionID != nil {
			return refuse(nativecontract.ReasonInvalidInput)
		}
		if x.Intent.ResumeFromOwnedSessionID != nil && len(*x.Intent.ResumeFromOwnedSessionID) > 36 || x.AfterExit != nil && len(x.AfterExit.ProcessIdentityDigest) > 64 {
			return refuse(nativecontract.ReasonBufferFull)
		}
	}
	if r.Resume != nil {
		x := r.Resume
		if len(x.RequestID) > 36 || len(x.PolicyID) > 36 || len(x.Reason) > 32 || !x.ExpectedBinding.Equal(r.ExpectedBinding) {
			return refuse(nativecontract.ReasonWrongGeneration)
		}
	}
	return nil
}

func (s *Switcher) checkCurrent(scope Scope, r Request, c Current) error {
	g := c.Grant
	if err := boundGrant(g); err != nil {
		return err
	}
	if g.Role != r.Role || g.Instance != scope.Instance || g.ProjectID != scope.ProjectID || g.Actor.TenantID != scope.Actor.TenantID {
		return refuse(nativecontract.ReasonWrongInstance)
	}
	if g.Actor.PrincipalID != scope.Actor.PrincipalID || g.Actor.OwnerPersonID != scope.Actor.OwnerPersonID || g.Actor.PrincipalKind != scope.Actor.PrincipalKind || !slices.Equal(g.Actor.KeyCeiling, scope.Actor.KeyCeiling) {
		return refuse(nativecontract.ReasonWrongPrincipal)
	}
	if g.Actor.GrantID != scope.Actor.GrantID || g.Actor.GrantRevision != scope.Actor.GrantRevision || g.Policy.GrantID != g.Actor.GrantID || g.Policy.GrantRevision != g.Actor.GrantRevision {
		return refuse(nativecontract.ReasonGrantMissing)
	}
	if err := s.validate("NativePolicy", g.Policy); err != nil {
		return err
	}
	if !s.now().Before(g.Policy.ExpiresAt) {
		return refuse(nativecontract.ReasonGrantExpired)
	}
	if !c.Predecessor.Binding.Equal(r.ExpectedBinding) || c.Revision != r.ExpectedRevision || c.Predecessor.Identity.RuntimeEpoch != r.ExpectedBinding.RuntimeEpoch {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	if !sameAssignment(g, c.PredecessorGrant) {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	if r.ExpectedBinding.DaemonID != g.DaemonID || r.ExpectedBinding.DaemonGeneration != g.DaemonGeneration || !g.PairedLocally || !g.OutboundRelay {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	if g.Policy.LocalHistory != "vendor_native" || !reference(g.NativeHistoryGrantID) || c.ApprovalHistoryDigest != r.ApprovalHistoryDigest || !reference(c.Predecessor.Identity.NativeSessionRef) {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	// The first recipes qualify the same Claude coordinator route as P1c.
	// Other providers require their own pins/history mapping before enablement.
	if g.Harness != "claude" || c.Predecessor.Identity.Provider != g.Harness {
		return refuse(nativecontract.ReasonUnsupported)
	}
	if _, err := nativequalification.SessionOverlay(g.Tools); err != nil {
		return refuse(nativecontract.ReasonPermissionRevoked)
	}
	if err := s.checkEvidence(g); err != nil {
		return err
	}
	if r.Start != nil {
		x, i := r.Start, r.Start.Intent
		if i.Role != "coordinator" || i.Profile != "native" || i.ComputerID != g.ComputerID || i.Harness != g.Harness || i.AccountID != g.AccountID ||
			i.WorkspaceID != g.WorkspaceID || x.AccountKey != g.AccountKey || i.ModelProfileID != g.ModelProfileID || i.PolicyID != g.PolicyID {
			return refuse(nativecontract.ReasonPermissionRevoked)
		}
		if len(g.WorkspaceRoot) > 4096 || !filepath.IsAbs(g.WorkspaceRoot) || filepath.Clean(g.WorkspaceRoot) != g.WorkspaceRoot || x.Cwd != g.WorkspaceRoot {
			return refuse(nativecontract.ReasonPermissionRevoked)
		}
		if x.NativeResumeRef == nil || *x.NativeResumeRef != c.Predecessor.Identity.NativeSessionRef || i.ResumeFromOwnedSessionID == nil || *i.ResumeFromOwnedSessionID != r.ExpectedBinding.OwnedSessionID {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		b, p := x.AfterExit, c.Predecessor.Identity
		if b == nil || p.PID < 2 || p.StartedAt.IsZero() || p.StartedAt.After(s.now()) || !c.Predecessor.PossibleFork || b.PID != int64(p.PID) || !b.ProcessStartedAt.Equal(p.StartedAt) || !digest(p.ProcessIdentityDigest) ||
			b.ProcessIdentityDigest != p.ProcessIdentityDigest || !s.now().Before(b.Deadline) || b.Deadline.After(g.Policy.ExpiresAt) || b.Deadline.Sub(s.now()) > 300*time.Second {
			return refuse(nativecontract.ReasonOwnershipUnconfirmed)
		}
	} else {
		if r.Resume.ExpectedRevision != r.ExpectedRevision {
			return refuse(nativecontract.ReasonWrongGeneration)
		}
		if r.Resume.PolicyID != g.PolicyID || !g.Evidence.Capabilities.Resume {
			return refuse(nativecontract.ReasonPermissionRevoked)
		}
		p, x := c.Predecessor.Identity, c.Exit
		if x == nil || p.PID < 2 || p.StartedAt.IsZero() || !c.Predecessor.PossibleFork || !c.Predecessor.ExitObserved || !x.RootExited || !x.GroupAbsent || !digest(p.ProcessIdentityDigest) ||
			x.ProcessIdentityDigest != p.ProcessIdentityDigest || x.ObservedAt.Before(p.StartedAt) || x.ObservedAt.After(s.now()) {
			return refuse(nativecontract.ReasonOwnershipUnconfirmed)
		}
	}
	return nil
}

func boundGrant(g Grant) error {
	for _, value := range []string{g.Role, g.Instance, g.ProjectID, g.Actor.TenantID, g.Actor.PrincipalID, g.Actor.OwnerPersonID, g.Actor.PrincipalKind, g.Actor.GrantID,
		g.ComputerID, g.AccountID, g.AccountKey, g.WorkspaceID, g.ModelProfileID, g.PolicyID, g.Harness, g.NativeHistoryGrantID, g.DaemonID, g.DaemonGeneration, g.Platform,
		g.Policy.GrantID, g.Policy.ToolPolicyDigest, g.Policy.SettingsPolicyDigest, g.Policy.PermissionPolicy, g.Policy.LocalHistory, g.Policy.Continuation} {
		if !reference(value) || len(value) > 128 {
			return refuse(nativecontract.ReasonInvalidInput)
		}
	}
	if len(g.WorkspaceRoot) > 4096 || len(g.Tools) > 13 {
		return refuse(nativecontract.ReasonBufferFull)
	}
	for _, tool := range g.Tools {
		if len(tool) > 128 {
			return refuse(nativecontract.ReasonBufferFull)
		}
	}
	return nil
}

func sameAssignment(a, b Grant) bool {
	return a.Role == b.Role && a.Instance == b.Instance && a.ProjectID == b.ProjectID && a.Actor.TenantID == b.Actor.TenantID &&
		a.Actor.PrincipalID == b.Actor.PrincipalID && a.Actor.OwnerPersonID == b.Actor.OwnerPersonID && a.Actor.PrincipalKind == b.Actor.PrincipalKind &&
		a.Actor.GrantID == b.Actor.GrantID && a.Actor.GrantRevision == b.Actor.GrantRevision && slices.Equal(a.Actor.KeyCeiling, b.Actor.KeyCeiling) &&
		a.ComputerID == b.ComputerID && a.Platform == b.Platform && a.Production == b.Production && a.AccountID == b.AccountID && a.AccountKey == b.AccountKey && a.WorkspaceID == b.WorkspaceID && a.WorkspaceRoot == b.WorkspaceRoot &&
		a.ModelProfileID == b.ModelProfileID && a.PolicyID == b.PolicyID && a.Harness == b.Harness && a.NativeHistoryGrantID == b.NativeHistoryGrantID &&
		a.Policy.ToolPolicyDigest == b.Policy.ToolPolicyDigest && a.Policy.SettingsPolicyDigest == b.Policy.SettingsPolicyDigest && slices.Equal(a.Tools, b.Tools)
}

func (s *Switcher) checkEvidence(g Grant) error {
	e := g.Evidence
	if e.Role != g.Role || e.Instance != g.Instance || e.TenantID != g.Actor.TenantID || e.PrincipalID != g.Actor.PrincipalID ||
		e.ComputerID != g.ComputerID || e.AccountID != g.AccountID || e.WorkspaceID != g.WorkspaceID ||
		e.ToolPolicyDigest != g.Policy.ToolPolicyDigest || e.SettingsPolicyDigest != g.Policy.SettingsPolicyDigest ||
		e.Platform != g.Platform || (e.Platform != "darwin" && e.Platform != "linux" && e.Platform != "windows") {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	for _, id := range []string{e.RestrictedRunID, e.NativeRunID, e.LeadSoakID, e.RoleApprovalID} {
		if !reference(id) {
			return refuse(nativecontract.ReasonQualificationMissing)
		}
	}
	if g.Production && !reference(e.OperatorApprovalID) || (e.Capabilities.TerminalView || e.Capabilities.Expert) && !reference(e.TerminalExportID) || (e.Capabilities.Take || e.Capabilities.TakeSupported) && !reference(e.TakeApprovalID) {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	if len(e.Capabilities.Steer) > 32 || len(e.Capabilities.Quota) > 64 || !digest(e.Capabilities.QualificationDigest) {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	return s.validate("NativeCapabilities", e.Capabilities)
}

func (s *Switcher) checkResult(c Current, out nativecontract.OperationResult, operation, requestID string) error {
	if err := s.validate("NativeOperationResult", out); err != nil {
		return err
	}
	r, view, previous := out.Receipt, out.Session, c.Predecessor.Binding
	if r.RequestID != requestID || r.Operation != operation {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	if r.State == "uncertain" || r.State == "rejected" || r.State == "expired" {
		if r.Reason != nil {
			return refuse(*r.Reason)
		}
		return refuse(nativecontract.ReasonNativeFailure)
	}
	if r.State != "accepted" && r.State != "queued" && r.State != "applied" {
		return refuse(nativecontract.ReasonNativeFailure)
	}
	if view == nil || view.OwnedSessionID != previous.OwnedSessionID || view.OwnerPersonID != c.Grant.Actor.OwnerPersonID || view.Role != "coordinator" || view.Profile != "native" ||
		view.Harness != c.Grant.Harness || view.ConversationRef != c.Predecessor.Identity.NativeSessionRef || !capabilitiesWithin(view.Capabilities, c.Grant.Evidence.Capabilities) {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	if c.Grant.Actor.PrincipalKind != "person" && (view.TerminalViewerURL != nil || view.Capabilities.Expert || view.Capabilities.TerminalView || view.Capabilities.Take || view.Capabilities.TakeSupported) {
		return refuse(nativecontract.ReasonWrongPrincipal)
	}
	if view.CurrentBinding == nil {
		if r.State == "applied" || r.Binding != nil {
			return refuse(nativecontract.ReasonWrongGeneration)
		}
		return nil
	}
	b := *view.CurrentBinding
	if b.TenantID != previous.TenantID || b.ProjectID != previous.ProjectID || b.OwnedSessionID != previous.OwnedSessionID || b.RunID != nil ||
		b.DaemonID != c.Grant.DaemonID || b.DaemonGeneration != c.Grant.DaemonGeneration || r.Binding == nil || !b.Equal(*r.Binding) {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	if b.Equal(previous) && r.State != "applied" {
		return nil // an accepted/queued intent is not a completed cutover
	}
	if b.HarnessSessionID == previous.HarnessSessionID || b.RuntimeEpoch == previous.RuntimeEpoch || b.BindingRevision <= previous.BindingRevision ||
		view.PredecessorHarnessSessionID == nil || *view.PredecessorHarnessSessionID != previous.HarnessSessionID {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	return nil
}

func capabilitiesWithin(a, b nativecontract.Capabilities) bool {
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	for i := 0; i < av.NumField(); i++ {
		if av.Field(i).Kind() == reflect.Bool && av.Field(i).Bool() && !bv.Field(i).Bool() {
			return false
		}
	}
	return a.QualificationDigest == b.QualificationDigest && (a.Steer == "unsupported" || a.Steer == b.Steer) && (a.Quota == "unsupported" || a.Quota == b.Quota)
}

func reference(s string) bool {
	return len(s) > 0 && len(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n")
}
func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

// These local authority/history records cannot become CLI/public DTOs.
func (Grant) MarshalJSON() ([]byte, error)   { return nil, refuse(nativecontract.ReasonWrongPrincipal) }
func (*Grant) UnmarshalJSON([]byte) error    { return refuse(nativecontract.ReasonWrongPrincipal) }
func (Current) MarshalJSON() ([]byte, error) { return nil, refuse(nativecontract.ReasonWrongPrincipal) }
func (*Current) UnmarshalJSON([]byte) error  { return refuse(nativecontract.ReasonWrongPrincipal) }
