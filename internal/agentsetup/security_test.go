// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type executorFunc func(context.Context, Command) ([]byte, error)

func (f executorFunc) Run(c context.Context, v Command) ([]byte, error) { return f(c, v) }

func TestDiscoveryOffersOnlyInstalledSignedInHarnesses(t *testing.T) {
	home := physicalTemp(t)
	bin := filepath.Join(home, ".nix-profile", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex", "cursor-agent"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	d := Discovery{Home: home, LookPath: func(name string) (string, error) {
		if name == "claude" || name == "codex" || name == "cursor-agent" {
			return filepath.Join(bin, name), nil
		}
		return "", os.ErrNotExist
	}, Executor: executorFunc(func(_ context.Context, c Command) ([]byte, error) {
		if strings.Join(c.Args, " ") == "--version" {
			return []byte("1.2.3"), nil
		}
		switch filepath.Base(c.Path) {
		case "claude":
			return []byte(`{"loggedIn":true,"email":"fixture@example.test","authMethod":"oauth"}`), nil
		case "cursor-agent":
			return []byte(`{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":"fixture","email":"cursor@example.test"}}`), nil
		case "codex":
			return nil, errors.New("not signed in")
		}
		t.Fatal("unexpected command")
		return nil, errors.New("unexpected")
	})}
	candidates := d.Available(t.Context(), "")
	if len(candidates) != 2 || candidates[0].Harness != "claude" || candidates[1].Harness != "cursor" {
		t.Fatalf("offered unexpected harnesses: %d", len(candidates))
	}
	for _, c := range candidates {
		if !c.Managed || c.Login != "signed_in" || !filepath.IsAbs(c.Path) {
			t.Fatal("Nix candidate not pinned and identified")
		}
	}
}

func TestDiscoveryNeverUsesCredentialFilesAndChecksIdentity(t *testing.T) {
	home := physicalTemp(t)
	path := filepath.Join(home, "fake-vendor")
	if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	d := Discovery{Home: home, LookPath: func(string) (string, error) { return path, nil }, CodexIdentity: func(context.Context, string, string) (string, error) { return "personal@example.test", nil }, Executor: executorFunc(func(_ context.Context, c Command) ([]byte, error) {
		calls++
		switch strings.Join(c.Args, " ") {
		case "--version":
			return []byte("codex-cli 0.157.1"), nil
		case "login status":
			if !c.StatusStderr {
				t.Fatal("stderr status discarded")
			}
			return []byte("Logged in using ChatGPT"), nil
		}
		t.Fatal("unexpected vendor command")
		return nil, errors.New("unexpected")
	})}
	c, err := d.Detect(t.Context(), "codex", "personal@example.test")
	if err != nil || c.Identity != "personal@example.test" || c.Login != "signed_in" || calls != 2 {
		t.Fatal("safe identity discovery failed")
	}
	if _, err = d.Detect(t.Context(), "codex", "work@example.test"); err == nil {
		t.Fatal("accepted wrong subscription context")
	}
	d.Executor = executorFunc(func(_ context.Context, c Command) ([]byte, error) {
		if len(c.Args) == 1 {
			return []byte("0.157.1"), nil
		}
		return nil, errors.New("not logged in")
	})
	if c, err = d.Detect(t.Context(), "codex", ""); err == nil || c.Login == "signed_in" || !strings.Contains(err.Error(), "normal login") {
		t.Fatal("missing login was not actionable")
	}
	if _, err = d.Detect(t.Context(), "grok", ""); err == nil {
		t.Fatal("unqualified auth-store probe allowed")
	}
}

func TestHTTPProofCannotFollowRedirectOrLeakRemoteError(t *testing.T) {
	reached := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++; w.WriteHeader(200) }))
	defer target.Close()
	status := 307
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("reflected-private-capability"))
	}))
	defer source.Close()
	c := HTTPClient{Origin: source.URL, HTTP: source.Client()}
	proof, _ := randomSecret()
	_, err := c.Reconcile(t.Context(), ProofRequest{LifecycleSecret: proof})
	if err == nil || reached != 0 || strings.Contains(err.Error(), "reflected") || strings.Contains(err.Error(), string(proof)) {
		t.Fatal("redirect or error disclosure")
	}
	status = 403
	_, err = c.Reconcile(t.Context(), ProofRequest{LifecycleSecret: proof})
	if err == nil || strings.Contains(err.Error(), "reflected") {
		t.Fatal("remote error disclosed")
	}
	for _, origin := range []string{"http://example.test", "https://a:b@example.test", "https://example.test/path", "https://example.test?token=value"} {
		if ValidateOrigin(origin) == nil {
			t.Fatal("unsafe origin allowed")
		}
	}
}

func TestServiceActivationLostResponseResumesOwnedReceipt(t *testing.T) {
	e, _, _, _, x := engineFixture(t)
	m := *e.Services
	bootstraps := 0
	m.Executor = executorFunc(func(ctx context.Context, c Command) ([]byte, error) {
		if len(c.Args) > 0 && c.Args[0] == "bootstrap" {
			bootstraps++
			x.active = true
			return nil, errors.New("lost response")
		}
		return x.Run(ctx, c)
	})
	if _, err := m.Install(t.Context(), e.Store, testComputer, true, nil); err == nil {
		t.Fatal("lost response hidden")
	}
	receipt, err := m.Install(t.Context(), e.Store, testComputer, true, nil)
	if err != nil || !receipt.Activated || bootstraps != 1 {
		t.Fatal("activation duplicated or lost receipt not recovered")
	}
	if err = m.Remove(t.Context(), e.Store, receipt, false); err == nil {
		t.Fatal("unconfirmed work permitted service unload")
	}
}

func TestServiceFixtureNamespaceAndDefinition(t *testing.T) {
	e, _, _, _, _ := engineFixture(t)
	m := *e.Services
	for _, bad := range []string{"at.inspr.aeon-agentd", "cm.aeon.agentd", "../cm.aeon.fixture.test-1234", "cm.aeon.fixture.x\n"} {
		m.FixtureLabel = bad
		if _, err := m.Definition(e.Store.Path()); err == nil {
			t.Fatal("unsafe fixture label")
		}
	}
	m.FixtureLabel = "cm.aeon.fixture.test-1234"
	raw, err := m.Definition(e.Store.Path())
	if err != nil || !strings.Contains(string(raw), m.FixtureLabel) || strings.Contains(string(raw), "runtime.key") {
		t.Fatal("fixture definition unsafe")
	}
	m.Platform = Platform{OS: "linux", Arch: "arm64"}
	raw, err = m.Definition(e.Store.Path())
	if err != nil || !strings.Contains(string(raw), "KillMode=none") || !strings.Contains(string(raw), "TimeoutStopSec=infinity") {
		t.Fatal("systemd could kill drained children")
	}
}

func TestSetupCannotClaimConnectedBeforeAccountProbe(t *testing.T) {
	e, a, l, o, _ := engineFixture(t)
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained"}
	if _, err := e.Begin(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	a.approved = true
	s, _ := e.load()
	s.NextPoll = e.now()
	if err := e.save(s, false); err != nil {
		t.Fatal(err)
	}
	p, err := e.Step(t.Context())
	if err != nil || p.Stage != "provisioning" {
		t.Fatal("approval/socket mistaken for account connectivity")
	}
}

func TestAddHarnessPendingBindingCannotMoveComputer(t *testing.T) {
	e, a, _, o, _ := engineFixture(t)
	approveFixture(t, e, a, o)
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	s.Request.ExistingComputerID = testComputer
	s.Request.RequestID = otherAccount
	s.Response.RequestID = otherAccount
	s.View = View{} // pending response may omit provisioned identity
	v := a.view
	v.RequestID = otherAccount
	v.ExistingComputerID = testComputer
	v.ComputerID = otherAccount
	if validateView(s, v, true) == nil {
		t.Fatal("Add harness changed immutable computer")
	}
	raw, _ := json.Marshal(e.progress(s))
	if strings.Contains(string(raw), string(s.Lifecycle)) || strings.Contains(string(raw), string(s.Runtime)) {
		t.Fatal("safe progress contained capability")
	}
}

func TestAddHarnessApprovalReusesDaemonAndNeverRotatesAuthority(t *testing.T) {
	e, a, _, o, x := engineFixture(t)
	o.StartService = true
	approveFixture(t, e, a, o)
	before, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Harness: "cursor", Label: "selected@example.test", Identity: "42", Path: o.Candidates[0].Path, Login: "signed_in", Version: "1.0.0"}
	a.approved = false
	p, err := e.AddHarness(t.Context(), []Candidate{candidate})
	if err != nil || p.Stage != "awaiting_approval" {
		t.Fatal("add request failed")
	}
	pending, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	if pending.Device == before.Device || pending.Request.RequestID == before.Request.RequestID || pending.Runtime != before.Runtime || pending.Lifecycle != before.Lifecycle || pending.LifecycleRequestID != before.LifecycleRequestID {
		t.Fatal("Add harness authority binding changed")
	}

	// Poll a still-pending request, then let the original daemon reconcile.
	pending.NextPoll = e.now()
	if err = e.save(pending, false); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = e.SyncFences(t.Context()); err != nil {
		t.Fatal("pending Add harness disrupted original computer")
	}
	if permitted, err := e.DispatchPermitted(); err != nil || !permitted {
		t.Fatal("healthy shared daemon was disabled")
	}
	pending, err = e.load()
	if err != nil {
		t.Fatal(err)
	}
	a.approved = true
	pending.NextPoll = e.now()
	if err = e.save(pending, false); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Step(t.Context()); err != nil || p.Stage != "connected" {
		t.Fatal("Add harness did not provision same computer")
	}
	config, _, err := ReadRuntime(e.Store.Path())
	if err != nil || len(config.Accounts) != 2 || config.ComputerID != testComputer {
		t.Fatal("existing enrollment replaced")
	}
	if _, err = e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, c := range x.calls {
		if len(c.Args) > 0 && c.Args[0] == "bootstrap" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatal("shared daemon restarted for Add harness")
	}
}

func TestSetupRefusesExistingUnrelatedRuntimeFile(t *testing.T) {
	e, a, _, o, _ := engineFixture(t)
	if err := e.Store.Write(RuntimeName, []byte("unrelated"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Begin(t.Context(), o); !errors.Is(err, ErrCollision) {
		t.Fatal("adopted unrelated state")
	}
	raw, _ := e.Store.Read(RuntimeName, 4096)
	if string(raw) != "unrelated" || a.createCount != 0 {
		t.Fatal("unrelated state or server changed")
	}
}

func TestTypedProgressDistinguishesMissingLoginAndUnsafeVerification(t *testing.T) {
	e, a, l, o, _ := engineFixture(t)
	approveFixture(t, e, a, o)
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", LoginRequired: true}
	p, err := e.Status(t.Context())
	if err != nil || p.Stage != "login_required" {
		t.Fatal("missing login hidden")
	}
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", HarnessFailed: true, LoginRequired: true}
	p, err = e.Status(t.Context())
	if err != nil || p.Stage != "blocked" || !strings.Contains(p.Action, "harness failed to start") {
		t.Fatal("startup failure presented as login required", err)
	}
	// A harness that fails to start is a per-harness hold, never setup_failed
	// for the whole computer (AEON-347/348).
	if err = e.SyncFences(t.Context()); err != nil || a.progress == nil || a.progress.State != "connected" || a.progress.ErrorCode != "" {
		t.Fatal("startup failure became a computer-wide setup failure", err, a.progress)
	}
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", HarnessFailed: true, ProfilePermissions: true}
	p, err = e.Status(t.Context())
	if err != nil || p.Stage != "blocked" || !strings.Contains(p.Action, "permissions") || strings.Contains(p.Action, "installation") {
		t.Fatal("profile permissions got installation hint", err)
	}
	a.view.Enrollments[0].VerificationRunID = otherAccount
	a.view.Enrollments[0].VerificationState = "queued"
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", Ready: true, VerificationUnavailable: []string{testAccount}}
	p, err = e.Status(t.Context())
	if err != nil || p.Stage != "verification_unavailable" {
		t.Fatal("unsafe adapter presented as verifying")
	}
	progress := observedProgress(a.view, l.states[""])
	if progress.State != "connected" || progress.ErrorCode != "verification_unavailable" {
		t.Fatal("verification refusal masqueraded as installation failure")
	}
	if err = e.SyncFences(t.Context()); err != nil || a.progress == nil || a.progress.State != "connected" || a.progress.ErrorCode != "verification_unavailable" {
		t.Fatal("daemon reconciliation lost typed unavailable status")
	}
	local := l.states[""]
	local.Ready = false
	progress = observedProgress(a.view, local)
	if progress.State != "provisioning" || progress.ErrorCode != "verification_unavailable" {
		t.Fatal("verification refusal claimed unconfirmed connectivity")
	}
	a.view.Enrollments[0].VerificationState = "unavailable"
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", Ready: true}
	p, err = e.Status(t.Context())
	if err != nil || p.Stage != "verification_unavailable" {
		t.Fatal("server-reported unavailable verification was hidden")
	}
}

func TestStatusSurfacesClaudeDependencyAndRepinFailures(t *testing.T) {
	for _, issue := range []string{"Claude dependencies changed/invalid: run aeon-agentd repin --harness claude", "Claude repin pending: waiting for active Claude runs to exit", "Claude CLI executable changed or unavailable; restore the approved physical executable, then retry"} {
		e, a, l, o, _ := engineFixture(t)
		approveFixture(t, e, a, o)
		l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "unconfirmed", HarnessErrors: map[string]string{"claude": issue}, HarnessStatuses: map[string]string{"claude": "blocked"}}
		p, err := e.Status(t.Context())
		if err != nil || p.Stage != "blocked" || p.Action != issue || p.LocalProcesses != "unconfirmed" {
			t.Fatal("specific Claude failure was hidden", p, err)
		}
		progress := observedProgress(a.view, l.states[""])
		if progress.State != "connected" || progress.ErrorCode != "" || progress.HarnessStatuses["claude"] != "blocked" {
			t.Fatal("runtime hold confused with installation failure or harness readiness")
		}
		if err = e.SyncFences(t.Context()); err != nil || a.progress == nil || a.progress.State != "connected" || a.progress.HarnessStatuses["claude"] != "blocked" {
			t.Fatal("runtime hold lost during reconciliation", err)
		}
	}
}

func TestReadyHarnessKeepsComputerConnectedDuringOtherLogin(t *testing.T) {
	p := observedProgress(View{}, LocalStatus{Ready: true, LoginRequired: true, HarnessStatuses: map[string]string{"claude": "login_required", "codex": "ready"}})
	if p.State != "connected" || p.ErrorCode != "" || p.HarnessStatuses["claude"] != "login_required" || p.HarnessStatuses["codex"] != "ready" {
		t.Fatalf("one login hid healthy harness: %+v", p)
	}
}

func TestHarnessDetailsSurviveSetupStatusAndReconcile(t *testing.T) {
	for _, reason := range []string{"repin_pending", "dependency_invalid", "pin_missing"} {
		e, a, l, o, _ := engineFixture(t)
		approveFixture(t, e, a, o)
		detail, ok := HarnessReport("claude", "blocked", reason)
		if !ok {
			t.Fatal("unsupported shared reason")
		}
		l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "unconfirmed", Ready: true, HarnessErrors: map[string]string{"claude": "local-only diagnostic"}, HarnessStatuses: map[string]string{"claude": "blocked", "codex": "ready"}, HarnessDetails: map[string]HarnessDetail{"claude": detail, "codex": {State: "ready"}}}
		p, err := e.Status(t.Context())
		if err != nil || p.HarnessDetails["claude"] != detail || p.HarnessStatuses["codex"] != "ready" {
			t.Fatal("setup status lost details", err)
		}
		if reason == "repin_pending" && (p.Stage != "repin_pending" || !strings.Contains(p.Action, "Waiting for repin")) {
			t.Fatal("normal repin wait presented as fault")
		}
		if err := e.SyncFences(t.Context()); err != nil || a.progress == nil || a.progress.State != "connected" || a.progress.HarnessDetails["claude"] != detail {
			t.Fatal("reconcile lost harness reason/fix", err)
		}
		raw, err := json.Marshal(a.progress)
		if err != nil || strings.Contains(string(raw), "local-only diagnostic") {
			t.Fatal("local diagnostics escaped into pairing progress")
		}
	}
}
