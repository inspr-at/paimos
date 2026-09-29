// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testKey = "aeon_prefix_secretvalue"

func isolate(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AEON_URL", "AEON_API_KEY", "AEON_API_KEY_FILE",
		"PAIMOS_URL", "PAIMOS_API_KEY", "PAIMOS_API_KEY_FILE",
		"CLAUDE_CODE_SESSION_ID", "CODEX_SESSION_ID", "CODEX_THREAD_ID",
	} {
		t.Setenv(k, "")
	}
}

func meServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"principal":{"id":"p","tenant_id":"t","kind":"agent","name":"cursor-grok","roles":["admin"]},"tenant":{"id":"t","slug":"aeon","name":"Aeon"},"identity":null}`))
	}))
}

func runCLI(args []string, stdin string) (int, string, string) {
	var out, err bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &err)
	return code, out.String(), err.String()
}

func assertNoSecret(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, testKey) || strings.Contains(text, "secretvalue") {
		t.Fatal("output contained the API key")
	}
}

func TestHelpAndProgramName(t *testing.T) {
	isolate(t)
	code, out, err := runCLI([]string{"aeon", "--help"}, "")
	if code != 0 || !strings.Contains(out, "aeon <command>") || err != "" {
		t.Fatalf("code %d out %q err %q", code, out, err)
	}
	code, out, err = runCLI([]string{"/usr/local/bin/paimos"}, "")
	if code != 2 || !strings.Contains(out, "paimos <command>") || !strings.Contains(out, "issue") {
		t.Fatalf("paimos help code %d out %q err %q", code, out, err)
	}
	code, out, _ = runCLI([]string{"aeon", "version"}, "")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("version code %d out %q", code, out)
	}
}

func TestAuthLoginDoesNotEchoKey(t *testing.T) {
	isolate(t)
	srv := meServer(t, testKey)
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	keyFile := filepath.Join(dir, "agent.key")
	if err := os.WriteFile(keyFile, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCLI([]string{"aeon", "auth", "login", "--url", srv.URL, "--name", "dev", "--key-file", keyFile, "--config", cfg}, "")
	assertNoSecret(t, out+"\n"+errOut)
	if code != 0 {
		t.Fatalf("login code %d err %s", code, errOut)
	}
	if !strings.Contains(out, "cursor-grok") || !strings.Contains(out, `default_instance = "dev"`) {
		t.Fatalf("login out %q", out)
	}
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testKey) || strings.Contains(string(raw), "api_key") {
		t.Fatal("config file stored the API key")
	}
	st, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %o", st.Mode().Perm())
	}
	keyRaw, err := os.ReadFile(filepath.Join(dir, "keys", "dev"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(keyRaw)) != testKey {
		t.Fatal("stored key mismatch")
	}
	kst, err := os.Stat(filepath.Join(dir, "keys", "dev"))
	if err != nil {
		t.Fatal(err)
	}
	if kst.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o", kst.Mode().Perm())
	}

	code, out, errOut = runCLI([]string{"aeon", "--config", cfg, "whoami"}, "")
	assertNoSecret(t, out+"\n"+errOut)
	if code != 0 || !strings.Contains(out, "principal: cursor-grok (agent)") || !strings.Contains(out, "tenant: Aeon (aeon)") {
		t.Fatalf("whoami code %d out %q err %q", code, out, errOut)
	}

	code, out, errOut = runCLI([]string{"aeon", "--config", cfg, "--json", "auth", "whoami"}, "")
	assertNoSecret(t, out+"\n"+errOut)
	if code != 0 {
		t.Fatalf("json whoami %d %s", code, errOut)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	if body["instance"] != "dev" || body["url"] != srv.URL {
		t.Fatalf("json whoami %+v", body)
	}
}

func TestAuthLoginStdin(t *testing.T) {
	isolate(t)
	srv := meServer(t, testKey)
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	code, out, errOut := runCLI([]string{"aeon", "auth", "login", "--url", srv.URL, "--config", cfg}, testKey+"\n")
	assertNoSecret(t, out+"\n"+errOut)
	if code != 0 {
		t.Fatalf("stdin login code %d err %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "keys", "default")); err != nil {
		t.Fatal(err)
	}
}

func TestAuthLoginRejectsKey(t *testing.T) {
	isolate(t)
	srv := meServer(t, testKey)
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	keyFile := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(keyFile, []byte("aeon_prefix_wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI([]string{"aeon", "auth", "login", "--url", srv.URL, "--key-file", keyFile, "--config", cfg}, "")
	if code != 1 || !strings.Contains(errOut, "unauthorized") {
		t.Fatalf("code %d out %q err %q", code, out, errOut)
	}
	if strings.Contains(out+errOut, "aeon_prefix_wrong") {
		t.Fatal("rejected key was echoed")
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatal("config written after a rejected key")
	}
}

func TestWhoamiEnv(t *testing.T) {
	isolate(t)
	srv := meServer(t, testKey)
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, errOut := runCLI([]string{"aeon", "whoami"}, "")
	assertNoSecret(t, out+"\n"+errOut)
	if code != 0 || !strings.Contains(out, "instance: env") {
		t.Fatalf("code %d out %q err %q", code, out, errOut)
	}

	isolate(t)
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	code, out, errOut = runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing.yaml"), "whoami"}, "")
	if code != 2 {
		t.Fatalf("aeon should ignore PAIMOS_URL, code %d out %q err %q", code, out, errOut)
	}
	code, out, errOut = runCLI([]string{"paimos", "auth", "whoami"}, "")
	assertNoSecret(t, out+"\n"+errOut)
	if code != 0 || !strings.Contains(out, "cursor-grok") {
		t.Fatalf("paimos env whoami code %d out %q err %q", code, out, errOut)
	}
}

func TestCompatibilityVerbs(t *testing.T) {
	isolate(t)
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"aeon", "issue", "get", "18"}, 2, "ambiguous bare issue number"},
		{[]string{"aeon", "issue", "create", "--title", "missing project"}, 2, "--project is required"},
		{[]string{"aeon", "issue", "create", "-p", "AEON", "--title", "CLI", "--dry-run"}, 0, ""},
		{[]string{"aeon", "issue", "update", "AEON-18"}, 2, "nothing to update"},
		{[]string{"aeon", "issue", "comment", "AEON-18"}, 2, "--body or --body-file is required"},
		{[]string{"aeon", "knowledge", "list"}, 2, "--project is required"},
		{[]string{"aeon", "knowledge", "create", "--type", "nope", "--slug", "n", "--project", "AEON", "--title", "N"}, 2, "unknown knowledge type"},
		{[]string{"aeon", "model", "resolve", "review-gate"}, 2, "review-gate requires --author-family"},
		{[]string{"aeon", "model", "resolve", "nope"}, 2, "unknown model role"},
		{[]string{"aeon", "onboard"}, 2, "--project is required"},
		{[]string{"aeon", "session", "start", "--agent", "worker"}, 2, "--project is required"},
		{[]string{"aeon", "issue", "list", "--nope"}, 2, "unknown flag"},
		{[]string{"aeon", "issue"}, 2, "requires a subcommand"},
	}
	for _, tc := range cases {
		code, out, errOut := runCLI(tc.args, "")
		if tc.code == 0 {
			if code != 0 || !strings.Contains(out, "dry-run:") {
				t.Errorf("%v code %d out %q err %q", tc.args, code, out, errOut)
			}
			continue
		}
		if code != tc.code || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v code %d out %q err %q, want %d %q", tc.args, code, out, errOut, tc.code, tc.want)
		}
	}

	code, _, errOut := runCLI([]string{"paimos", "--json", "run-agent", "watch"}, "")
	if code != 2 {
		t.Fatalf("json code %d err %s", code, errOut)
	}
	if !strings.Contains(errOut, "--project is required") {
		t.Fatalf("usage error %q", errOut)
	}
}

func TestMCPWhoamiAndStubs(t *testing.T) {
	isolate(t)
	srv := meServer(t, testKey)
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("default_instance: dev\ninstances:\n  dev:\n    url: "+srv.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "dev"), []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := &runtime{
		program:    "aeon",
		stdin:      strings.NewReader(""),
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		configPath: cfg,
	}
	server := rt.mcpServer()
	ctx := context.Background()
	left, right := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, left, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "dev"}, nil).Connect(ctx, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"whoami", "issue_list", "issue_get", "issue_create", "issue_update", "issue_comment", "knowledge_list", "knowledge_get", "knowledge_create", "knowledge_update", "search"} {
		if !names[name] {
			t.Errorf("missing tool %s", name)
		}
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("whoami tool error %#v", res)
	}
	text := toolText(res)
	if !strings.Contains(text, "cursor-grok") || strings.Contains(text, testKey) {
		t.Fatalf("whoami text %q", text)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "issue_list",
		Arguments: map[string]any{"project": "AEON"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(toolText(res), "issue_list arrives in R1") {
		t.Fatalf("issue_list %#v text %q", res.IsError, toolText(res))
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "nodes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(toolText(res), "arrives in R1") {
		t.Fatalf("search text %q", toolText(res))
	}
}

func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}
