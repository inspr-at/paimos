// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

type pairingTransport func(*http.Request) (*http.Response, error)

// Realistic short home paths keep the prompt tests independent of macOS's
// unusually long test temp root; socket length refusal has its own test.
func pairingShortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "aeon-pair-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func (f pairingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPairOneCommandCreatesDefaultStateDisplaysCodeAndResumes(t *testing.T) {
	home, err := filepath.EvalSymlinks(pairingShortTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Chdir(workspace)
	bin := filepath.Join(home, ".nix-profile", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	// This fixture exposes status only; no real vendor command or credential is used.
	vendor := "#!/bin/sh\ncase \"$1\" in\n--version) echo 1.2.3;;\nstatus) echo '{\"status\":\"authenticated\",\"isAuthenticated\":true,\"userInfo\":{\"userId\":\"fixture\",\"email\":\"fixture@example.test\"}}';;\n*) exit 97;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "cursor-agent"), []byte(vendor), 0700); err != nil {
		t.Fatal(err)
	}
	service := filepath.Join(home, "Library", "LaunchAgents", "cm.aeon.agentd.plist")
	if runtime.GOOS == "linux" {
		service = filepath.Join(home, ".config", "systemd", "user", "cm.aeon.agentd.service")
	}
	if err := os.MkdirAll(filepath.Dir(service), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "declarative-service.fixture")
	if err := os.WriteFile(target, []byte("preserve declarative service"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, service); err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	creates := 0
	var request agentsetup.DeviceRequest
	http.DefaultTransport = pairingTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "pairing.example.test" {
			t.Fatal("unexpected origin")
		}
		var reply any
		switch r.URL.Path {
		case "/api/agent-pairing/guide":
			reply = map[string]any{"instance_url": "https://pairing.example.test", "protocol": "pairing-v1", "default_tenant_slug": "fixture"}
		case "/api/agent-pairing/device":
			creates++
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal("invalid device request")
			}
			if request.Workspace != workspace || request.TenantSlug != "fixture" || len(request.Accounts) != 1 || request.Accounts[0].Harness != "cursor" {
				t.Fatal("default choices lost")
			}
			reply = map[string]any{"request_id": request.RequestID, "tenant_id": "11111111-1111-4111-8111-111111111111", "user_code": "123-456-789", "state": "pending", "request_digest": strings.Repeat("a", 64), "verification_uri": "https://pairing.example.test/agents/register-agent"}
		case "/api/agent-pairing/redeem":
			// A loaded CI runner may resume after the five-second polling floor.
			reply = agentsetup.View{RequestID: request.RequestID, TenantID: "11111111-1111-4111-8111-111111111111", State: "pending", Digest: strings.Repeat("a", 64), ComputerName: request.ComputerName, Platform: request.Platform, Arch: request.Arch, Workspace: request.Workspace, Requested: request.Accounts}
		default:
			t.Fatal("unexpected API call before person approval")
		}
		raw, err := json.Marshal(reply)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}, err
	})
	var out bytes.Buffer
	err = setupCommandInput("pair", []string{"--url", "https://pairing.example.test", "--once"}, strings.NewReader("yes\nyes\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if creates != 1 || !strings.Contains(out.String(), "Pairing code: 123-456-789") || !strings.Contains(out.String(), "Pair cursor") {
		t.Fatal("short command did not offer account and show code")
	}
	root, err := agentsetup.DefaultStateRoot(runtime.GOOS, home, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := agentsetup.Engine{Store: store}
	saved, err := engine.SavedOptions()
	if err != nil || saved.StartService || saved.Workspace != workspace {
		t.Fatal("managed pairing tried to own the service")
	}
	if _, err := os.Stat(filepath.Join(root, agentsetup.RuntimeName)); !os.IsNotExist(err) {
		t.Fatal("runtime created before approval")
	}
	// Even from another folder, bare pair resumes the saved instance and choices.
	t.Chdir(home)
	out.Reset()
	if err := setupCommandInput("pair", []string{"--once"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || !strings.Contains(out.String(), "Pairing code: 123-456-789") {
		t.Fatal("resume requested new authority")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "preserve declarative service" {
		t.Fatal("managed service modified")
	}
}

func TestSetupPromptOffersAccountsAndRequiresAffirmativeChoice(t *testing.T) {
	var out bytes.Buffer
	p := setupPrompt{in: bufio.NewReader(strings.NewReader("no\nyes\n")), out: &out}
	candidates := []agentsetup.Candidate{{Harness: "claude", Label: "first@example.test"}, {Harness: "codex", Label: "second@example.test"}}
	got, err := p.selectHarnesses(candidates)
	if err != nil || len(got) != 1 || got[0].Harness != "codex" {
		t.Fatalf("selection: %v", err)
	}
	if !strings.Contains(out.String(), "first@example.test") || !strings.Contains(out.String(), "second@example.test") {
		t.Fatal("accounts not offered")
	}
	for _, input := range []string{"", "\n\n", "no\nno\n"} {
		p.in = bufio.NewReader(strings.NewReader(input))
		if _, err := p.selectHarnesses(candidates); err == nil {
			t.Fatal("implicit consent accepted")
		}
	}
	p.json = true
	out.Reset()
	if _, err := p.selectHarnesses(candidates); err == nil || out.Len() != 0 {
		t.Fatal("JSON output prompted")
	}
}

func TestPairDefaultsConfirmWorkspaceBeforeCreatingState(t *testing.T) {
	home, err := filepath.EvalSymlinks(pairingShortTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Chdir(workspace)
	var out bytes.Buffer
	err = setupCommandInput("pair", []string{"--url", "https://example.test"}, strings.NewReader("no\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "declined") || !strings.Contains(out.String(), workspace) {
		t.Fatalf("workspace confirmation: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("declined setup wrote state")
	}
	out.Reset()
	err = setupCommandInput("pair", []string{"--url", "https://example.test", "--json"}, strings.NewReader("yes\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "--workspace") || out.Len() != 0 {
		t.Fatal("JSON setup implicitly accepted folder")
	}
	// Bare pair asks for the guide origin; it does not guess a deployment.
	err = setupCommandInput("pair", nil, strings.NewReader("https://other.example.test\nno\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "declined") || !strings.Contains(out.String(), "Aeon address") {
		t.Fatalf("bare pair origin: %v", err)
	}
}

func TestPairRejectsUnsafeExplicitRootWithoutWrites(t *testing.T) {
	home, err := filepath.EvalSymlinks(pairingShortTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	root := filepath.Join(home, "workspace", "new", "paired")
	var out bytes.Buffer
	err = setupCommandInput("pair", []string{"--url", "https://example.test", "--state-root", root, "--workspace", filepath.Join(home, "workspace"), "--harness", "cursor"}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("unsafe workspace state accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(root)); !os.IsNotExist(err) {
		t.Fatal("unsafe root created")
	}
}

func TestPairFromHomeExplainsWorkspaceBeforeConfirmationOrWrites(t *testing.T) {
	home, err := filepath.EvalSymlinks(pairingShortTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Chdir(home)
	for _, extra := range [][]string{nil, {"--workspace", home}, {"--json"}} {
		var out bytes.Buffer
		args := append([]string{"--url", "https://example.test"}, extra...)
		err = setupCommandInput("pair", args, strings.NewReader("yes\n"), &out)
		if err == nil || !strings.Contains(err.Error(), "--workspace") || !strings.Contains(err.Error(), "home folder") || !strings.Contains(err.Error(), "--state-root") {
			t.Fatalf("home-folder recovery missing: %v", err)
		}
		if out.Len() != 0 {
			t.Fatal("asked for confirmation before rejecting impossible workspace")
		}
		entries, err := os.ReadDir(home)
		if err != nil || len(entries) != 0 {
			t.Fatal("home-folder rejection created pairing state")
		}
	}
}

func TestPairSetupAndServeRefuseImpossibleSocketBeforeSideEffects(t *testing.T) {
	home, err := filepath.EvalSymlinks(pairingShortTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(home, strings.Repeat("long-home-", 12))
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	root := filepath.Join(home, "paired")
	for _, command := range []string{"pair", "setup", "serve"} {
		var out bytes.Buffer
		var err error
		if command == "serve" {
			err = serve([]string{"--setup-root", root})
		} else {
			err = setupCommandInput(command, []string{"--state-root", root}, strings.NewReader(""), &out)
		}
		if err == nil || !strings.Contains(err.Error(), "The agentd socket path is too long for this system") || !strings.Contains(err.Error(), "Use a shorter --setup-root") {
			t.Fatalf("%s preflight: %v", command, err)
		}
		if out.Len() != 0 {
			t.Fatalf("%s prompted before refusal", command)
		}
		entries, err := os.ReadDir(home)
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s created state before refusal", command)
		}
	}
}
