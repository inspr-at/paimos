// SPDX-License-Identifier: AGPL-3.0-only

package start

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type testClaims struct {
	result    nativecontract.ClaimResult
	err       error
	committed bool
	advance   func()
}

func (c *testClaims) Claim(context.Context, nativecontract.DaemonActor, nativecontract.Claim) (nativecontract.ClaimResult, error) {
	if c.advance != nil {
		c.advance()
	}
	if c.err != nil {
		return nativecontract.ClaimResult{}, c.err
	}
	c.committed = true
	return c.result, nil
}

type testProfiles struct{ profile nativecontract.LocalProfile }

func (p testProfiles) Resolve(context.Context, nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	return p.profile, nil
}

type testLauncher struct {
	claims *testClaims
	count  int
	last   nativecontract.LaunchSpec
}

func (l *testLauncher) Open(_ context.Context, s nativecontract.LaunchSpec) (nativecontract.NativeTransport, error) {
	if !l.claims.committed {
		return nil, errors.New("opened before durable claim")
	}
	l.count++
	l.last = s
	return &testTransport{}, nil
}

type testTransport struct{ nativecontract.NativeTransport }
type testGuard struct {
	now              func() time.Time
	count            int
	entered, release chan struct{}
}

func (g *testGuard) Open(ctx context.Context, c nativecontract.ClaimResult, s nativecontract.LaunchSpec, deadline time.Time, open func(context.Context, nativecontract.LaunchSpec) (nativecontract.NativeTransport, error)) (nativecontract.NativeTransport, error) {
	g.count++
	if g.entered != nil {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if !g.now().Before(deadline) {
		return nil, refusal(nativecontract.ReasonGrantExpired)
	}
	return open(ctx, s)
}

// Risk: local paths/account homes or a delayed response bypass the durable
// claim, immutable profile or monotonic final launch check.
func TestStartRequiresDurableClaimAndLocalLaunchFence(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	const id = "21000000-0000-4000-8000-000000000001"
	const id2 = "21000000-0000-4000-8000-000000000002"
	now := time.Now()
	digest := strings.Repeat("a", 64)
	intent := nativecontract.StartIntent{RequestID: id, ExpectedProjectRevision: 1, Role: "coordinator", Profile: "native", Harness: "claude", ComputerID: id, AccountID: id, WorkspaceID: "approved-workspace", ModelProfileID: id, PolicyID: id}
	policy := nativecontract.Policy{GrantID: id, GrantRevision: 1, ExpiresAt: now.Add(time.Hour), ToolPolicyDigest: digest, SettingsPolicyDigest: digest, PermissionPolicy: "supervised", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 100, MaximumTokens: 10000}
	binding := nativecontract.Binding{TenantID: id, ProjectID: id, OwnedSessionID: id, HarnessSessionID: id, RuntimeEpoch: id2, DaemonID: "native-daemon", DaemonGeneration: "generation-1", BindingRevision: 2}
	rootID := id2
	claims := &testClaims{result: nativecontract.ClaimResult{OperationID: id, OwnedSessionID: id, Intent: intent, Policy: policy, ExpectedBinding: &binding, ExpiresAt: now.Add(20 * time.Second), ClaimKind: "start", LaunchAuthorized: true, LaunchConfig: nativecontract.LaunchConfig{ModelProfileID: id, Model: "claude-opus-4-6", ServiceTier: "default", ToolPolicyDigest: digest, SettingsPolicyDigest: digest}, AuthorizationTTLMS: 20000, RootReservationID: &rootID}}
	launcher := &testLauncher{claims: claims}
	guard := &testGuard{now: func() time.Time { return now }}
	profiles := testProfiles{nativecontract.LocalProfile{AccountID: id, AccountHome: root, UserHome: root, ConversationRoot: root, WorkspaceRoot: root, ProfileBindingRevision: 2, Binary: nativecontract.BinaryPin{Path: filepath.Join(root, "vendor"), SHA256: digest}}}
	lifetime, cancelLifetime := context.WithCancel(t.Context())
	defer cancelLifetime()
	s := Starter{Claims: claims, Profiles: profiles, Launcher: launcher, Guard: guard, Lifetime: lifetime, Now: func() time.Time { return now }}
	daemon := nativecontract.DaemonActor{Actor: nativecontract.Actor{TenantID: id, OwnerPersonID: id}, ComputerID: id, DaemonID: binding.DaemonID, DaemonGeneration: binding.DaemonGeneration}
	local := nativecontract.LocalStart{Intent: intent, Cwd: root, AccountKey: "own-account"}
	claim := nativecontract.Claim{OperationID: id, ClaimKind: "start"}
	claims.err = refusal(nativecontract.ReasonPermissionRevoked)
	if _, err := s.Start(t.Context(), daemon, claim, local); !hasReason(err, nativecontract.ReasonPermissionRevoked) || launcher.count != 0 {
		t.Fatalf("failed claim opened vendor: %v", err)
	}
	claims.err = nil
	local.Cwd = filepath.Join(root, "outside")
	if _, err := s.Start(t.Context(), daemon, claim, local); !hasReason(err, nativecontract.ReasonPermissionRevoked) || launcher.count != 0 {
		t.Fatalf("physical workspace escape opened vendor: %v", err)
	}
	local.Cwd = root
	wrong := daemon
	wrong.DaemonGeneration = "foreign"
	if _, err := s.Start(t.Context(), wrong, claim, local); !hasReason(err, nativecontract.ReasonIdentityMismatch) || launcher.count != 0 {
		t.Fatalf("foreign daemon launched: %v", err)
	}
	// The elapsed request duration consumes TTL; wall-clock expires_at cannot
	// renew authority by starting a fresh timer when the response is received.
	claims.advance = func() { now = now.Add(21 * time.Second) }
	if _, err := s.Start(t.Context(), daemon, claim, local); !hasReason(err, nativecontract.ReasonGrantExpired) || launcher.count != 0 {
		t.Fatalf("slow claim renewed TTL: %v", err)
	}
	claims.advance = nil
	guard.entered = make(chan struct{})
	guard.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := s.Start(t.Context(), daemon, claim, local); done <- err }()
	guardCtx, guardCancel := context.WithTimeout(t.Context(), time.Minute)
	defer guardCancel()
	select {
	case <-guard.entered:
	case err := <-done:
		t.Fatalf("launch did not reach final ledger fence: %v", err)
	case <-guardCtx.Done():
		t.Fatal("launch fence never reached")
	}
	now = now.Add(21 * time.Second)
	close(guard.release)
	if err := <-done; !hasReason(err, nativecontract.ReasonGrantExpired) || launcher.count != 0 {
		t.Fatalf("delayed local fence launched: %v", err)
	}
	guard.entered = nil
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	if _, err := s.Start(requestCtx, daemon, claim, local); err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	if launcher.count != 1 || launcher.last.Lifetime.Err() != nil || launcher.last.Binding.RuntimeEpoch != id2 {
		t.Fatal("claim/binding or supervisor lifetime was lost")
	}
}

func hasReason(err error, want nativecontract.Reason) bool {
	var e *nativecontract.ValidationError
	return errors.As(err, &e) && e.Reason == want
}
