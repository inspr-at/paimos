// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_rulesimport_unsupported

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const compareProse = "SYNTHETIC_RULE_PROSE_251"
const comparePerson = "10000000-0000-4000-8000-000000000003"
const compareProject = "10000000-0000-4000-8000-000000000002"

func compareFixture(t *testing.T) (home, repo string) {
	t.Helper()
	home = t.TempDir()
	repo = filepath.Join(t.TempDir(), "fixture")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(home, ".codex", "credentials.json")
	if err := os.WriteFile(secret, []byte("SYNTHETIC_SECRET_251"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o600) })
	body := "# Synthetic\n\n## Safety\n\n<!-- aeon-rule: safety -->\n- " + compareProse + "\n  Why: it stays local.\n"
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(repo), "AGENTS.md")
	if err := os.WriteFile(outside, []byte("SYNTHETIC_OUTSIDE_251"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, repo
}

func compareServer(t *testing.T, kind string, mergedStatus int, posts *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/me":
			_, _ = w.Write([]byte(`{"principal":{"id":"` + comparePerson + `","tenant_id":"10000000-0000-4000-8000-000000000001","kind":"` + kind + `","name":"fixture"},"tenant":{"id":"10000000-0000-4000-8000-000000000001","slug":"fixture","name":"Fixture"},"identity":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/rules/merged":
			if mergedStatus != http.StatusOK {
				w.WriteHeader(mergedStatus)
				_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"context":{"tenant_id":"10000000-0000-4000-8000-000000000001","project_id":"` + compareProject + `","person_id":"` + comparePerson + `","role":"builder","harness":"codex"},"versions":[],"version":"260929120000.0.0","sha256":"` + strings.Repeat("ab", 32) + `","body":"` + compareProse + `","byte_size":1,"rules":[{"identity":"safety","text":"` + compareProse + `","why":"published","strength":"normal","enabled":true,"source":{"reference":"fixture","edited_here":false}},{"identity":"merged-only","text":"other","why":"published","strength":"normal","enabled":true,"source":{"reference":"fixture","edited_here":false}}],"floor":"","valid_until":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/rules/comparisons":
			raw, _ := readAll(r)
			*posts = append(*posts, string(raw))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

func TestRulesCompareLiveReportsHashesOnly(t *testing.T) {
	isolate(t)
	home, repo := compareFixture(t)
	var posts []string
	srv := compareServer(t, "person", http.StatusOK, &posts)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	outPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(outPath, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "rules", "compare", "--harness", "codex", "--repo", repo, "--project", compareProject, "--role", "builder", "--home", home}
	code, out, stderr := runCLI(append(base, "--out", outPath, "--upload"), "")
	kept, err := os.ReadFile(outPath)
	if err != nil || string(kept) != "keep\n" || code == 0 {
		t.Fatalf("overwrite code %d err %s file %q", code, stderr, kept)
	}
	fresh := filepath.Join(filepath.Dir(outPath), "fresh.json")
	code, out, stderr = runCLI(append(base, "--out", fresh, "--upload"), "")
	assertNoSecret(t, out+"\n"+stderr)
	if code != 0 {
		t.Fatal(stderr)
	}
	if strings.Contains(out, compareProse) || strings.Contains(out, "SYNTHETIC_SECRET_251") || strings.Contains(out, "SYNTHETIC_OUTSIDE_251") || strings.Contains(out, repo) || strings.Contains(out, home) {
		t.Fatal(out)
	}
	var report map[string]any
	if err = json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report["schema"] != "aeon.rules-comparison.v1" || report["harness"] != "codex" {
		t.Fatalf("%v", report["schema"])
	}
	raw, err := os.ReadFile(fresh)
	if err != nil || strings.Contains(string(raw), compareProse) || strings.Contains(string(raw), repo) {
		t.Fatalf("out file leaked %v", err)
	}
	if len(posts) != 1 || strings.Contains(posts[0], compareProse) || strings.Contains(posts[0], "SYNTHETIC") {
		t.Fatalf("upload %v", posts)
	}
	var uploaded map[string]json.RawMessage
	if err = json.Unmarshal([]byte(posts[0]), &uploaded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"text", "body", "why", "details", "path", "content", "files", "summary"} {
		if _, ok := uploaded[key]; ok {
			t.Fatal("uploaded " + key)
		}
	}
}

func TestRulesCompareUploadsOmittedImport(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	home := t.TempDir()
	repo := filepath.Join(base, "fixture")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "outside.md"), []byte("SYNTHETIC_OUTSIDE_251"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "# Synthetic\n\n## Safety\n\n<!-- aeon-rule: safety -->\n- " + compareProse + "\n  Why: published\n"
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("@AGENTS.md\n@../outside.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts []string
	srv := compareServer(t, "person", http.StatusOK, &posts)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	code, out, stderr := runCLI([]string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "--json", "rules", "compare", "--harness", "claude", "--repo", repo, "--project", compareProject, "--role", "builder", "--home", home, "--upload"}, "")
	if code != 0 {
		t.Fatal(stderr)
	}
	if strings.Contains(out, compareProse) || strings.Contains(out, "SYNTHETIC_OUTSIDE_251") || strings.Contains(out, repo) || strings.Contains(out, home) {
		t.Fatal("report leaked instruction text or a path")
	}
	if !strings.Contains(out, "imports_not_followed") || !strings.Contains(out, `"status":"both"`) {
		t.Fatal(out)
	}
	if len(posts) != 1 || !strings.Contains(posts[0], "imports_not_followed") || strings.Contains(posts[0], compareProse) || strings.Contains(posts[0], "SYNTHETIC_OUTSIDE_251") || strings.Contains(posts[0], repo) {
		t.Fatalf("upload %v", posts)
	}
}

func TestRulesCompareLiveRefusesBeforeReadAndUpload(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "")
	code, out, stderr := runCLI([]string{"paimos", "rules", "compare", "--harness", "grok", "--repo", t.TempDir(), "--project", compareProject, "--role", "builder"}, "")
	if code != 2 || !strings.Contains(stderr, "harness") || strings.Contains(stderr, "home directory") || out != "" {
		t.Fatalf("code %d out %q err %q", code, out, stderr)
	}
	home, repo := compareFixture(t)
	var posts []string
	srv := compareServer(t, "person", http.StatusInternalServerError, &posts)
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	code, out, stderr = runCLI([]string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "rules", "compare", "--harness", "claude", "--repo", repo, "--project", compareProject, "--role", "builder", "--home", home, "--upload"}, "")
	if code == 0 || len(posts) != 0 || strings.Contains(out+stderr, compareProse) {
		t.Fatalf("code %d posts %d out %q err %q", code, len(posts), out, stderr)
	}
	agent := compareServer(t, "agent", http.StatusOK, &posts)
	defer agent.Close()
	t.Setenv("PAIMOS_URL", agent.URL)
	code, _, stderr = runCLI([]string{"paimos", "--config", filepath.Join(t.TempDir(), "missing"), "rules", "compare", "--harness", "codex", "--repo", repo, "--project", compareProject, "--role", "builder", "--home", home}, "")
	if code != 2 || !strings.Contains(stderr, "--person") {
		t.Fatalf("code %d err %q", code, stderr)
	}
}
