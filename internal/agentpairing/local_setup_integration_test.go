// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

// handlerTransport keeps the production HTTPS origin while exercising the real
// HTTP client, auth middleware, pairing handlers, and disposable database.
type handlerTransport struct{ handler http.Handler }

func (x handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "pairing.test" {
		return nil, errors.New("unexpected pairing destination")
	}
	w := httptest.NewRecorder()
	x.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

type localSetupExecutor struct{ executable string }

func (x localSetupExecutor) Run(_ context.Context, c agentsetup.Command) ([]byte, error) {
	if c.Path == "/bin/ps" {
		return []byte(fmt.Sprintf("%d %s\n", os.Getpid(), x.executable)), nil
	}
	if c.Path == "/bin/launchctl" && len(c.Args) > 0 && c.Args[0] == "print" {
		return nil, &agentsetup.CommandError{ExitCode: 113}
	}
	return nil, errors.New("unexpected service operation")
}

type localSetupDaemon struct {
	root     string
	active   map[string]bool
	fenced   map[string]bool
	statuses map[string]string
}

func (d *localSetupDaemon) daemonID() (string, error) {
	c, err := agentsetup.ReadRuntimeConfig(d.root)
	if err != nil {
		return "", err
	}
	return c.DaemonID, nil
}

func (d *localSetupDaemon) Fence(_ context.Context, _, account string) (agentsetup.LocalStatus, error) {
	d.fenced[account] = true
	return d.Status(context.Background(), account)
}

func (d *localSetupDaemon) Status(_ context.Context, account string) (agentsetup.LocalStatus, error) {
	id, err := d.daemonID()
	if err != nil {
		return agentsetup.LocalStatus{}, err
	}
	state := "drained"
	if account == "" {
		state = "ready"
		if d.fenced[""] {
			state = "drained"
		}
		for _, active := range d.active {
			if active {
				state = "running"
			}
		}
	} else if d.active[account] {
		state = "running"
	}
	return agentsetup.LocalStatus{Ready: true, DaemonID: id, State: state, HarnessStatuses: d.statuses}, nil
}

func physicalSetupTemp(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func setupAccount(t *testing.T, f *fixture, harness, home string) agentsetup.Candidate {
	t.Helper()
	path := filepath.Join(home, harness+"-fixture")
	if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	return agentsetup.Candidate{Harness: harness, Label: harness + " test account", ProfileID: f.profiles[harness], Path: path, Home: home, Identity: harness + "@example.test", Login: "signed_in", Version: "1.0.0"}
}

func assertNoRuntimeKey(t *testing.T, key string, p agentsetup.Progress) {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), key) {
		t.Fatal("public setup progress exposed runtime authority")
	}
}

func TestLocalSetupHTTPPairingAddHarnessAndSelectiveDrain(t *testing.T) {
	f := newFixture(t)
	home := physicalSetupTemp(t)
	workspace := physicalSetupTemp(t)
	store, err := agentsetup.OpenStore(filepath.Join(home, "setup-state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	serviceExe := filepath.Join(home, "agentd-fixture")
	if err := os.WriteFile(serviceExe, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(home, "bin", "node")
	if err := os.MkdirAll(filepath.Dir(nodePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodePath, []byte("synthetic Node fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(home, "lib", "node_modules", "@anthropic-ai", "claude-agent-sdk")
	if err := os.MkdirAll(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(`{"name":"@anthropic-ai/claude-agent-sdk","main":"sdk.mjs"}`), 0600); err != nil {
		t.Fatal(err)
	}
	sdkEntry := filepath.Join(packageDir, "sdk.mjs")
	if err := os.WriteFile(sdkEntry, []byte("synthetic SDK fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	local := &localSetupDaemon{root: store.Path(), active: map[string]bool{}, fenced: map[string]bool{}}
	now := time.Now().UTC()
	e := &agentsetup.Engine{
		Store:    store,
		API:      agentsetup.HTTPClient{Origin: origin, HTTP: &http.Client{Transport: handlerTransport{f.h}}},
		Local:    local,
		Services: &agentsetup.ServiceManager{FixtureLabel: "cm.aeon.fixture.pair5-integration", Platform: agentsetup.Platform{OS: "darwin", Arch: "arm64"}, Home: home, UID: os.Getuid(), Executable: serviceExe, Executor: localSetupExecutor{serviceExe}},
		Now:      func() time.Time { return now },
	}
	// Use a qualified verification harness; no vendor process is launched by this fixture.
	o := agentsetup.Options{Origin: origin, ComputerName: "integration computer", Workspace: workspace, Platform: agentsetup.Platform{OS: "darwin", Arch: "arm64"}, Candidates: []agentsetup.Candidate{setupAccount(t, f, "claude", home)}, NodePath: nodePath, ClaudeSDKPath: sdkEntry}
	p, err := e.Begin(t.Context(), o)
	if err != nil || p.Stage != "awaiting_approval" || p.UserCode == "" {
		t.Fatalf("initial code request: stage=%s err=%v", p.Stage, err)
	}
	if _, _, err := agentsetup.ReadRuntime(store.Path()); err == nil {
		t.Fatal("runtime authority installed before person approval")
	}
	var review agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/lookup", map[string]string{"user_code": p.UserCode}, true, "", 200), &review)
	if review.State != "pending" || len(review.Requested) != 1 {
		t.Fatal("person review did not show local request")
	}
	decodeResult(t, f.call("POST", "/api/agent-pairing/requests/"+p.RequestID+"/approve", map[string]any{"request_digest": review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{review.Requested[0].AccountKey}}, true, "", 200), &review)
	if review.RuntimePrefix != "" || review.Connectivity == "connected" {
		t.Fatal("approval exposed credential or claimed live connectivity")
	}
	now = now.Add(time.Minute)
	p, err = e.Step(t.Context())
	if err != nil || p.Stage != "verification_pending" {
		t.Fatalf("redeem and provision: stage=%s err=%v", p.Stage, err)
	}
	config, key, err := agentsetup.ReadRuntime(store.Path())
	if err != nil || len(config.Accounts) != 1 || config.ComputerID != p.ComputerID {
		t.Fatalf("runtime binding: accounts=%d err=%v", len(config.Accounts), err)
	}
	assertNoRuntimeKey(t, string(key), p)
	var initial agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+config.ComputerID, nil, true, "", 200), &initial)
	if len(initial.Enrollments) != 1 || initial.Enrollments[0].VerificationRunID == nil {
		t.Fatal("approved verification run missing")
	}
	f.probe(initial, initial.Enrollments[0], string(key), 200)
	reservation := f.reserve(initial, initial.Enrollments[0], string(key), 200)
	f.claim(initial, initial.Enrollments[0], string(key), reservation, 200)
	if err := e.SyncFences(t.Context()); err != nil {
		t.Fatalf("connected progress reconciliation: %v", err)
	}
	p, err = e.Status(t.Context())
	if err != nil || p.Stage != "verification_pending" {
		t.Fatalf("connected status truth: stage=%s err=%v", p.Stage, err)
	}
	assertNoRuntimeKey(t, string(key), p)

	p, err = e.AddHarness(t.Context(), []agentsetup.Candidate{setupAccount(t, f, "cursor", home)})
	if err != nil || p.Stage != "awaiting_approval" || p.UserCode == "" {
		t.Fatalf("Add harness code request: stage=%s err=%v", p.Stage, err)
	}
	decodeResult(t, f.call("POST", "/api/agent-pairing/lookup", map[string]string{"user_code": p.UserCode}, true, "", 200), &review)
	if review.ExistingComputerID != config.ComputerID || len(review.Requested) != 1 || review.Requested[0].Harness != "cursor" {
		t.Fatal("Add harness review lost computer or account binding")
	}
	decodeResult(t, f.call("POST", "/api/agent-pairing/requests/"+p.RequestID+"/approve", map[string]any{"request_digest": review.Digest, "verification": "connect_only", "selected_account_keys": []string{review.Requested[0].AccountKey}}, true, "", 200), &review)
	now = now.Add(time.Minute)
	p, err = e.Step(t.Context())
	if err != nil {
		t.Fatalf("Add harness redeem: stage=%s err=%v", p.Stage, err)
	}
	updated, sameKey, err := agentsetup.ReadRuntime(store.Path())
	if err != nil || len(updated.Accounts) != 2 || updated.ComputerID != config.ComputerID || string(sameKey) != string(key) {
		t.Fatalf("Add harness changed runtime binding: accounts=%d err=%v", len(updated.Accounts), err)
	}
	assertNoRuntimeKey(t, string(key), p)

	first, second := updated.Accounts[0].AccountID, updated.Accounts[1].AccountID
	local.active[first] = true
	p, err = e.Disconnect(t.Context(), first)
	if err != nil || p.Stage != "draining" || !local.fenced[first] || local.fenced[second] {
		t.Fatalf("selective active drain: stage=%s err=%v", p.Stage, err)
	}
	kept, keptKey, err := agentsetup.ReadRuntime(store.Path())
	if err != nil || len(kept.Accounts) != 2 || string(keptKey) != string(key) {
		t.Fatal("active local work was cleaned before drain completed")
	}
	local.active[first] = false
	p, err = e.Step(t.Context())
	if err != nil || p.Stage != "draining" {
		t.Fatalf("server active run was cleaned before settlement: stage=%s err=%v", p.Stage, err)
	}
	f.telemetry(initial, initial.Enrollments[0], string(key), 200)
	p, err = e.Step(t.Context())
	if err != nil || p.Stage != "connected" {
		t.Fatalf("selective cleanup reconciliation: stage=%s err=%v", p.Stage, err)
	}
	kept, keptKey, err = agentsetup.ReadRuntime(store.Path())
	if err != nil || len(kept.Accounts) != 1 || kept.Accounts[0].AccountID != second || string(keptKey) != string(key) {
		t.Fatal("selective cleanup removed shared authority or other account")
	}
	assertNoRuntimeKey(t, string(key), p)
	local.statuses = map[string]string{"pi": "future_state", "codex": "future_state", "cursor": "ready"}
	delete(local.fenced, first)
	if err := e.SyncFences(t.Context()); err != nil || !local.fenced[first] || local.fenced[second] {
		t.Fatal("unknown telemetry prevented HTTP fence synchronization", err)
	}
	local.active[second] = true
	p, err = e.Disconnect(t.Context(), "")
	if err != nil || p.Stage != "draining" || !local.fenced[""] {
		t.Fatalf("whole-computer drain: stage=%s err=%v", p.Stage, err)
	}
	if _, _, err = agentsetup.ReadRuntime(store.Path()); err != nil {
		t.Fatal("whole-computer drain removed runtime before local work exited")
	}
	local.active[second] = false
	p, err = e.Step(t.Context())
	if err != nil || p.Stage != "disconnected" || p.ServerRevocation != "confirmed" {
		t.Fatalf("whole-computer cleanup: stage=%s revocation=%s err=%v", p.Stage, p.ServerRevocation, err)
	}
	if _, _, err = agentsetup.ReadRuntime(store.Path()); err == nil {
		t.Fatal("disconnected computer retained runtime authority")
	}
}
