// SPDX-License-Identifier: AGPL-3.0-only

package nativeadmission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type fakeProfiles struct{ revision atomic.Int64 }

func (p *fakeProfiles) Resolve(_ context.Context, r nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	return nativecontract.LocalProfile{AccountID: r.AccountID, AccountHome: "/qualified/profile",
		UserHome: "/qualified/owner", WorkspaceRoot: "/qualified/workspace", ConversationRoot: "/qualified/history",
		ProfileBindingRevision: p.revision.Load(), Binary: nativecontract.BinaryPin{Path: "/qualified/vendor", SHA256: strings.Repeat("a", 64)}}, nil
}

type fakeAuthority struct {
	revoked          atomic.Bool
	entered, release chan struct{}
	once             sync.Once
	check            func(context.Context) error
}

func (a *fakeAuthority) Revalidate(ctx context.Context, _ Request) error {
	if a.check != nil {
		if err := a.check(ctx); err != nil {
			return err
		}
	}
	if a.entered != nil {
		var err error
		a.once.Do(func() {
			close(a.entered)
			select {
			case <-a.release:
			case <-ctx.Done():
				err = ctx.Err()
			}
		})
		if err != nil {
			return err
		}
	}
	if a.revoked.Load() {
		return denial(nativecontract.ReasonPermissionRevoked)
	}
	return nil
}

func (a *fakeAuthority) AuthorizeProfileChange(context.Context, nativecontract.ProfileRequest) error {
	if a.revoked.Load() {
		return denial(nativecontract.ReasonPermissionRevoked)
	}
	return nil
}

type admissionFixture struct {
	ledger       *agentsetup.SharedLedger
	root, worker *Gate
	profiles     *fakeProfiles
	authority    *fakeAuthority
	now          time.Time
	request      Request
}

func newAdmissionFixture(t *testing.T) *admissionFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := agentsetup.OpenSharedLedger(filepath.Join(base, "shared"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	f := &admissionFixture{ledger: l, now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC), profiles: &fakeProfiles{}, authority: &fakeAuthority{}}
	f.profiles.revision.Store(1)
	d, _, err := l.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for i, label := range []string{"root", "worker"} {
		id, err := agentsetup.LedgerID()
		if err != nil {
			t.Fatal(err)
		}
		instanceLabel, err := agentsetup.InstanceLabel(label)
		if err != nil {
			t.Fatal(err)
		}
		member := agentsetup.LedgerMember{ID: id, Label: instanceLabel, Root: filepath.Join(base, label), JoinedAt: f.now}
		if err = l.Register(member); err != nil {
			t.Fatal(err)
		}
		_, err = l.Import(id, agentsetup.LedgerInstance{Fingerprint: agentsetup.LedgerFingerprint(d.Generation, "instance", id), PID: os.Getpid(), StartedAt: f.now, MaximumAgents: 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = l.Enrolled(id, d.Generation); err != nil {
			t.Fatal(err)
		}
		state, err := agentsetup.OpenStore(filepath.Join(base, label), true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := state.Close(); err != nil {
				t.Error(err)
			}
		})
		g, err := New(Options{Ledger: l, State: state, Profiles: f.profiles, Authority: f.authority,
			MemberID: id, Generation: d.Generation, Now: func() time.Time { return f.now }})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			f.root = g
		} else {
			f.worker = g
		}
	}
	uid := func(n string) string { return "10000000-0000-4000-8000-" + strings.Repeat("0", 12-len(n)) + n }
	policy := nativecontract.Policy{GrantID: uid("1"), GrantRevision: 1, ExpiresAt: f.now.Add(time.Hour),
		ToolPolicyDigest: strings.Repeat("b", 64), SettingsPolicyDigest: strings.Repeat("c", 64),
		PermissionPolicy: "supervised", LocalHistory: "vendor_native", Continuation: "manual",
		MaximumRuntimeSeconds: 3600, MaximumTurns: 100, MaximumTokens: 10000}
	f.request = Request{
		AccountBindingDigest: strings.Repeat("f", 64),
		Binding: nativecontract.Binding{TenantID: uid("2"), ProjectID: uid("3"), OwnedSessionID: uid("4"),
			HarnessSessionID: uid("5"), RuntimeEpoch: uid("6"), DaemonID: "daemon-test", DaemonGeneration: "generation-test", BindingRevision: 1},
		Profile: nativecontract.ProfileRequest{TenantID: uid("2"), OwnerPersonID: uid("7"), AccountID: uid("8"), AccountKey: "enrolled", WorkspaceID: "approved-workspace", Harness: "claude", Policy: policy, BindingRevision: 1},
		Launch:  nativecontract.LaunchConfig{ModelProfileID: uid("10"), Model: "qualified-model", ServiceTier: "default", ToolPolicyDigest: policy.ToolPolicyDigest, SettingsPolicyDigest: policy.SettingsPolicyDigest},
		Login:   agentsetup.LedgerLogin{Harness: "claude", Identity: agentsetup.LedgerFingerprint(d.Generation, "login/claude", "verified-owner")},
	}
	return f
}

func requireReason(t *testing.T, err error, reason nativecontract.Reason) {
	t.Helper()
	var v *nativecontract.ValidationError
	if !errors.As(err, &v) || v.Reason != reason {
		t.Fatalf("got %v, want %s", err, reason)
	}
}

func workerRequest(f *admissionFixture) Request {
	r := cloneRequest(f.request)
	r.Binding.OwnedSessionID = "20000000-0000-4000-8000-000000000001"
	r.Binding.RuntimeEpoch = "20000000-0000-4000-8000-000000000002"
	r.Binding.HarnessSessionID = "20000000-0000-4000-8000-000000000003"
	run := "20000000-0000-4000-8000-000000000004"
	r.Binding.RunID = &run
	return r
}

// Risk: long-lived roots and workers overbook an owner's max_parallel=1,
// or a stale route/revoked grant sends the first native input anyway.
func TestNativeAdmissionOverlappingRootWorkerAndRevokedRoute(t *testing.T) {
	f := newAdmissionFixture(t)
	barrierCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	f.authority.entered, f.authority.release = make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-f.authority.release:
		default:
			close(f.authority.release)
		}
	})
	type outcome struct {
		permit *Permit
		err    error
	}
	root := make(chan outcome, 1)
	go func() { p, err := f.root.Reserve(t.Context(), f.request); root <- outcome{p, err} }()
	select {
	case <-f.authority.entered:
	case early := <-root:
		t.Fatalf("root failed before admission barrier: %v", early.err)
	case <-barrierCtx.Done():
		t.Fatal("root never entered final admission")
	}
	// Root is still in final server admission, after taking the LOCAL slot.
	// The worker uses a different ledger member and cannot bypass that slot.
	worker := workerRequest(f)
	if _, err := f.worker.Reserve(t.Context(), worker); !errors.Is(err, agentsetup.ErrLedgerOccupied) {
		t.Fatalf("double booking: %v", err)
	}
	close(f.authority.release)
	first := <-root
	if first.err != nil {
		t.Fatal(first.err)
	}
	var forks, inputs int
	f.authority.revoked.Store(true)
	err := f.root.Attempt(t.Context(), first.permit, func(context.Context, nativecontract.LocalProfile) (nativecontract.NativeIdentity, error) {
		forks++
		return nativecontract.NativeIdentity{}, nil
	})
	requireReason(t, err, nativecontract.ReasonPermissionRevoked)
	if forks != 0 {
		t.Fatal("revoked routing started native writer")
	}
	if err = f.root.Abandon(t.Context(), first.permit); err != nil {
		t.Fatal(err)
	}
	f.authority.revoked.Store(false)
	request := cloneRequest(f.request)
	request.Binding.RuntimeEpoch = "30000000-0000-4000-8000-000000000001"
	request.Binding.HarnessSessionID = "30000000-0000-4000-8000-000000000002"
	request.Binding.BindingRevision++
	p, err := f.root.Reserve(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	identity := nativecontract.NativeIdentity{RuntimeEpoch: request.Binding.RuntimeEpoch, PID: 1234, StartedAt: f.now, ProcessIdentityDigest: strings.Repeat("d", 64)}
	if err = f.root.Attempt(t.Context(), p, func(context.Context, nativecontract.LocalProfile) (nativecontract.NativeIdentity, error) {
		forks++
		return identity, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = f.root.State(t.Context(), p, "idle"); err != nil {
		t.Fatal(err)
	}
	if err = f.root.Input(t.Context(), p, func(context.Context) error { inputs++; return nil }); err != nil {
		t.Fatal(err)
	}
	f.authority.revoked.Store(true)
	requireReason(t, f.root.Input(t.Context(), p, func(context.Context) error { inputs++; return nil }), nativecontract.ReasonPermissionRevoked)
	f.authority.revoked.Store(false)
	f.profiles.revision.Store(2)
	requireReason(t, f.root.Input(t.Context(), p, func(context.Context) error { inputs++; return nil }), nativecontract.ReasonIdentityMismatch)
	f.profiles.revision.Store(1)
	if inputs != 1 || forks != 1 {
		t.Fatal("refused operation reached vendor")
	}
	for _, state := range []string{"idle", "taken", "exit_unconfirmed"} {
		if err = f.root.State(t.Context(), p, state); err != nil {
			t.Fatal(err)
		}
		if _, err = f.worker.Reserve(t.Context(), worker); !errors.Is(err, agentsetup.ErrLedgerOccupied) {
			t.Fatalf("%s freed the slot: %v", state, err)
		}
	}
	requireReason(t, f.root.State(t.Context(), p, "idle"), nativecontract.ReasonOwnershipUnconfirmed)
	f.now = f.now.Add(2 * time.Hour)
	worker.Profile.Policy.ExpiresAt = f.now.Add(time.Hour)
	if _, err = f.worker.Reserve(t.Context(), worker); !errors.Is(err, agentsetup.ErrLedgerOccupied) {
		t.Fatalf("expired grant freed live slot: %v", err)
	}
	exit := nativecontract.ExitEvidence{ProcessIdentityDigest: identity.ProcessIdentityDigest, RootExited: true, ObservedAt: f.now}
	requireReason(t, f.root.ConfirmExit(t.Context(), p, exit), nativecontract.ReasonOwnershipUnconfirmed)
	exit.GroupAbsent = true
	if err = f.root.ConfirmExit(t.Context(), p, exit); err != nil {
		t.Fatal(err)
	}
	// Re-open the gate: the private tombstone still forbids an alternate account.
	reopened, err := New(f.root.opts)
	if err != nil {
		t.Fatal(err)
	}
	request.Profile.Policy.ExpiresAt = f.now.Add(time.Hour)
	request.Profile.AccountID = "40000000-0000-4000-8000-000000000001"
	requireReason(t, reserveError(reopened.Reserve(t.Context(), request)), nativecontract.ReasonUnsupported)
	request.Profile.AccountID = f.request.Profile.AccountID
	request.AccountBindingDigest = strings.Repeat("e", 64)
	requireReason(t, reserveError(reopened.Reserve(t.Context(), request)), nativecontract.ReasonUnsupported)
	// A fresh worker's failed fork is UNCERTAIN even with a cancelled caller.
	wp, err := f.worker.Reserve(t.Context(), worker)
	if err != nil {
		t.Fatal(err)
	}
	failedFork := errors.New("fake launch reply lost")
	if err = f.worker.Attempt(t.Context(), wp, func(context.Context, nativecontract.LocalProfile) (nativecontract.NativeIdentity, error) {
		return nativecontract.NativeIdentity{}, failedFork
	}); !errors.Is(err, failedFork) {
		t.Fatal(err)
	}
	requireReason(t, f.worker.Abandon(t.Context(), wp), nativecontract.ReasonOwnershipUnconfirmed)
	data, _, err := f.ledger.Snapshot()
	if err != nil || len(data.Groups) != 1 {
		t.Fatalf("unknown fork lost hold: %v", err)
	}
	groups, err := f.worker.HeldGroups(t.Context())
	if err != nil || len(groups) != 1 || groups[0].State != "launching" || groups[0].ID != wp.group {
		t.Fatalf("re-import forgot uncertain writer: %v", err)
	}
	opts := f.worker.opts
	opts.Generation = "60000000-0000-4000-8000-000000000001"
	rebuilt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rebuilt.HeldGroups(t.Context()); !errors.Is(err, agentsetup.ErrLedgerGeneration) {
		t.Fatalf("rebuild laundered old occupancy: %v", err)
	}
}

func reserveError(_ *Permit, err error) error { return err }

// Risk: refusal after durable possible-fork evidence permanently consumes the
// machine's only slot even though the owning launch callback was never called.
func TestNativeAdmissionPreForkRefusalReleasesReservation(t *testing.T) {
	for _, failure := range []struct {
		name   string
		err    error
		cancel bool
	}{
		{name: "revoked", err: denial(nativecontract.ReasonPermissionRevoked)},
		{name: "authority_unavailable", err: errors.New("fake authority unavailable")},
		{name: "cancelled", err: context.Canceled, cancel: true},
	} {
		t.Run(failure.name, func(t *testing.T) {
			f := newAdmissionFixture(t)
			p, err := f.root.Reserve(t.Context(), f.request)
			if err != nil {
				t.Fatal(err)
			}
			attemptCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			barrierCtx, stopBarrier := context.WithTimeout(t.Context(), 15*time.Second)
			defer stopBarrier()
			entered, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			var checks int
			// The first Attempt check passes; only the final check after durable
			// journal/ledger transitions fails. The worker keeps valid authority.
			f.root.opts.Authority = &fakeAuthority{check: func(ctx context.Context) error {
				checks++
				if checks == 1 {
					return nil
				}
				close(entered)
				select {
				case <-release:
					if failure.cancel {
						cancel()
						return ctx.Err()
					}
					return failure.err
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			var forks atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- f.root.Attempt(attemptCtx, p, func(context.Context, nativecontract.LocalProfile) (nativecontract.NativeIdentity, error) {
					forks.Add(1)
					return nativecontract.NativeIdentity{}, nil
				})
			}()
			select {
			case <-entered:
			case early := <-done:
				t.Fatalf("attempt failed before final-check barrier: %v", early)
			case <-barrierCtx.Done():
				t.Fatal("attempt never reached final-check barrier")
			}
			r, err := f.root.read(f.request.Binding.OwnedSessionID)
			if err != nil || !r.Attempted || r.State != "possible_fork" {
				t.Fatalf("final check lacks durable possible-fork evidence: %+v, %v", r, err)
			}
			data, _, err := f.ledger.Snapshot()
			if err != nil || data.Groups[p.group].State != "launching" {
				t.Fatalf("final check lacks launching hold: %+v, %v", data.Groups, err)
			}
			worker := workerRequest(f)
			if _, err = f.worker.Reserve(t.Context(), worker); !errors.Is(err, agentsetup.ErrLedgerOccupied) {
				t.Fatalf("reservation released before final refusal: %v", err)
			}
			close(release)
			select {
			case err = <-done:
			case <-barrierCtx.Done():
				t.Fatal("attempt did not return after final refusal")
			}
			if !errors.Is(err, failure.err) || checks != 2 || forks.Load() != 0 {
				t.Fatalf("wrong refusal or fork: err=%v checks=%d forks=%d", err, checks, forks.Load())
			}
			wp, err := f.worker.Reserve(t.Context(), worker)
			if err != nil {
				t.Fatalf("provably unlaunched root still blocks worker: %v", err)
			}
			if err = f.worker.Abandon(t.Context(), wp); err != nil {
				t.Fatal(err)
			}
			r, err = f.root.read(f.request.Binding.OwnedSessionID)
			if err != nil || r.State != "released" || !r.Attempted || r.IdentityDigest != "" {
				t.Fatalf("refusal lacks released tombstone: %+v, %v", r, err)
			}
			reopened, err := New(f.root.opts)
			if err != nil {
				t.Fatal(err)
			}
			groups, err := reopened.HeldGroups(t.Context())
			if err != nil || len(groups) != 0 {
				t.Fatalf("reopen re-imports unlaunched root: %+v, %v", groups, err)
			}
		})
	}
}

// Risk: logout/relogin races a live or uncertain writer, or a callback failure
// silently permits new use of a profile whose sign-in operation is still live.
func TestNativeProfileChangeSerializesWithLiveUsers(t *testing.T) {
	f := newAdmissionFixture(t)
	barrierCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	p, err := f.root.Reserve(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	change := func(context.Context) (bool, error) { calls++; return true, nil }
	_, err = f.root.ChangeProfile(t.Context(), f.request.Profile, f.request.Login, change)
	requireReason(t, err, nativecontract.ReasonAccountFull)
	// Another paired member is protected by the SAME login hold, even with
	// no account journal in its own directory.
	_, err = f.worker.ChangeProfile(t.Context(), f.request.Profile, f.request.Login, change)
	if !errors.Is(err, agentsetup.ErrLedgerOccupied) {
		t.Fatalf("peer profile change bypassed live writer: %v", err)
	}
	if calls != 0 {
		t.Fatal("live profile mutated")
	}
	if err = f.root.Abandon(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := f.root.ChangeProfile(t.Context(), f.request.Profile, f.request.Login, func(ctx context.Context) (bool, error) {
			close(entered)
			select {
			case <-release:
				return true, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-entered:
	case early := <-done:
		t.Fatalf("profile change failed before barrier: %v", early)
	case <-barrierCtx.Done():
		t.Fatal("profile change never reached barrier")
	}
	worker := workerRequest(f)
	if _, err = f.worker.Reserve(t.Context(), worker); !errors.Is(err, agentsetup.ErrLedgerOccupied) {
		t.Fatalf("start raced profile change: %v", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	// Current person authority is rechecked; it cannot be inferred from a path.
	f.authority.revoked.Store(true)
	_, err = f.root.ChangeProfile(t.Context(), f.request.Profile, f.request.Login, change)
	requireReason(t, err, nativecontract.ReasonPermissionRevoked)
	f.authority.revoked.Store(false)
	group, err := f.root.ChangeProfile(t.Context(), f.request.Profile, f.request.Login, func(context.Context) (bool, error) { return false, errors.New("fake login still running") })
	requireReason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
	data, _, snapErr := f.ledger.Snapshot()
	if snapErr != nil || group == "" || len(data.Groups) != 1 {
		t.Fatalf("unconfirmed login lost hold: %v", snapErr)
	}
	if _, err = f.worker.Reserve(t.Context(), worker); !errors.Is(err, agentsetup.ErrLedgerOccupied) {
		t.Fatalf("unconfirmed login admitted writer: %v", err)
	}
	changedLogin := cloneRequest(worker)
	changedLogin.Login.Identity = strings.Repeat("e", 64)
	requireReason(t, reserveError(f.root.Reserve(t.Context(), changedLogin)), nativecontract.ReasonOwnershipUnconfirmed)
	groups, err := f.root.HeldGroups(t.Context())
	if err != nil || len(groups) != 1 || groups[0].ID != group {
		t.Fatalf("import forgot unresolved profile change: %v", err)
	}
}
