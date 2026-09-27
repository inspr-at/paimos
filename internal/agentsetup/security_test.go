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
