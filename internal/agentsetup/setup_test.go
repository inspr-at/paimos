// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testTenant = "11111111-1111-4111-8111-111111111111"
const testComputer = "22222222-2222-4222-8222-222222222222"
const testPrincipal = "33333333-3333-4333-8333-333333333333"
const testAccount = "44444444-4444-4444-8444-444444444444"
const otherAccount = "55555555-5555-4555-8555-555555555555"

type setupAPI struct {
	prior           View
	request         DeviceRequest
	createCount     int
	lostCreate      bool
	offline         bool
	approved        bool
	view            View
	cleaned         []string
	computerCleaned bool
	progress        *SetupProgress
}

func (*setupAPI) Guide(context.Context) (Guide, error) {
	return Guide{InstanceURL: "https://aeon.example.test", DefaultTenantSlug: "test", Protocol: "pairing-v1"}, nil
}
func (a *setupAPI) Create(_ context.Context, r DeviceRequest) (DeviceResponse, error) {
	a.createCount++
	if a.request.RequestID != "" && (a.request.RequestID != r.RequestID || a.request.DeviceHash != r.DeviceHash || a.request.RuntimeHash != r.RuntimeHash || a.request.LifecycleHash != r.LifecycleHash) {
		if r.ExistingComputerID != testComputer || Hash([]byte(r.ExistingProof)) != a.request.LifecycleHash || r.RuntimeHash != a.request.RuntimeHash || r.LifecycleHash != a.request.LifecycleHash {
			return DeviceResponse{}, errors.New("identity changed")
		}
		a.prior = a.view
	}
	a.request = r
	if a.lostCreate && a.createCount == 1 {
		return DeviceResponse{}, errors.New("lost response")
	}
	return DeviceResponse{RequestID: r.RequestID, TenantID: testTenant, UserCode: "123-456-789", State: "pending", ExpiresAt: time.Now().Add(10 * time.Minute), IntervalSeconds: 5, VerificationURI: "https://aeon.example.test/agents/register-agent", Digest: Hash([]byte("approved details"))}, nil
}
func (a *setupAPI) Redeem(_ context.Context, r ProofRequest) (View, error) {
	if Hash([]byte(r.DeviceSecret)) != a.request.DeviceHash {
		return View{}, errors.New("proof mismatch")
	}
	v := View{RequestID: a.request.RequestID, TenantID: testTenant, State: "pending", Digest: Hash([]byte("approved details")), ComputerName: a.request.ComputerName, Platform: a.request.Platform, Arch: a.request.Arch, Workspace: a.request.Workspace, Requested: a.request.Accounts, Capabilities: a.request.Capabilities}
	v.ExistingComputerID = a.request.ExistingComputerID
	if a.approved {
		v.State = "redeemed"
		v.ComputerID = testComputer
		v.PrincipalID = testPrincipal
		v.DaemonID = "paired-daemon"
		v.ComputerState = "connected"
		v.RuntimePrefix = "test"
		v.Revision = 1
		v.Verification = Verification{Mode: "connect_only"}
		v.Enrollments = append([]Enrollment(nil), a.prior.Enrollments...)
		for i, c := range a.request.Accounts {
			id := testAccount
			if i > 0 || a.request.ExistingComputerID != "" {
				id = otherAccount
			}
			v.Enrollments = append(v.Enrollments, Enrollment{AccountID: id, AccountKey: c.Key, Harness: c.Harness, Label: c.Label, State: "connected", VerificationState: "not_selected"})
		}
	}
	a.view = v
	return v, nil
}
func (a *setupAPI) Reconcile(_ context.Context, p ProofRequest) (View, error) {
	if a.offline {
		return View{}, errors.New("offline")
	}
	if Hash([]byte(p.LifecycleSecret)) != a.request.LifecycleHash {
		return View{}, errors.New("lifecycle proof mismatch")
	}
	a.progress = p.Progress
	a.cleaned = append([]string(nil), p.Cleaned...)
	a.computerCleaned = p.ComputerCleaned
	v := a.view
	if v.ComputerID == "" && a.prior.ComputerID != "" {
		v = a.prior
	}
	v.RuntimePrefix = ""
	v.Enrollments = append([]Enrollment(nil), v.Enrollments...)
	for i := range v.Enrollments {
		for _, id := range p.Cleaned {
			if v.Enrollments[i].AccountID == id {
				v.Enrollments[i].Cleanup = "confirmed"
			}
		}
	}
	a.view.Enrollments = append([]Enrollment(nil), v.Enrollments...)
	if p.ComputerCleaned {
		v.Cleanup = "confirmed"
		v.Processes = "drained"
	}
	return v, nil
}
func (a *setupAPI) Disconnect(_ context.Context, _ secret, id string) (View, error) {
	if a.offline {
		return View{}, errors.New("offline")
	}
	if id == "" {
		a.view.ComputerState = "revoked"
	}
	for i := range a.view.Enrollments {
		if id == "" || a.view.Enrollments[i].AccountID == id {
			a.view.Enrollments[i].State = "revoked"
		}
	}
	a.view.Revision++
	v := a.view
	v.RuntimePrefix = ""
	return v, nil
}

type fakeLocal struct {
	states  map[string]LocalStatus
	fenced  []string
	offline bool
}

func (l *fakeLocal) Fence(_ context.Context, daemon, id string) (LocalStatus, error) {
	l.fenced = append(l.fenced, id)
	return LocalStatus{DaemonID: daemon, State: "unconfirmed"}, nil
}
func (l *fakeLocal) Status(_ context.Context, id string) (LocalStatus, error) {
	if l.offline {
		return LocalStatus{}, errors.New("offline")
	}
	if s, ok := l.states[id]; ok {
		return s, nil
	}
	return LocalStatus{Ready: true, DaemonID: "paired-daemon", State: "drained"}, nil
}

type fixtureExecutor struct {
	calls  []Command
	self   string
	active bool
}

func (x *fixtureExecutor) Run(_ context.Context, c Command) ([]byte, error) {
	x.calls = append(x.calls, c)
	if len(c.Args) > 0 && c.Args[0] == "bootstrap" {
		x.active = true
	}
	if len(c.Args) > 0 && c.Args[0] == "bootout" {
		x.active = false
	}
	if c.Path == "/bin/ps" {
		return []byte(fmt.Sprintf("%d %s\n900 /other/classic/paimos-agentd\n", os.Getpid(), x.self)), nil
	}
	if len(c.Args) > 0 && (c.Args[0] == "print" || c.Args[0] == "--user" && len(c.Args) > 1 && c.Args[1] == "is-active") {
		if x.active {
			return []byte("active"), nil
		}
		return nil, &CommandError{ExitCode: 113}
	}
	return nil, nil
}

func engineFixture(t *testing.T) (*Engine, *setupAPI, *fakeLocal, Options, *fixtureExecutor) {
	t.Helper()
	s := testStore(t)
	home := physicalTemp(t)
	exe := filepath.Join(home, "paimos-agentd")
	if err := os.WriteFile(exe, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	x := &fixtureExecutor{self: exe}
	api := &setupAPI{}
	local := &fakeLocal{states: map[string]LocalStatus{}}
	now := time.Now().UTC()
	e := &Engine{Store: s, API: api, Local: local, Services: &ServiceManager{Platform: Platform{OS: "darwin", Arch: "arm64"}, Home: home, UID: os.Getuid(), Executable: exe, Executor: x}, Now: func() time.Time { return now }}
	o := Options{Origin: "https://aeon.example.test", ComputerName: "test computer", Workspace: physicalTemp(t), Platform: Platform{OS: "darwin", Arch: "arm64"}, Candidates: []Candidate{{Harness: "codex", Label: "personal@example.test", Identity: "personal@example.test", Path: exe, Home: home, Login: "signed_in", Version: "0.157.1"}}}
	return e, api, local, o, x
}
func approveFixture(t *testing.T, e *Engine, a *setupAPI, o Options) {
	t.Helper()
	if _, err := e.Begin(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	a.approved = true
	e.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if p, err := e.Step(t.Context()); err != nil || p.Stage != "connected" {
		t.Fatalf("approval not provisioned: %s %v", p.Stage, err)
	}
}

func TestSetupResponseLossResumesSamePrivateIdentity(t *testing.T) {
	e, a, _, o, _ := engineFixture(t)
	a.lostCreate = true
	if _, err := e.Begin(t.Context(), o); err == nil {
		t.Fatal("lost response hidden")
	}
	before, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	if before.Device == before.Runtime || before.Device == before.Lifecycle || before.Runtime == before.Lifecycle {
		t.Fatal("capability secrets not independent")
	}
	p, err := e.Begin(t.Context(), o)
	if err != nil || p.UserCode != "123-456-789" {
		t.Fatal("response loss did not resume")
	}
	after, _ := e.load()
	if before.Device != after.Device || before.Runtime != after.Runtime || before.Request.RequestID != after.Request.RequestID || a.createCount != 2 {
		t.Fatal("retry minted new authority")
	}
	b, _ := json.Marshal(p)
	for _, capability := range []secret{after.Device, after.Runtime, after.Lifecycle} {
		if strings.Contains(string(b), string(capability)) {
			t.Fatal("progress leaked capability")
		}
	}
	if _, _, err := ReadRuntime(e.Store.Path()); err == nil {
		t.Fatal("pending approval provisioned runtime")
	}
}
func TestSetupApprovalAndRetryDoNotDuplicateServiceOrCredential(t *testing.T) {
	e, a, _, o, x := engineFixture(t)
	o.StartService = true
	approveFixture(t, e, a, o)
	_, key, err := ReadRuntime(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, again, err := ReadRuntime(e.Store.Path())
	if err != nil || again != key {
		t.Fatal("runtime credential rotated on retry")
	}
	starts := 0
	for _, c := range x.calls {
		if len(c.Args) > 0 && c.Args[0] == "bootstrap" {
			starts++
		}
		for _, arg := range c.Args {
			if strings.Contains(arg, string(key)) {
				t.Fatal("credential in command arguments")
			}
		}
	}
	if starts != 1 {
		t.Fatal("service activated more than once")
	}
}
func TestSelectiveOfflineDisconnectPreservesSharedDaemonAndOtherAccount(t *testing.T) {
	e, a, l, o, x := engineFixture(t)
	o.StartService = true
	o.Candidates = append(o.Candidates, Candidate{Harness: "cursor", Label: "work@example.test", Identity: "provider-user", Path: o.Candidates[0].Path, Login: "signed_in", Version: "1.0.0"})
	approveFixture(t, e, a, o)
	a.offline = true
	l.offline = true
	p, err := e.Disconnect(t.Context(), testAccount)
	if err == nil || p.ServerRevocation != "unconfirmed" {
		t.Fatal("offline server revocation falsely confirmed")
	}
	if len(l.fenced) != 1 || l.fenced[0] != testAccount {
		t.Fatal("wrong local disconnect scope")
	}
	for _, c := range x.calls {
		if len(c.Args) > 0 && c.Args[0] == "bootout" {
			t.Fatal("offline selective disconnect unloaded service")
		}
	}
	a.offline = false
	l.offline = false
	p, err = e.Disconnect(t.Context(), testAccount)
	if err != nil || p.Stage != "connected" {
		t.Fatalf("selective cleanup failed: %s %v", p.Stage, err)
	}
	runtime, _, err := ReadRuntime(e.Store.Path())
	if err != nil || len(runtime.Accounts) != 1 || runtime.Accounts[0].AccountID != otherAccount {
		t.Fatal("selective cleanup removed healthy account/runtime key")
	}
	for _, c := range x.calls {
		if len(c.Args) > 0 && c.Args[0] == "bootout" {
			t.Fatal("selective disconnect unloaded shared service")
		}
	}
}
func TestRevokedActiveWorkDrainsThenCleansWithUnsettledAudit(t *testing.T) {
	e, a, l, o, x := engineFixture(t)
	o.StartService = true
	approveFixture(t, e, a, o)
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "draining", Active: []string{"run"}}
	l.states[testAccount] = l.states[""]
	p, err := e.Disconnect(t.Context(), "")
	if err != nil || p.Stage != "draining" || a.computerCleaned {
		t.Fatal("active revoked work falsely cleaned")
	}
	for _, c := range x.calls {
		if len(c.Args) > 0 && c.Args[0] == "bootout" {
			t.Fatal("active work service unloaded")
		}
	}
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", SettlementPending: []string{"run"}}
	l.states[testAccount] = l.states[""]
	p, err = e.Status(t.Context())
	if err != nil || p.Stage != "disconnected" || !a.computerCleaned {
		t.Fatalf("idle revoked cleanup did not converge: %s %v", p.Stage, err)
	}
	if _, err = e.Store.Read("runtime.key", 4096); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime credential retained")
	}
	s, err := e.load()
	if err != nil || s.Runtime != "" || s.Device != "" {
		t.Fatal("runtime or redemption secret retained in snapshot")
	}
	if _, err = e.Status(t.Context()); err != nil {
		t.Fatal("repeated cleanup failed")
	}
}
func TestServiceConflictNeverStartsAndManagedSymlinkPreserved(t *testing.T) {
	e, _, _, o, x := engineFixture(t)
	x.active = true
	if _, err := e.Begin(t.Context(), o); !errors.Is(err, ErrServiceConflict) {
		t.Fatal("unowned loaded service adopted")
	}
	x.active = false
	dir, name, _ := e.Services.location()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(physicalTemp(t), "managed.plist")
	if err := os.WriteFile(target, []byte("managed fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Begin(t.Context(), o); !errors.Is(err, ErrDeclarative) {
		t.Fatal("managed service overwritten")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "managed fixture" {
		t.Fatal("managed service changed")
	}
}
