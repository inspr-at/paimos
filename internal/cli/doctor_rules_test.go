// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsOneChannel(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	const identity = "inspr-at/fixture-doctrine/docs/AGENTS-KERNEL.md#secrets"
	const text = "Never print the environment."
	doctrine := `{"sources":[{"repository":"inspr-at/fixture-doctrine","ref":"v260922101217.0.0","commit":"` + strings.Repeat("ab", 20) + `","files":[{"rules":[{"identity":"` + identity + `","key":"no-env-dump","text":"` + text + `"}]}]}]}`
	channels := `{"releases":[{"repository":"inspr-at/fixture-doctrine","ref":"v260922101217.0.0","commit":"` + strings.Repeat("ab", 20) + `"}],"duplicates":[]}`
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"status":"ok","db":"ok"}`))
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"260925100000.0.0","scheme":"inspr-calendar-v2","brand":{"wordmark":"PAIMOS AEON","product":"PAIMOS"}}`))
		case "/api/me":
			_, _ = w.Write([]byte(`{"principal":{"name":"worker"}}`))
		case "/api/kinds":
			_, _ = w.Write([]byte(`{"items":[]}`))
		case "/api/rules/doctrine":
			_, _ = w.Write([]byte(doctrine))
		case "/api/rules/channels":
			_, _ = w.Write([]byte(channels))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	hook := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"aeon hook claude Stop` + inboxHookMarker + `"}]}]}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(hook), 0o600); err != nil {
		t.Fatal(err)
	}
	matched := "# Kernel\n\n## Secrets\n\n<!-- aeon-rule: no-env-dump -->\n- Never print the environment.\n"
	if err := os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte(matched), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "doctor"}, "")
	if code != 0 || errOut != "" || !strings.Contains(out, "no rule served twice") {
		t.Fatalf("clean exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(strings.Join(paths, ","), "/api/rules/doctrine") || !strings.Contains(strings.Join(paths, ","), "/api/rules/channels") {
		t.Fatalf("doctor did not compare channels: %v", paths)
	}

	drifted := "# Kernel\n\n## Secrets\n\n<!-- aeon-rule: no-env-dump -->\n- Sometimes print the environment.\n"
	if err := os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte(drifted), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "doctor"}, "")
	if code != 2 || !strings.Contains(out, "harness file drifted") || !strings.Contains(out, identity) || strings.Contains(out, "Sometimes print") {
		t.Fatalf("drift exit %d\n%s\n%s", code, out, errOut)
	}
}
