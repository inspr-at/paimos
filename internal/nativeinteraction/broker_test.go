// SPDX-License-Identifier: AGPL-3.0-only

package nativeinteraction

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type authorityFunc func(context.Context, nativecontract.Actor, nativecontract.Approval, nativecontract.ApprovalDecision) error

func (f authorityFunc) Authorize(ctx context.Context, a nativecontract.Actor, r nativecontract.Approval, d nativecontract.ApprovalDecision) error {
	return f(ctx, a, r, d)
}

func callbackFixture(t *testing.T, authority Authority) (*Broker, *time.Time, nativecontract.Approval, nativecontract.ApprovalDecision, nativecontract.Actor) {
	t.Helper()
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	b, err := New(authority, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	bound := nativecontract.Binding{TenantID: "10000000-0000-4000-8000-000000000001", ProjectID: "10000000-0000-4000-8000-000000000002", OwnedSessionID: "10000000-0000-4000-8000-000000000003", HarnessSessionID: "10000000-0000-4000-8000-000000000004", RuntimeEpoch: "10000000-0000-4000-8000-000000000005", DaemonID: "fixture-daemon", DaemonGeneration: "generation-1", BindingRevision: 2}
	if err = b.Activate(bound); err != nil {
		t.Fatal(err)
	}
	desk := "10000000-0000-4000-8000-000000000006"
	a := nativecontract.Approval{ApprovalID: "10000000-0000-4000-8000-000000000007", RequestID: "10000000-0000-4000-8000-000000000008", Binding: bound, TurnRef: "turn-1", NativeRequestRef: "request-1", Kind: "tool", EffectDigest: strings.Repeat("a", 64), Summary: "Read the authorized file", AllowedDecisions: []string{"allow_once", "deny"}, ExpiresAt: now.Add(time.Minute), ExpiresInMS: 60000, DeskItemID: &desk}
	d := nativecontract.ApprovalDecision{RequestID: "10000000-0000-4000-8000-000000000009", ExpectedBinding: bound, ApprovalID: a.ApprovalID, ExpectedRevision: 3, EffectDigest: a.EffectDigest, Decision: "allow_once"}
	actor := nativecontract.Actor{TenantID: bound.TenantID, PrincipalID: "10000000-0000-4000-8000-000000000010", PrincipalKind: "person"}
	return b, &now, a, d, actor
}

func wantFailure(t *testing.T, err error, status int, reason nativecontract.Reason) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) || f.Status != status || f.Reason != reason {
		t.Fatalf("want %d/%s, got %v", status, reason, err)
	}
}

// Risk: concurrent resolutions, an agent message, or a replacement runtime
// can otherwise turn one human decision into multiple/unrelated native allows.
func TestCallbackExactOnceAndRuntimeFence(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var checks atomic.Int32
	authority := authorityFunc(func(ctx context.Context, _ nativecontract.Actor, _ nativecontract.Approval, _ nativecontract.ApprovalDecision) error {
		checks.Add(1)
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	b, now, a, d, actor := callbackFixture(t, authority)
	result, err := b.Register(a, *now)
	if err != nil {
		t.Fatal(err)
	}
	agent := actor
	agent.PrincipalKind = "agent"
	wantFailure(t, b.Decide(t.Context(), agent, d), 403, nativecontract.ReasonWrongPrincipal)
	wrong := d
	wrong.EffectDigest = strings.Repeat("b", 64)
	wantFailure(t, b.Decide(t.Context(), actor, wrong), 409, nativecontract.ReasonWrongGeneration)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- b.Decide(ctx, actor, d) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	started := make(chan struct{})
	go func() { close(started); done <- b.Decide(ctx, actor, d) }()
	<-started
	close(release)
	one, two := <-done, <-done
	if one == nil {
		wantFailure(t, two, 410, nativecontract.ReasonApprovalExpired)
	} else {
		wantFailure(t, one, 410, nativecontract.ReasonApprovalExpired)
		if two != nil {
			t.Fatal(two)
		}
	}
	if checks.Load() != 1 {
		t.Fatalf("native authority checked %d times", checks.Load())
	}
	if out := <-result; out.Decision != "allow_once" || out.Reason != "" {
		t.Fatalf("wrong outcome: %#v", out)
	}
	if _, open := <-result; open {
		t.Fatal("callback must close after one resolution")
	}
	_, err = b.Register(a, *now)
	wantFailure(t, err, 409, nativecontract.ReasonIdentityMismatch)
	// The host explicitly replaces its binding; the old callback is denied,
	// and a stale decision cannot find a newly reconstructed allow.
	a.ApprovalID = "10000000-0000-4000-8000-000000000011"
	result, err = b.Register(a, *now)
	if err != nil {
		t.Fatal(err)
	}
	fresh := a.Binding
	fresh.RuntimeEpoch = "10000000-0000-4000-8000-000000000012"
	if err = b.Activate(fresh); err != nil {
		t.Fatal(err)
	}
	if out := <-result; out.Decision != "deny" || out.Reason != nativecontract.ReasonApprovalExpired {
		t.Fatalf("replacement did not expire: %#v", out)
	}
}

// Risk: saved callbacks, transit time, or permission revocation must not be
// converted into an allow by retrying after restart/expiry.
func TestCallbackExpiryRevocationAndRestart(t *testing.T) {
	var revoked atomic.Bool
	check := authorityFunc(func(context.Context, nativecontract.Actor, nativecontract.Approval, nativecontract.ApprovalDecision) error {
		if revoked.Load() {
			return Fail(403, nativecontract.ReasonPermissionRevoked)
		}
		return nil
	})
	b, now, a, d, actor := callbackFixture(t, check)
	result, err := b.Register(a, now.Add(-59*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	wantFailure(t, b.Decide(t.Context(), actor, d), 403, nativecontract.ReasonPermissionRevoked)
	*now = now.Add(time.Second) // exact deadline, including transit time
	wantFailure(t, b.Decide(t.Context(), actor, d), 410, nativecontract.ReasonApprovalExpired)
	if b.Expire() != 1 {
		t.Fatal("lifecycle tick did not expire the waiting callback")
	}
	b.Close()
	if out := <-result; out.Decision != "deny" {
		t.Fatalf("expired callback permitted: %#v", out)
	}
	restarted, err := New(check, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Activate(a.Binding); err != nil {
		t.Fatal(err)
	}
	wantFailure(t, restarted.Decide(t.Context(), actor, d), 410, nativecontract.ReasonApprovalExpired)
}
