// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessProvenanceHashesAllowlistedFiles(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	body := []byte("do-not-send-this-body\n")
	agents := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agents, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo", "SKILL.md"), []byte("# skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SECRET.txt"), []byte("super-secret-neighbor"), 0o644); err != nil {
		t.Fatal(err)
	}
	lease := filepath.Join(dir, "lease")
	if err := os.WriteFile(lease, []byte("pv1-provenance-lease-00000000000001"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []transcriptRequest
	var leaseHeader string
	srv := transcriptFixture(t, "memory", "note", &calls)
	orig := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Aeon-Worker-Lease") != "" {
			leaseHeader = r.Header.Get("X-Aeon-Worker-Lease")
		}
		orig.ServeHTTP(w, r)
	})
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	code, _, stderr := runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "--json", "harness", "provenance", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", lease, "--instruction", agents, "--instruction", filepath.Join(dir, "demo", "SKILL.md"), "--instruction-version", "AGENTS.md=pin-1", "--prompt-template-version", "260927120000.0.0"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var posted map[string]any
	for _, call := range calls {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/provenance") {
			posted = call.body
		}
	}
	raw, _ := json.Marshal(posted)
	text := string(raw)
	sum := sha256.Sum256(body)
	versionHash := sha256.Sum256([]byte("aeon.harness.provenance.prompt-template\x00" + "260927120000.0.0"))
	if !strings.Contains(text, hex.EncodeToString(sum[:])) || !strings.Contains(text, "AGENTS.md") || !strings.Contains(text, "demo/SKILL.md") || !strings.Contains(text, "pin-1") || !strings.Contains(text, "prompt-template") || !strings.Contains(text, `"hash_kind":"absent"`) {
		t.Fatalf("posted %s", text)
	}
	if strings.Contains(text, hex.EncodeToString(versionHash[:])) || strings.Contains(text, "do-not-send-this-body") || strings.Contains(text, "super-secret-neighbor") || strings.Contains(text, dir) || strings.Contains(text, "pv1-provenance-lease") {
		t.Fatalf("request carried a fabricated digest, contents, a path or the lease: %s", text)
	}
	explicit := strings.Repeat("d", 64)
	code, _, stderr = runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "--json", "harness", "provenance", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", lease, "--prompt-template-version", "260927120000.0.0", "--prompt-template-sha256", explicit}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("explicit digest exit %d: %s", code, stderr)
	}
	var explicitText string
	for _, call := range calls {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/provenance") {
			again, _ := json.Marshal(call.body)
			explicitText = string(again)
		}
	}
	if !strings.Contains(explicitText, `"hash_kind":"content"`) || !strings.Contains(explicitText, explicit) || strings.Contains(explicitText, hex.EncodeToString(versionHash[:])) {
		t.Fatalf("explicit digest posted %s", explicitText)
	}
	if leaseHeader != "pv1-provenance-lease-00000000000001" {
		t.Fatal("worker lease header was not sent")
	}
}

func TestHarnessProvenanceRejectsPrivateAndUnlistedFiles(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	private := filepath.Join(dir, ".ssh")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(private, "AGENTS.md")
	if err := os.WriteFile(secret, []byte("do-not-read"), 0o000); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "harness", "provenance", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", filepath.Join(dir, "missing-lease"), "--instruction", secret}, "")
	if code == 0 || !strings.Contains(stderr, "allowlisted") || strings.Contains(stderr, "do-not-read") || strings.Contains(stderr, secret) {
		t.Fatalf("private path exit %d stderr %q", code, stderr)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "harness", "provenance", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", filepath.Join(dir, "missing-lease"), "--instruction", dir}, "")
	if code == 0 || !strings.Contains(stderr, "allowlisted") || strings.Contains(stderr, dir) {
		t.Fatalf("directory exit %d stderr %q", code, stderr)
	}
	targetDir := filepath.Join(dir, "real")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "AGENTS.md")
	if err := os.WriteFile(target, []byte("symlink-target-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(dir, "alias")
	if err := os.Mkdir(aliasDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(aliasDir, "AGENTS.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "harness", "provenance", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", filepath.Join(dir, "missing-lease"), "--instruction", link}, "")
	if code == 0 || !strings.Contains(stderr, "allowlisted") || strings.Contains(stderr, "symlink-target-body") || strings.Contains(stderr, dir) {
		t.Fatalf("symlink exit %d stderr %q", code, stderr)
	}
	transcript := filepath.Join(dir, "agent-transcripts", "AGENTS.md")
	if err := os.Mkdir(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("transcript-secret"), 0o000); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "harness", "provenance", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", filepath.Join(dir, "missing-lease"), "--instruction", transcript}, "")
	if code == 0 || !strings.Contains(stderr, "allowlisted") || strings.Contains(stderr, "transcript-secret") || strings.Contains(stderr, dir) {
		t.Fatalf("transcript exit %d stderr %q", code, stderr)
	}
	wide := filepath.Join(dir, "AGENTS.md")
	f, err := os.Create(wide)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(1<<20 + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	code, _, stderr = runCLI([]string{"paimos", "--config", filepath.Join(dir, "missing"), "harness", "provenance", "--project", "AEON", "--session", transcriptSessionID, "--agent", "worker", "--worker-lease-file", filepath.Join(dir, "missing-lease"), "--instruction", wide}, "")
	if code == 0 || !strings.Contains(stderr, "too large") || strings.Contains(stderr, dir) {
		t.Fatalf("oversized exit %d stderr %q", code, stderr)
	}
}

func TestHarnessProvenanceShowReads(t *testing.T) {
	isolate(t)
	var calls []transcriptRequest
	srv := transcriptFixture(t, "memory", "note", &calls)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	code, _, stderr := runCLI([]string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "harness", "provenance", "--project", "AEON", "--session", transcriptSessionID, "--show"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("show exit %d: %s", code, stderr)
	}
	found := false
	for _, call := range calls {
		if call.method == http.MethodGet && strings.HasSuffix(call.path, "/provenance") {
			found = true
		}
	}
	if !found {
		t.Fatal("show did not read provenance")
	}
}
