// SPDX-License-Identifier: AGPL-3.0-only

package nativeroles

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

func roleID(n byte) string {
	return "10000000-0000-4000-8000-0000000000" + string([]byte{'0' + n/10, '0' + n%10})
}

type roleAuthority struct {
	current Current
	route   *roleRoute
	calls   int
}

func (a *roleAuthority) WithCurrent(ctx context.Context, _ Scope, _ string, apply func(Current, Route) error) error {
	a.calls++
	return apply(a.current, a.route)
}

// The fake route models the existing owning daemon: it retains occupancy and
// blocks possible fork until the test proves source exit at an explicit barrier.
type roleRoute struct {
	mu              sync.Mutex
	current         Current
	calls, launches int
	live            map[string]bool
	entered, exited chan struct{}
	revoked         bool
	responseState   string
	corrupt         func(*nativecontract.OperationResult)
	failure         error
	start           nativecontract.LocalStart
	resume          nativecontract.ResumeIntent
}

func (r *roleRoute) Start(ctx context.Context, in nativecontract.LocalStart) (nativecontract.OperationResult, error) {
	r.mu.Lock()
	r.calls++
	r.start = in
	r.mu.Unlock()
	if r.entered != nil {
		close(r.entered)
		select {
		case <-r.exited:
		case <-ctx.Done():
			return nativecontract.OperationResult{}, ctx.Err()
		}
	}
	return r.finish("start", in.Intent.RequestID)
}

func (r *roleRoute) Resume(_ context.Context, in nativecontract.ResumeIntent) (nativecontract.OperationResult, error) {
	r.mu.Lock()
	r.calls++
	r.resume = in
	r.mu.Unlock()
	return r.finish("resume", in.RequestID)
}

func (r *roleRoute) finish(operation, requestID string) (nativecontract.OperationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure != nil {
		return nativecontract.OperationResult{}, r.failure
	}
	if r.revoked {
		return nativecontract.OperationResult{}, refuse(nativecontract.ReasonPermissionRevoked)
	}
	c, state := r.current, r.responseState
	if state == "" {
		state = "applied"
	}
	b := c.Predecessor.Binding
	old := b.HarnessSessionID
	if state == "applied" {
		if r.live[b.OwnedSessionID] {
			return nativecontract.OperationResult{}, refuse(nativecontract.ReasonOwnershipUnconfirmed)
		}
		r.launches++
		r.live[b.OwnedSessionID] = true
		b.HarnessSessionID, b.RuntimeEpoch, b.BindingRevision = roleID(31), roleID(32), b.BindingRevision+1
	}
	out := nativecontract.OperationResult{
		Receipt: nativecontract.Receipt{RequestID: requestID, OperationID: roleID(30), Binding: &b, Operation: operation, State: state, SourceSequence: "1", UpdatedAt: c.Grant.Policy.ExpiresAt.Add(-time.Hour)},
		Session: &nativecontract.SessionView{OwnedSessionID: b.OwnedSessionID, CurrentBinding: &b, OwnerPersonID: c.Grant.Actor.OwnerPersonID, Role: "coordinator", Profile: "native", Harness: "claude",
			State: "running", Mode: "structured", Revision: c.Revision + 1, Generation: 2, ConversationRef: c.Predecessor.Identity.NativeSessionRef, PredecessorHarnessSessionID: &old, Capabilities: c.Grant.Evidence.Capabilities},
	}
	if r.corrupt != nil {
		r.corrupt(&out)
	}
	return out, nil
}

func roleFixture(t *testing.T, role string) (*Switcher, Scope, Request, *roleAuthority) {
	t.Helper()
	now := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	index := map[string]byte{"Tick": 11, "OPS": 12, "AIT": 13}[role]
	actor := nativecontract.Actor{TenantID: roleID(1), PrincipalID: roleID(index), OwnerPersonID: roleID(3), PrincipalKind: "agent", GrantID: roleID(index + 10), GrantRevision: 4, KeyCeiling: []string{"harness.read"}}
	scope := Scope{Instance: "ppm", ProjectID: roleID(4), Actor: actor}
	policy := nativecontract.Policy{GrantID: actor.GrantID, GrantRevision: actor.GrantRevision, ExpiresAt: now.Add(time.Hour), ToolPolicyDigest: strings.Repeat("a", 64), SettingsPolicyDigest: strings.Repeat("b", 64),
		PermissionPolicy: "bounded_preapproved", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 100, MaximumTokens: 10000}
	grant := Grant{Scope: scope, Role: role, ComputerID: roleID(5), AccountID: roleID(index + 40), AccountKey: "owner-" + role, WorkspaceID: role + "-worktree", WorkspaceRoot: t.TempDir(),
		ModelProfileID: roleID(7), PolicyID: roleID(8), Harness: "claude", NativeHistoryGrantID: roleID(9), DaemonID: "paired", DaemonGeneration: "daemon-1", Policy: policy,
		Tools: []string{"aeon_read", "aeon_comment"}, PairedLocally: true, OutboundRelay: true, Platform: "darwin"}
	grant.Evidence = Evidence{Role: role, Instance: scope.Instance, TenantID: actor.TenantID, PrincipalID: actor.PrincipalID, ComputerID: grant.ComputerID, AccountID: grant.AccountID, WorkspaceID: grant.WorkspaceID,
		Platform: "darwin", ToolPolicyDigest: policy.ToolPolicyDigest, SettingsPolicyDigest: policy.SettingsPolicyDigest, RestrictedRunID: role + "-restricted", NativeRunID: role + "-native", LeadSoakID: "48h-soak", RoleApprovalID: role + "-approval",
		Capabilities: nativecontract.Capabilities{Input: true, Stop: true, Resume: true, EventsSupported: true, ChatSupported: true, CorrelatedReceipts: true, Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("c", 64)}}
	b := nativecontract.Binding{TenantID: actor.TenantID, ProjectID: scope.ProjectID, OwnedSessionID: roleID(10), HarnessSessionID: roleID(14), RuntimeEpoch: roleID(15), DaemonID: grant.DaemonID, DaemonGeneration: grant.DaemonGeneration, BindingRevision: 2}
	identity := nativecontract.NativeIdentity{Provider: "claude", NativeSessionRef: roleID(16), NativeThreadRef: "thread", ResumeCursor: "cursor", RuntimeEpoch: b.RuntimeEpoch, ProcessIdentityDigest: strings.Repeat("d", 64), PID: 123, StartedAt: now.Add(-time.Hour)}
	current := Current{Grant: grant, PredecessorGrant: grant, Predecessor: nativecontract.WriterRecord{Binding: b, Identity: identity, PossibleFork: true}, Revision: 3, ApprovalHistoryDigest: strings.Repeat("e", 64)}
	resume, owned := identity.NativeSessionRef, b.OwnedSessionID
	request := Request{Role: role, ExpectedBinding: b, ExpectedRevision: current.Revision, ApprovalHistoryDigest: current.ApprovalHistoryDigest,
		Start: &nativecontract.LocalStart{Intent: nativecontract.StartIntent{RequestID: roleID(17), ExpectedProjectRevision: 1, Role: "coordinator", Profile: "native", Harness: "claude", ComputerID: grant.ComputerID,
			AccountID: grant.AccountID, WorkspaceID: grant.WorkspaceID, ModelProfileID: grant.ModelProfileID, PolicyID: grant.PolicyID, ResumeFromOwnedSessionID: &owned}, Cwd: grant.WorkspaceRoot, AccountKey: grant.AccountKey, NativeResumeRef: &resume,
			AfterExit: &nativecontract.ExitBarrier{PID: int64(identity.PID), ProcessStartedAt: identity.StartedAt, ProcessIdentityDigest: identity.ProcessIdentityDigest, Deadline: now.Add(time.Minute)}}}
	route := &roleRoute{current: current, live: map[string]bool{b.OwnedSessionID: false, roleID(90): true}}
	a := &roleAuthority{current: current, route: route}
	switcher, err := New(a, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return switcher, scope, request, a
}

func requireReason(t *testing.T, err error, want nativecontract.Reason) {
	t.Helper()
	var validation *nativecontract.ValidationError
	if !errors.As(err, &validation) || validation.Reason != want {
		t.Fatalf("wrong refusal: got %v, want %s", err, want)
	}
}

// Risk: one role borrows another actor/workspace/account/grant or stale runtime
// and starts a second writer while disrupting unrelated live coordinators.
func TestNativeRoleSwitchFencesAndSingleWriter(t *testing.T) {
	for _, role := range []string{"Tick", "OPS", "AIT"} {
		t.Run(role, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				change func(*Scope, *Request, *Current)
				reason nativecontract.Reason
			}{
				{"tenant", func(s *Scope, _ *Request, _ *Current) { s.Actor.TenantID = roleID(80) }, nativecontract.ReasonWrongInstance},
				{"project", func(s *Scope, _ *Request, _ *Current) { s.ProjectID = roleID(80) }, nativecontract.ReasonWrongInstance},
				{"instance", func(s *Scope, _ *Request, _ *Current) { s.Instance = "pma" }, nativecontract.ReasonWrongInstance},
				{"principal", func(s *Scope, _ *Request, _ *Current) { s.Actor.PrincipalID = roleID(80) }, nativecontract.ReasonWrongPrincipal},
				{"invalid principal kind", func(s *Scope, _ *Request, _ *Current) { s.Actor.PrincipalKind = "coordinator" }, nativecontract.ReasonWrongPrincipal},
				{"owner", func(s *Scope, _ *Request, _ *Current) { s.Actor.OwnerPersonID = roleID(80) }, nativecontract.ReasonWrongPrincipal},
				{"grant", func(s *Scope, _ *Request, _ *Current) { s.Actor.GrantID = roleID(80) }, nativecontract.ReasonGrantMissing},
				{"grant revision", func(s *Scope, _ *Request, _ *Current) { s.Actor.GrantRevision-- }, nativecontract.ReasonGrantMissing},
				{"workspace", func(_ *Scope, r *Request, _ *Current) { r.Start.Intent.WorkspaceID = "foreign" }, nativecontract.ReasonPermissionRevoked},
				{"cwd", func(_ *Scope, r *Request, _ *Current) { r.Start.Cwd += "/foreign" }, nativecontract.ReasonPermissionRevoked},
				{"account", func(_ *Scope, r *Request, _ *Current) { r.Start.Intent.AccountID = roleID(80) }, nativecontract.ReasonPermissionRevoked},
				{"account label", func(_ *Scope, r *Request, _ *Current) { r.Start.AccountKey = "LEAD" }, nativecontract.ReasonPermissionRevoked},
				{"predecessor account", func(_ *Scope, _ *Request, c *Current) { c.PredecessorGrant.AccountID = roleID(80) }, nativecontract.ReasonIdentityMismatch},
				{"predecessor workspace", func(_ *Scope, _ *Request, c *Current) { c.PredecessorGrant.WorkspaceID = "foreign" }, nativecontract.ReasonIdentityMismatch},
				{"predecessor grant", func(_ *Scope, _ *Request, c *Current) { c.PredecessorGrant.Actor.GrantID = roleID(80) }, nativecontract.ReasonIdentityMismatch},
				{"model", func(_ *Scope, r *Request, _ *Current) { r.Start.Intent.ModelProfileID = roleID(80) }, nativecontract.ReasonPermissionRevoked},
				{"policy", func(_ *Scope, r *Request, _ *Current) { r.Start.Intent.PolicyID = roleID(80) }, nativecontract.ReasonPermissionRevoked},
				{"conversation", func(_ *Scope, r *Request, _ *Current) { x := roleID(80); r.Start.NativeResumeRef = &x }, nativecontract.ReasonIdentityMismatch},
				{"approval history", func(_ *Scope, r *Request, _ *Current) { r.ApprovalHistoryDigest = strings.Repeat("f", 64) }, nativecontract.ReasonIdentityMismatch},
				{"stale predecessor", func(_ *Scope, r *Request, _ *Current) { r.ExpectedBinding.RuntimeEpoch = roleID(80) }, nativecontract.ReasonWrongGeneration},
				{"stale daemon", func(_ *Scope, r *Request, _ *Current) { r.ExpectedBinding.DaemonGeneration = "daemon-old" }, nativecontract.ReasonWrongGeneration},
				{"stale revision", func(_ *Scope, r *Request, _ *Current) { r.ExpectedRevision-- }, nativecontract.ReasonWrongGeneration},
				{"source pid reuse", func(_ *Scope, r *Request, _ *Current) {
					r.Start.AfterExit.ProcessStartedAt = r.Start.AfterExit.ProcessStartedAt.Add(time.Second)
				}, nativecontract.ReasonOwnershipUnconfirmed},
				{"no after exit", func(_ *Scope, r *Request, _ *Current) { r.Start.AfterExit = nil }, nativecontract.ReasonOwnershipUnconfirmed},
				{"unpaired host", func(_ *Scope, _ *Request, c *Current) { c.Grant.PairedLocally = false }, nativecontract.ReasonQualificationMissing},
				{"network socket", func(_ *Scope, _ *Request, c *Current) { c.Grant.OutboundRelay = false }, nativecontract.ReasonQualificationMissing},
				{"borrowed LEAD evidence", func(_ *Scope, _ *Request, c *Current) { c.Grant.Evidence.Role = "LEAD" }, nativecontract.ReasonQualificationMissing},
				{"other host platform", func(_ *Scope, _ *Request, c *Current) { c.Grant.Evidence.Platform = "unknown" }, nativecontract.ReasonQualificationMissing},
				{"OS crossover", func(_ *Scope, _ *Request, c *Current) {
					c.Grant.Platform = "linux"
					c.PredecessorGrant.Platform = "linux"
				}, nativecontract.ReasonQualificationMissing},
				{"no role approval", func(_ *Scope, _ *Request, c *Current) { c.Grant.Evidence.RoleApprovalID = "" }, nativecontract.ReasonQualificationMissing},
				{"no restricted evidence", func(_ *Scope, _ *Request, c *Current) { c.Grant.Evidence.RestrictedRunID = "" }, nativecontract.ReasonQualificationMissing},
				{"no native evidence", func(_ *Scope, _ *Request, c *Current) { c.Grant.Evidence.NativeRunID = "" }, nativecontract.ReasonQualificationMissing},
				{"no LEAD soak", func(_ *Scope, _ *Request, c *Current) { c.Grant.Evidence.LeadSoakID = "" }, nativecontract.ReasonQualificationMissing},
				{"no operator execution", func(_ *Scope, _ *Request, c *Current) {
					c.Grant.Production = true
					c.PredecessorGrant.Production = true
				}, nativecontract.ReasonQualificationMissing},
				{"production downgrade", func(_ *Scope, _ *Request, c *Current) { c.PredecessorGrant.Production = true }, nativecontract.ReasonIdentityMismatch},
				{"no terminal export approval", func(_ *Scope, _ *Request, c *Current) { c.Grant.Evidence.Capabilities.TerminalView = true }, nativecontract.ReasonQualificationMissing},
				{"no history consent", func(_ *Scope, _ *Request, c *Current) { c.Grant.NativeHistoryGrantID = "" }, nativecontract.ReasonInvalidInput},
				{"expired grant", func(_ *Scope, _ *Request, c *Current) {
					c.Grant.Policy.ExpiresAt = c.Grant.Policy.ExpiresAt.Add(-time.Hour)
				}, nativecontract.ReasonGrantExpired},
				{"ambient tool", func(_ *Scope, _ *Request, c *Current) {
					c.Grant.Tools = []string{"Bash"}
					c.PredecessorGrant.Tools = c.Grant.Tools
				}, nativecontract.ReasonPermissionRevoked},
			} {
				t.Run(tc.name, func(t *testing.T) {
					switcher, scope, request, a := roleFixture(t, role)
					tc.change(&scope, &request, &a.current)
					_, err := switcher.Switch(t.Context(), scope, request)
					requireReason(t, err, tc.reason)
					if a.route.calls != 0 || a.route.launches != 0 || !a.route.live[roleID(90)] {
						t.Fatal("refused role switch reached native I/O or touched unrelated live session")
					}
				})
			}
			switcher, scope, request, a := roleFixture(t, role)
			a.route.entered, a.route.exited = make(chan struct{}), make(chan struct{})
			a.route.live[request.ExpectedBinding.OwnedSessionID] = true
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			go func() { _, err := switcher.Switch(ctx, scope, request); done <- err }()
			select {
			case <-a.route.entered:
			case <-ctx.Done():
				t.Fatal("did not reach predecessor exit barrier")
			}
			a.route.mu.Lock()
			if a.route.launches != 0 || !a.route.live[request.ExpectedBinding.OwnedSessionID] || !a.route.live[roleID(90)] {
				t.Fatal("writer replaced before source exit")
			}
			// Caller-owned pointers must not alter the queued, exact history/exit.
			*request.Start.NativeResumeRef = roleID(80)
			request.Start.AfterExit.PID = 999
			if a.route.start.NativeResumeRef == nil || *a.route.start.NativeResumeRef != a.current.Predecessor.Identity.NativeSessionRef || a.route.start.AfterExit.PID != 123 {
				t.Fatal("queued request borrowed mutable caller pointers")
			}
			a.route.live[request.ExpectedBinding.OwnedSessionID] = false
			a.route.mu.Unlock()
			close(a.route.exited)
			if err := <-done; err != nil {
				t.Fatal("exact after-exit cutover refused", err)
			}
			if a.route.launches != 1 || !a.route.live[roleID(90)] {
				t.Fatal("wrong writer count or unrelated session changed")
			}
		})
	}
}

func TestNativeRoleResumeExitAndReceiptEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Current, *roleRoute)
		reason nativecontract.Reason
	}{
		{"confirmed", func(*Current, *roleRoute) {}, ""},
		{"uncertain exit", func(c *Current, _ *roleRoute) { c.Exit.GroupAbsent = false }, nativecontract.ReasonOwnershipUnconfirmed},
		{"future exit", func(c *Current, _ *roleRoute) { c.Exit.ObservedAt = c.Exit.ObservedAt.Add(2 * time.Hour) }, nativecontract.ReasonOwnershipUnconfirmed},
		{"wrong process", func(c *Current, _ *roleRoute) { c.Exit.ProcessIdentityDigest = strings.Repeat("f", 64) }, nativecontract.ReasonOwnershipUnconfirmed},
		{"revoked", func(_ *Current, r *roleRoute) { r.revoked = true }, nativecontract.ReasonPermissionRevoked},
		{"accepted only", func(_ *Current, r *roleRoute) { r.responseState = "accepted" }, ""},
		{"uncertain receipt", func(_ *Current, r *roleRoute) { r.responseState = "uncertain" }, nativecontract.ReasonNativeFailure},
		{"rejected receipt", func(_ *Current, r *roleRoute) { r.responseState = "rejected" }, nativecontract.ReasonNativeFailure},
		{"wrong receipt", func(_ *Current, r *roleRoute) {
			r.corrupt = func(o *nativecontract.OperationResult) { o.Receipt.RequestID = roleID(80) }
		}, nativecontract.ReasonIdentityMismatch},
		{"wrong successor", func(_ *Current, r *roleRoute) {
			r.corrupt = func(o *nativecontract.OperationResult) {
				o.Session.CurrentBinding.RuntimeEpoch = r.current.Predecessor.Binding.RuntimeEpoch
			}
		}, nativecontract.ReasonWrongGeneration},
		{"capability escalation", func(_ *Current, r *roleRoute) {
			r.corrupt = func(o *nativecontract.OperationResult) {
				o.Session.Capabilities.Take = true
				o.Session.Capabilities.TakeSupported = true
			}
		}, nativecontract.ReasonIdentityMismatch},
		{"agent terminal URL", func(_ *Current, r *roleRoute) {
			r.corrupt = func(o *nativecontract.OperationResult) {
				url := "https://approved.example:8082/OPS"
				o.Session.TerminalViewerURL = &url
			}
		}, nativecontract.ReasonWrongPrincipal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			switcher, scope, request, a := roleFixture(t, "OPS")
			request.Start = nil
			request.Resume = &nativecontract.ResumeIntent{RequestID: roleID(17), ExpectedBinding: request.ExpectedBinding, ExpectedRevision: request.ExpectedRevision, Reason: "approved_restart", PolicyID: a.current.Grant.PolicyID}
			a.current.Predecessor.ExitObserved = true
			a.current.Exit = &nativecontract.ExitEvidence{RootExited: true, GroupAbsent: true, ProcessIdentityDigest: a.current.Predecessor.Identity.ProcessIdentityDigest, ObservedAt: a.current.Grant.Policy.ExpiresAt.Add(-time.Hour)}
			tc.change(&a.current, a.route)
			out, err := switcher.Switch(t.Context(), scope, request)
			if tc.reason != "" {
				requireReason(t, err, tc.reason)
				if out.Session != nil || out.Receipt.RequestID != "" {
					t.Fatal("failed or foreign response exposed a partial session/history result")
				}
			} else if err != nil || out.Receipt.State != "applied" && out.Receipt.State != "accepted" {
				t.Fatal("exact resume refused", err)
			}
			if !a.route.live[roleID(90)] {
				t.Fatal("resume touched unrelated live session")
			}
			if tc.name == "accepted only" && a.route.launches != 0 {
				t.Fatal("acceptance treated as native start evidence")
			}
		})
	}
	// Revocation while the old writer exits is checked by the final owned route,
	// not turned into a new role grant or an automatic retry by this layer.
	switcher, scope, request, a := roleFixture(t, "AIT")
	a.route.entered, a.route.exited = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() { _, err := switcher.Switch(ctx, scope, request); done <- err }()
	select {
	case <-a.route.entered:
	case <-ctx.Done():
		t.Fatal("did not reach revocation barrier")
	}
	a.route.mu.Lock()
	a.route.revoked = true
	a.route.mu.Unlock()
	close(a.route.exited)
	requireReason(t, <-done, nativecontract.ReasonPermissionRevoked)
	if a.route.calls != 1 || a.route.launches != 0 || !a.route.live[roleID(90)] {
		t.Fatal("revocation caused retry/fork or touched unrelated session")
	}
	// Transport loss remains unconfirmed, with no automatic resume or retry.
	switcher, scope, request, a = roleFixture(t, "Tick")
	lost := errors.New("fixture lost response")
	a.route.failure = lost
	if _, err := switcher.Switch(t.Context(), scope, request); !errors.Is(err, lost) || a.route.calls != 1 {
		t.Fatal("transport failure hidden or retried", err)
	}
	if _, err := json.Marshal(a.current.Grant); err == nil {
		t.Fatal("local role authority became a public DTO")
	}
	if _, err := json.Marshal(a.current); err == nil {
		t.Fatal("local history proof became a public DTO")
	}
	if !reflect.DeepEqual(a.current.PredecessorGrant.Tools, a.current.Grant.Tools) {
		t.Fatal("fixture lost immutable predecessor assignment")
	}
}
