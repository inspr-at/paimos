// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
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
	t.Chdir(home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	const identity = "inspr-at/fixture-doctrine/docs/AGENTS-KERNEL.md#secrets"
	const text = "Never print the environment."
	doctrine := `{"sources":[{"state":"ready","repository":"inspr-at/fixture-doctrine","ref":"v260922101217.0.0","commit":"` + strings.Repeat("ab", 20) + `","files":[{"rules":[{"identity":"` + identity + `","key":"no-env-dump","text":"` + text + `"}]}]}]}`
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
	sessionPath := filepath.Join(home, "received session.txt")
	hookBytes, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]string{"type": "command", "command": "aeon session start --rules-out " + shellHookQuote(sessionPath) + rulesHookMarker}}}}}})
	hook := string(hookBytes)
	cleanSession := "# Aeon session rules\n\n- [local] Project style.\n"
	if err := os.WriteFile(sessionPath, []byte(cleanSession), 0o600); err != nil {
		t.Fatal(err)
	}
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
	cleanDoctrine := doctrine
	for _, mode := range []string{"rewrapped", "nested imports", "missing import", "cyclic import", "unsafe import", "import drift", "empty harness", "removed marker", "failed source", "unindexed source", "missing state", "ready with error", "file index error", "missing files", "changed pin", "empty response", "session duplicate", "empty session", "missing session", "unreadable session", "inbox only", "explicit session", "codex session"} {
		t.Run(mode, func(t *testing.T) {
			doctrine = cleanDoctrine
			mustWrite := func(path, text string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			mustWrite(filepath.Join(home, ".claude", "CLAUDE.md"), matched)
			mustWrite(filepath.Join(home, ".claude", "settings.json"), hook)
			mustWrite(sessionPath, cleanSession)
			args := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "doctor"}
			want, phrase := 2, ""
			switch mode {
			case "rewrapped":
				mustWrite(filepath.Join(home, ".claude", "CLAUDE.md"), "- Never print\n  the\t environment.\n")
				want, phrase = 0, "no rule served twice"
			case "nested imports", "missing import", "cyclic import", "unsafe import", "import drift":
				mustWrite(filepath.Join(home, ".claude", "CLAUDE.md"), "# Harness\n@wrapper.md\n")
				mustWrite(filepath.Join(home, ".claude", "wrapper.md"), "See @../kernel.md\n")
				mustWrite(filepath.Join(home, "kernel.md"), matched)
				want, phrase = 0, "no rule served twice"
				switch mode {
				case "missing import":
					mustWrite(filepath.Join(home, "kernel.md"), "@missing.md\n")
					want, phrase = 1, "unverified"
				case "cyclic import":
					mustWrite(filepath.Join(home, "kernel.md"), "@.claude/CLAUDE.md\n")
					want, phrase = 1, "unverified"
				case "unsafe import":
					mustWrite(filepath.Join(home, "kernel.md"), "@.env\n")
					want, phrase = 1, "unverified"
				case "import drift":
					mustWrite(filepath.Join(home, "kernel.md"), drifted)
					want, phrase = 2, "harness file drifted"
				}
			case "empty harness":
				mustWrite(filepath.Join(home, ".claude", "CLAUDE.md"), "")
			case "removed marker":
				mustWrite(filepath.Join(home, ".claude", "CLAUDE.md"), "# Kernel\n")
			case "failed source":
				doctrine = strings.Replace(doctrine, `"state":"ready"`, `"state":"failed"`, 1)
			case "unindexed source":
				doctrine = strings.Replace(doctrine, `"state":"ready"`, `"state":"not_indexed"`, 1)
			case "missing state":
				doctrine = strings.Replace(doctrine, `"state":"ready",`, "", 1)
			case "ready with error":
				doctrine = strings.Replace(doctrine, `"state":"ready"`, `"state":"ready","error":"index failed"`, 1)
			case "file index error":
				doctrine = strings.Replace(doctrine, `"files":[{`, `"files":[{"problem":"not UTF-8 text",`, 1)
			case "missing files":
				doctrine = `{"sources":[{"state":"ready","repository":"inspr-at/fixture-doctrine","commit":"` + strings.Repeat("ab", 20) + `"}]}`
			case "changed pin":
				doctrine = strings.Replace(doctrine, strings.Repeat("ab", 20), strings.Repeat("cd", 20), 1)
			case "empty response":
				doctrine = `{}`
			case "session duplicate":
				mustWrite(sessionPath, cleanSession+"- [copy] "+text+"\n")
				phrase = "rule served twice"
			case "empty session":
				mustWrite(sessionPath, "")
			case "missing session":
				args = append(args, "--rules-harness", "claude", "--rules-out", filepath.Join(home, "missing.txt"))
				want = 1
			case "unreadable session":
				dir := filepath.Join(t.TempDir(), "directory.txt")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--rules-harness", "claude", "--rules-out", dir)
				want = 1
			case "inbox only", "explicit session", "codex session":
				mustWrite(filepath.Join(home, ".claude", "settings.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"aeon hook claude Stop`+inboxHookMarker+`"}]}]}}`)
				want, phrase = 0, "rules session hook not installed"
				if mode != "inbox only" {
					harness := "claude"
					if mode == "codex session" {
						harness = "codex"
						if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
							t.Fatal(err)
						}
						mustWrite(filepath.Join(home, ".codex", "AGENTS.md"), matched)
					}
					args = append(args, "--rules-harness", harness, "--rules-out", sessionPath)
					mustWrite(sessionPath, cleanSession+"- [copy] "+text+"\n")
					want, phrase = 2, "rule served twice"
				}
			}
			code, out, errOut := runCLI(args, "")
			if code != want || !strings.Contains(out, phrase) || (want != 0 && strings.Contains(out, "no rule served twice")) || (phrase == "unverified" && strings.Contains(out, "drifted")) {
				t.Fatalf("%s: exit %d want %d\n%s\n%s", mode, code, want, out, errOut)
			}
		})
	}
}

func TestDoctorRulesHookOutputs(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	write := func(path, command string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]string{"type": "command", "command": command}}}}}})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	userPath := hookSettingsPath(home, "claude")
	write(userPath, "aeon hook claude Stop"+inboxHookMarker)
	if outputs, err := rulesSessionOutputs(home, doctorRulesOptions{}); err != nil || len(outputs) != 0 {
		t.Fatalf("inbox treated as rules: %v %v", outputs, err)
	}
	write(userPath, "aeon session start --rules-out 'user file.txt'"+rulesHookMarker)
	write(filepath.Join(home, ".claude", "settings.local.json"), "aeon session start --rules-out=project.txt"+rulesHookMarker)
	outputs, err := rulesSessionOutputs(home, doctorRulesOptions{})
	if err != nil || len(outputs) != 2 || outputs[0].Path != filepath.Join(home, "user file.txt") || outputs[1].Path != filepath.Join(home, "project.txt") {
		t.Fatalf("outputs %v %v", outputs, err)
	}
	for _, command := range []string{"aeon session start", "aeon session start --rules-out '$TARGET'", "aeon session start --rules-out file.txt; touch unexpected", "aeon session start --rules-out file.env", "aeon session start --rules-out ~/file.txt", "aeon session start --rules-out *.txt", "aeon session start # --rules-out ignored.txt", "aeon session start --rules-out 'unterminated"} {
		write(userPath, command+rulesHookMarker)
		if _, err := rulesSessionOutputs(home, doctorRulesOptions{}); err == nil {
			t.Fatalf("accepted unverified hook %q", command)
		}
	}
	outputs, err = rulesSessionOutputs(home, doctorRulesOptions{Harness: "codex", Out: "manual.txt"})
	if err != nil || len(outputs) != 1 || outputs[0].Harness != "codex" || outputs[0].Path != filepath.Join(home, "manual.txt") {
		t.Fatalf("explicit output %v %v", outputs, err)
	}
}

func TestDoctorHarnessImportsAreBounded(t *testing.T) {
	for _, mode := range []string{"symlink", "outside home", "secret directory", "large file", "total bytes", "depth", "file count", "quoted and home imports", "code examples"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "CLAUDE.md")
			write := func(path, text string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			text, wantVerified := "@import.md\n", false
			switch mode {
			case "symlink":
				write(filepath.Join(home, "target.md"), "Untrusted fixture text.")
				if err := os.Symlink("target.md", filepath.Join(home, "import.md")); err != nil {
					t.Fatal(err)
				}
			case "outside home":
				outside := filepath.Join(t.TempDir(), "outside.md")
				write(outside, "Untrusted fixture text.")
				text = "@" + outside
			case "secret directory":
				write(filepath.Join(home, ".ssh", "fixture.md"), "Untrusted fixture text.")
				text = "@.ssh/fixture.md"
			case "large file":
				write(filepath.Join(home, "import.md"), strings.Repeat("x", 256*1024+1))
			case "depth", "file count", "total bytes":
				text = ""
				count, size := 9, 0
				if mode == "file count" {
					count = 64
				} else if mode == "total bytes" {
					count, size = 5, 240*1024
				}
				for i := 0; i < count; i++ {
					name := fmt.Sprintf("part-%d.md", i)
					content := strings.Repeat("x", size)
					if mode == "depth" && i+1 < count {
						content = fmt.Sprintf("@part-%d.md\n", i+1)
					}
					write(filepath.Join(home, name), content)
					if mode != "depth" || i == 0 {
						text += "@" + name + "\n"
					}
				}
			case "quoted and home imports":
				text, wantVerified = "@\"my file.md\"\n@~/other.md\n", true
				write(filepath.Join(home, "my file.md"), "First rule.")
				write(filepath.Join(home, "other.md"), "Second rule.")
			case "code examples":
				text, wantVerified = "`@missing.md`\n```md\n@missing.md\n```\n", true
			}
			write(path, text)
			file := readHarnessImports(home, "claude", path)
			if file.Missing || file.Unverified == wantVerified || strings.Contains(file.Text, "Untrusted fixture text.") {
				t.Fatalf("verification=%v missing=%v", !file.Unverified, file.Missing)
			}
			if mode == "quoted and home imports" && (!strings.Contains(file.Text, "First rule.") || !strings.Contains(file.Text, "Second rule.")) {
				t.Fatal("literal imports were not read")
			}
		})
	}
}
