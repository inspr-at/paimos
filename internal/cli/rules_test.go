// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rules"
)

func TestRulesRendererPreservesExactBytes(t *testing.T) {
	body := "# Aeon session rules\n\n- [safety] Preserve safety.\n"
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	for _, h := range rules.Harnesses {
		m := rules.Merged{Context: rules.Context{Harness: h}, Version: "260928120000.0.0", Body: body, ByteSize: len(body), SHA256: hash}
		r, err := renderRulesThroughHarness(m)
		if err != nil || r.Body != body || r.Rev != hash {
			t.Fatal("renderer changed exact bytes", err)
		}
		want := "AGENTS.md"
		if h == "claude-code" {
			want = "CLAUDE.md"
		}
		if r.SuggestedPath != want {
			t.Fatal("harness mismatch")
		}
	}
}

func TestRulesHarnessRenderGolden(t *testing.T) {
	root := filepath.Join("testdata", "rules-harness")
	body, err := os.ReadFile(filepath.Join(root, "body.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Harnesses map[string]struct {
			Path string `json:"path"`
			File string `json:"file"`
		} `json:"harnesses"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		t.Fatal(err)
	}
	if len(manifest.Harnesses) != len(rules.Harnesses) {
		t.Fatalf("golden harnesses %d, renderer %d", len(manifest.Harnesses), len(rules.Harnesses))
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	for _, harness := range rules.Harnesses {
		spec, ok := manifest.Harnesses[harness]
		if !ok || spec.Path == "" || spec.File == "" {
			t.Fatal("missing golden", harness)
		}
		golden, err := os.ReadFile(filepath.Join(root, spec.File))
		if err != nil {
			t.Fatal(err)
		}
		m := rules.Merged{Context: rules.Context{Harness: harness}, Version: "260929080000.0.0", Body: string(body), ByteSize: len(body), SHA256: hash}
		got, err := renderRulesThroughHarness(m)
		if err != nil || got.Body != string(golden) || got.SuggestedPath != spec.Path || got.Rev != hash {
			t.Fatalf("%s render %q %q: %v", harness, got.SuggestedPath, got.Body, err)
		}
	}
}
func TestRulesOfflineOnlyForNetworkFailure(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 422, 500} {
		if networkUnavailable(&client.StatusError{Status: code}) {
			t.Fatalf("authorization/semantic error %d enabled cache", code)
		}
	}
	for _, code := range []int{502, 503, 504} {
		if !networkUnavailable(&client.StatusError{Status: code}) {
			t.Fatal(code)
		}
	}
	if !networkUnavailable(&net.OpError{Op: "dial", Err: errors.New("offline")}) {
		t.Fatal("network failure not recognized")
	}
}

func TestRulesPreviewOnlineOfflineAndRefusedCache(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	c := rules.Context{TenantID: "10000000-0000-4000-8000-000000000001", ProjectID: "10000000-0000-4000-8000-000000000002", PersonID: "10000000-0000-4000-8000-000000000003", AgentID: "10000000-0000-4000-8000-000000000004", Role: "builder", Harness: "codex"}
	r := rules.Rule{Identity: "safety", Text: "Preserve safety.", Why: "Fixture", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "test"}}
	snap := rules.Snapshot{SetID: "10000000-0000-4000-8000-000000000005", Scope: rules.Scope{Layer: "company"}, Name: "Floor", Revision: 1, Version: "260928100000.0.0", Rules: []rules.Rule{r}}
	snap.SHA256 = rules.SnapshotDigest(snap)
	m, err := rules.Merge(c, []rules.Snapshot{snap}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	floor := filepath.Join(dir, "floor.txt")
	if err = rules.WriteFile(floor, []byte(m.Floor), false); err != nil {
		t.Fatal(err)
	}
	floorHash := sha256.Sum256([]byte(m.Floor))
	writes := make(chan string, 8)
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes <- r.Method + " " + r.URL.Path
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api/me" {
			json.NewEncoder(w).Encode(map[string]any{"principal": map[string]string{"id": c.AgentID, "name": "worker"}})
			return
		}
		if r.URL.Path != "/api/rules/merged" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		if r.Header.Get(rules.ClientMaximumHeader) != strconv.Itoa(rules.SessionFileLimit(c.Harness)) {
			t.Error("CLI did not report the harness read limit")
		}
		if r.URL.Query().Get("person_id") != c.PersonID || r.URL.Query().Get("agent_id") != c.AgentID {
			t.Error("lost request context")
		}
		json.NewEncoder(w).Encode(m)
	}))
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	args := []string{"paimos", "--config", filepath.Join(dir, "missing"), "session", "start", "--project", c.ProjectID, "--agent", "worker", "--rules-preview", "--rules-tenant", c.TenantID, "--rules-person", c.PersonID, "--rules-agent", c.AgentID, "--rules-role", c.Role, "--rules-harness", c.Harness, "--rules-floor", floor, "--rules-floor-sha256", hex.EncodeToString(floorHash[:]), "--rules-cache", filepath.Join(dir, "cache.json")}
	code, out, stderr := runCLI(args, "")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(out, `"stale":false`) || !strings.Contains(out, `"execution_verified":false`) || !strings.Contains(out, `"provenance_recorded":false`) || strings.Contains(out, `"provenance_items"`) {
		t.Fatal(out)
	}
	var preview struct {
		Proposed struct {
			BodySHA256 string `json:"body_sha256"`
			Version    string `json:"version"`
			ByteSize   int    `json:"byte_size"`
		} `json:"proposed_received_payload"`
	}
	if err := json.Unmarshal([]byte(out), &preview); err != nil || preview.Proposed.BodySHA256 != m.SHA256 || preview.Proposed.Version != m.Version || preview.Proposed.ByteSize != len(m.Body) {
		t.Fatal("proposed received payload differs from preview bytes", err, out)
	}
	assertNoSecret(t, out+stderr)

	select {
	case write := <-writes:
		t.Fatal("preview wrote to server:", write)
	default:
	}
	for _, flag := range []string{"--rules-record-received", "--rules-worker-lease-file"} {
		obsolete := append(append([]string{}, args...), flag, "unused")
		code, out, stderr = runCLI(obsolete, "")
		if code != 2 || !strings.Contains(stderr, "unknown flag "+flag) || out != "" {
			t.Fatal("obsolete flag was accepted", flag, code, stderr, out)
		}
	}
	code, out, stderr = runCLI([]string{"paimos", "session", "start", "--help"}, "")
	if code != 0 || strings.Contains(out, "rules-record-received") || strings.Contains(out, "rules-worker-lease-file") {
		t.Fatal("obsolete flag remains in help", code, stderr, out)
	}
	status = 403
	code, _, _ = runCLI(args, "")
	if code == 0 {
		t.Fatal("403 used cached authorization")
	}
	cachePath := filepath.Join(dir, "cache.json")
	fresh, err := rules.ReadFile(cachePath, rules.MaxCacheBytes)
	if err != nil || strings.Contains(string(fresh), `"stale"`) {
		t.Fatal("authorization failure marked the cache stale", err)
	}
	status = 503
	code, out, stderr = runCLI(args, "")
	if code != 0 || !strings.Contains(out, `"stale":true`) || !strings.Contains(out, "Preserve safety") {
		t.Fatal("offline cache", stderr, out)
	}
	staleRaw, err := rules.ReadFile(cachePath, rules.MaxCacheBytes)
	var marked rules.Cache
	if err != nil || json.Unmarshal(staleRaw, &marked) != nil || !marked.Stale || marked.Bundle.SHA256 != m.SHA256 {
		t.Fatal("cache not marked stale", err)
	}
	if _, err = rules.DecodeCache(staleRaw, srv.URL, c, time.Now()); err != nil {
		t.Fatal("marked cache failed integrity", err)
	}
	status = 200
	code, out, stderr = runCLI(args, "")
	if code != 0 || !strings.Contains(out, `"stale":false`) {
		t.Fatal("online refresh", stderr, out)
	}
	fresh, err = rules.ReadFile(cachePath, rules.MaxCacheBytes)
	if err != nil || strings.Contains(string(fresh), `"stale"`) {
		t.Fatal("online fetch left the cache stale", err)
	}
	if err = os.WriteFile(cachePath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	status = 503
	code, out, stderr = runCLI(args, "")
	if code != 0 || !strings.Contains(out, "floor-only") || !strings.Contains(out, "Preserve safety") {
		t.Fatal("floor lost", stderr, out)
	}
	left, err := os.ReadFile(cachePath)
	if err != nil || string(left) != "corrupt" {
		t.Fatal("unusable cache was rewritten", err, string(left))
	}

	select {
	case write := <-writes:
		t.Fatal("preview or obsolete flag wrote to server:", write)
	default:
	}
}
