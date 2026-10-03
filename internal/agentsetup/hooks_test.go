// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const hookCanary = "fixture-private-canary-NOT-A-CREDENTIAL-391"

type hookFixture struct {
	home, workspace, source, settings string
	installer                         *HookInstaller
}

func newHookFixture(t *testing.T) hookFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := hookFixture{home: filepath.Join(root, "home"), workspace: filepath.Join(root, "project"), source: filepath.Join(root, "release", "aeon")}
	for _, dir := range []string{f.home, f.workspace, filepath.Dir(f.source), filepath.Join(f.home, ".claude")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.source, []byte("#!/bin/sh\nexit 0\n"), 0500); err != nil {
		t.Fatal(err)
	}
	// Public Linux evidence is retained with the pin. This fixture verifier
	// checks its executable digest; native signed-manifest tests run separately.
	for suffix, raw := range map[string]string{".manifest.json": "public-fixture-manifest", ".manifest.sig": "public-fixture-signature"} {
		if err := os.WriteFile(f.source+suffix, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.settings = filepath.Join(f.home, ".claude", "settings.json")
	if err := os.WriteFile(f.settings, []byte(`{"env":{"PRIVATE":"`+hookCanary+`"},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"unrelated"}]}]}}`), 0640); err != nil {
		t.Fatal(err)
	}
	want := Hash([]byte("#!/bin/sh\nexit 0\n"))
	f.installer = &HookInstaller{Enabled: true, Scope: "user", Executable: f.source, qualify: func(string, string, string) string { return "" }, authenticate: func(_ context.Context, path, digest string) error {
		if digest != want {
			return errHookArtifact
		}
		return nil
	}}
	return f
}

func (f hookFixture) apply(t *testing.T, uninstall bool) string {
	t.Helper()
	c := f.installer.apply(t.Context(), f.home, f.workspace, testComputer, "claude", "2.fixture", runtime.GOOS, uninstall, nil)
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), hookCanary) || strings.Contains(string(raw), f.home) {
		t.Fatal("diagnostic leaked local data")
	}
	if c.Verified != (c.Blocker == "") {
		t.Fatal("inconsistent capability")
	}
	return c.Blocker
}

func TestPairedHooksPinIgnoresPATHAndMovingProfile(t *testing.T) {
	f := newHookFixture(t)
	profile := filepath.Join(f.home, "profile")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(profile, "aeon")
	if err := os.Symlink(f.source, link); err != nil {
		t.Fatal(err)
	}
	f.installer.Executable = link
	t.Setenv("PATH", filepath.Join(f.home, "malicious"))
	beforeProject := projectHookFiles(t, f.workspace)
	if got := f.apply(t, false); got != "" {
		t.Fatal(got)
	}
	var doc struct {
		Hooks map[string][]struct{ Hooks []struct{ Command string } }
	}
	raw, _ := os.ReadFile(f.settings)
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("bad settings")
	}
	for _, event := range hookEvents {
		if len(doc.Hooks[event]) != 1 || len(doc.Hooks[event][0].Hooks) != 1 {
			t.Fatal("duplicate consumer")
		}
		cmd := doc.Hooks[event][0].Hooks[0].Command
		if !strings.HasPrefix(cmd, "'"+filepath.Join(f.home, ".local", "share", "aeon", "hooks", "aeon-")) || !strings.Contains(cmd, " --paired") {
			t.Fatal("not pinned")
		}
		for _, forbidden := range []string{link, "--config", "--instance", hookCanary, "$PATH", ".profile", f.workspace} {
			if strings.Contains(cmd, forbidden) {
				t.Fatal("unsafe command")
			}
		}
	}
	if beforeProject != projectHookFiles(t, f.workspace) {
		t.Fatal("project changed")
	}
	// Exact duplicates are repaired down to one integration.
	if got := f.apply(t, false); got != "" {
		t.Fatal(got)
	}
	store, err := OpenStore(filepath.Join(f.home, ".local", "share", "aeon", "hooks"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receiptRaw, _ := store.Read("claude.json", 64<<10)
	var receipt HookReceipt
	_ = json.Unmarshal(receiptRaw, &receipt)
	publicPin, configDigest, err := readUserHookIdentityVerified(t.Context(), f.home, testComputer, "claude", f.installer.authenticate)
	if err != nil || publicPin != receipt.Pin || len(configDigest) != 64 {
		t.Fatal("runtime identity projection unavailable")
	}
	if _, _, err = readUserHookIdentityVerified(t.Context(), f.home, "other-computer", "claude", f.installer.authenticate); err == nil {
		t.Fatal("another pairing acquired hook identity")
	}
	if receipt.Pin.matchesLoadedImage(t.Context(), receipt.Pin.Device, receipt.Pin.Inode, f.installer.authenticate) != nil || receipt.Pin.matchesLoadedImage(t.Context(), receipt.Pin.Device, receipt.Pin.Inode+1, f.installer.authenticate) == nil {
		t.Fatal("loaded image identity not enforced")
	}
	if err := os.Chmod(receipt.Pin.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt.Pin.Path, []byte("swapped"), 0500); err != nil {
		t.Fatal(err)
	}
	if receipt.Pin.Check() == nil {
		t.Fatal("post-approval executable swap accepted")
	}
	if _, _, err = readUserHookIdentity(f.home, testComputer, "claude"); err == nil {
		t.Fatal("changed artifact retained runtime identity")
	}
}

func projectHookFiles(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(path)
		if !d.IsDir() {
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			b.Write(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestPairedHooksPostApprovalSourceSwap(t *testing.T) {
	f := newHookFixture(t)
	f.installer.authenticate = func(context.Context, string, string) error {
		_ = os.Chmod(f.source, 0700)
		_ = os.WriteFile(f.source, []byte("changed"), 0700)
		return nil
	}
	before, _ := os.ReadFile(f.settings)
	if got := f.apply(t, false); got != "artifact_changed" {
		t.Fatal(got)
	}
	after, _ := os.ReadFile(f.settings)
	if !bytes.Equal(before, after) {
		t.Fatal("settings changed after artifact swap")
	}
}

func TestPairedHooksRejectChecksumOnlyAndWrongSigner(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	digest := Hash([]byte("artifact"))
	release := hookRelease{VersionScheme: "inspr-calendar-v2", Version: "260930000000.0.0", OS: "linux", Arch: "arm64", SHA256: digest}
	raw, _ := json.Marshal(map[string]string{"Schema": "aeon.hook-release.v1", "Artifact": "aeon-cli", "VersionScheme": release.VersionScheme, "Version": release.Version, "OS": release.OS, "Arch": release.Arch, "SHA256": digest})
	sig := ed25519.Sign(private, raw)
	if verifyHookManifest(raw, sig, pub, release) != nil {
		t.Fatal("valid release rejected")
	}
	for _, test := range []struct {
		name               string
		signature, key     []byte
		digest, goos, arch string
	}{
		{"checksum-only", nil, pub, digest, "linux", "arm64"},
		{"wrong-signer", sig, other, digest, "linux", "arm64"},
		{"substitution", sig, pub, Hash([]byte("other")), "linux", "arm64"},
		{"wrong-platform", sig, pub, digest, "darwin", "arm64"},
		{"wrong-arch", sig, pub, digest, "linux", "amd64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := release
			want.SHA256, want.OS, want.Arch = test.digest, test.goos, test.arch
			if verifyHookManifest(raw, test.signature, test.key, want) == nil {
				t.Fatal("unauthenticated release accepted")
			}
		})
	}
	f := newHookFixture(t)
	f.installer.authenticate = authenticateHook
	if got := f.apply(t, false); got != "artifact_untrusted" {
		t.Fatal("unsigned native artifact accepted", got)
	}
	if runtime.GOOS == "darwin" && verifyHookAppleSignature(t.Context(), "/bin/ls") == nil {
		t.Fatal("Apple system signer substituted for Aeon release signer")
	}
}

func TestPairedHooksEnvironmentScopeAndOverrides(t *testing.T) {
	for _, key := range []string{"HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "AEON_CONFIG", "PAIMOS_CONFIG"} {
		t.Run(key, func(t *testing.T) {
			f := newHookFixture(t)
			c := f.installer.apply(t.Context(), f.home, f.workspace, testComputer, "claude", "2.fixture", runtime.GOOS, false, []string{key + "=" + hookCanary})
			if c.Blocker != "config_environment" {
				t.Fatal(c)
			}
		})
	}
	for _, ancestor := range []bool{false, true} {
		t.Run(map[bool]string{false: "project", true: "ancestor"}[ancestor], func(t *testing.T) {
			f := newHookFixture(t)
			parent := f.workspace
			if ancestor {
				f.workspace = filepath.Join(parent, "nested")
				_ = os.Mkdir(f.workspace, 0700)
			}
			_ = os.Mkdir(filepath.Join(parent, ".claude"), 0700)
			_ = os.WriteFile(filepath.Join(parent, ".claude", "settings.local.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"aeon hook claude Stop # aeon-inbox-hook-v1"}]}]}}`), 0600)
			before := projectHookFiles(t, parent)
			if got := f.apply(t, false); got != "project_override" {
				t.Fatal(got)
			}
			if before != projectHookFiles(t, parent) {
				t.Fatal("project changed")
			}
		})
	}
	f := newHookFixture(t)
	f.installer.Scope = "project"
	if got := f.apply(t, false); got != "project_scope" {
		t.Fatal(got)
	}
}

func TestPairedHooksDuplicateEntriesAndUninstallPreservation(t *testing.T) {
	f := newHookFixture(t)
	original, _ := os.ReadFile(f.settings)
	if got := f.apply(t, false); got != "" {
		t.Fatal(got)
	}
	raw, _ := os.ReadFile(f.settings)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	hooks := doc["hooks"].(map[string]any)
	stop := hooks["Stop"].([]any)
	hooks["Stop"] = append(stop, stop[0])
	raw, _ = json.Marshal(doc)
	_ = os.WriteFile(f.settings, raw, 0640)
	if got := f.apply(t, false); got != "" {
		t.Fatal(got)
	}
	raw, _ = os.ReadFile(f.settings)
	if strings.Count(string(raw), pairedHookMarker) != 3 {
		t.Fatal("duplicates survived")
	}
	// A suffix cannot prove v1 ownership; refuse the entire transaction.
	_ = json.Unmarshal(raw, &doc)
	hooks = doc["hooks"].(map[string]any)
	hooks["Stop"] = append(hooks["Stop"].([]any), map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "other hook # aeon-inbox-hook-v1"}}})
	mixed, _ := json.Marshal(doc)
	_ = os.WriteFile(f.settings, mixed, 0640)
	if got := f.apply(t, false); got != "ownership_unknown" {
		t.Fatal(got)
	}
	after, _ := os.ReadFile(f.settings)
	if !bytes.Equal(after, mixed) {
		t.Fatal("unknown v1 entry overwritten")
	}
	_ = os.WriteFile(f.settings, raw, 0640)
	// A changed owned group (including timeout/matcher) is retained on uninstall.
	changed := bytes.Replace(raw, []byte(`"timeout": 3`), []byte(`"timeout": 30`), 1)
	_ = os.WriteFile(f.settings, changed, 0640)
	if got := f.apply(t, true); got != "ownership_unknown" {
		t.Fatal(got)
	}
	after, _ = os.ReadFile(f.settings)
	if !bytes.Equal(after, changed) {
		t.Fatal("modified entry removed")
	}
	_ = os.WriteFile(f.settings, raw, 0640)
	if got := f.apply(t, true); got != "repair_required" {
		t.Fatal(got)
	}
	after, _ = os.ReadFile(f.settings)
	if !sameHookJSON(original, after) {
		t.Fatal("unrelated settings lost")
	}
}

func TestPairedHooksConcurrentEditAndSymlinkPreserved(t *testing.T) {
	f := newHookFixture(t)
	concurrent := []byte(`{"editor":"preserve"}`)
	f.installer.beforeCommit = func() { _ = os.WriteFile(f.settings, concurrent, 0640) }
	if got := f.apply(t, false); got != "settings_changed" {
		t.Fatal(got)
	}
	after, _ := os.ReadFile(f.settings)
	if !bytes.Equal(after, concurrent) {
		t.Fatal("concurrent settings lost")
	}
	// An ancestor symlink must not redirect the pinned directory descriptor.
	g := newHookFixture(t)
	original := g.home
	g.home = filepath.Join(filepath.Dir(g.home), "alias")
	_ = os.Symlink(original, g.home)
	if got := g.apply(t, false); got != "config_provenance" {
		t.Fatal(got)
	}
}

func TestPairedHooksQualificationCannotBeClaimed(t *testing.T) {
	f := newHookFixture(t)
	f.installer.qualify = nil
	if got := f.apply(t, false); got != "qualification_pending" {
		t.Fatal(got)
	}
	f.installer.Enabled = false
	if got := f.apply(t, false); got != "feature_disabled" {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".local")); !os.IsNotExist(err) {
		t.Fatal("unqualified installer wrote state")
	}
	for _, raw := range []string{`{"hooks":{},"hooks":{}}`, `{"hooks":{"Stop":[],"Stop":[]}}`, `{} {}`} {
		if uniqueHookJSON([]byte(raw)) {
			t.Fatal("ambiguous config accepted")
		}
	}
}

func TestPairedHooksSetupAndExistingComputerRepairStayOff(t *testing.T) {
	e, api, _, options, _ := engineFixture(t)
	approveFixture(t, e, api, options)
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.HookCapabilities) != 1 || s.HookCapabilities[0].Verified || s.HookCapabilities[0].Blocker != "feature_disabled" {
		t.Fatal("pairing silently enabled hooks", s.HookCapabilities)
	}
	before, err := e.Store.Read(RuntimeName, 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an existing computer with no stage-2 record, then repair without
	// a native qualification. This changes no credential, consent or service.
	s.HookCapabilities = nil
	if err = e.save(s, false); err != nil {
		t.Fatal(err)
	}
	e.Hooks = &HookInstaller{Enabled: true, Scope: "user", Executable: options.Candidates[0].Path}
	p, err := e.RepairHooks(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.HookCapabilities) != 1 || p.HookCapabilities[0].Verified {
		t.Fatal("repair promoted unsupported hook")
	}
	after, err := e.Store.Read(RuntimeName, 128<<10)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("repair changed runtime authority")
	}
	s, err = e.load()
	if err != nil {
		t.Fatal(err)
	}
	proof := e.proof(s)
	if len(proof.HookCapabilities) != 1 || proof.HookCapabilities[0].Verified {
		t.Fatal("status lost blocker")
	}
}

func TestPairedHooksEscapedOverrideAndMissingReceipt(t *testing.T) {
	f := newHookFixture(t)
	_ = os.Mkdir(filepath.Join(f.workspace, ".claude"), 0700)
	_ = os.WriteFile(filepath.Join(f.workspace, ".claude", "settings.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"\u0061eon hook claude Stop"}]}]}}`), 0600)
	if got := f.apply(t, false); got != "project_override" {
		t.Fatal(got)
	}
	if aeonHookGroup([]byte(`{"hooks":[{"command":"aeon rules receive"}]}`)) {
		t.Fatal("unrelated rules hook became delivery authority")
	}
	g := newHookFixture(t)
	_ = os.WriteFile(g.settings, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"aeon hook claude Stop --paired # aeon-inbox-hook-v2"}]}]}}`), 0600)
	if got := g.apply(t, false); got != "ownership_unknown" {
		t.Fatal("marker claimed ownership", got)
	}
}
