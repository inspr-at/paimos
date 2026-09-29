// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

func TestRedactProbeLineKeepsCauseAndDropsSecrets(t *testing.T) {
	home := "/Users/ada"
	secret := "sk-" + strings.Repeat("a", 24)
	token := strings.Repeat("b", 40)
	generic := genericProbeDetail("")
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "deleted folder", line: "The current working directory was deleted", want: "The current working directory was deleted"},
		{name: "home path", line: "The current working directory was deleted: " + filepath.Join(home, "Code", "old"), want: "The current working directory was deleted: ~/Code/old"},
		{name: "other home on deleted folder", line: "The current working directory was deleted: /home/other/proj", want: "The current working directory was deleted: ~/proj"},
		{name: "longer home prefix on deleted folder", line: "The current working directory was deleted: /Users/adaburg", want: "The current working directory was deleted: ~"},
		{name: "command not found", line: "zsh: command not found: claude", want: "zsh: command not found: claude"},
		{name: "permission denied", line: "bash: claude: Permission denied", want: "bash: claude: Permission denied"},
		{name: "missing module", line: "Error: Cannot find module '@scope/left-pad'", want: "cannot find module @scope/left-pad"},
		{name: "home only", line: "missing " + home, want: generic},
		{name: "other home", line: "missing /home/other/proj", want: generic},
		{name: "longer prefix", line: "missing /Users/adaburg", want: generic},
		{name: "token same line", line: "The current working directory was deleted " + secret, want: generic},
		{name: "assignment", line: "failed token=" + token, want: generic},
		{name: "bearer", line: "Authorization: Bearer " + token, want: generic},
		{name: "url user", line: "dial https://user:" + token + "@example.test/path", want: generic},
		{name: "json token", line: `startup failed {"access_token":"demoCredential_1234567890"}`, want: generic},
		{name: "quoted password", line: `startup failed password="synthetic words remain"`, want: generic},
		{name: "email", line: "startup failed for review-fixture@example.test", want: generic},
		{name: "json token spaced", line: `startup failed {"access_token": "demoCredential_1234567890"}`, want: generic},
		{name: "single quoted password", line: "startup failed password='synthetic words remain'", want: generic},
		{name: "absolute path", line: "missing /opt/customer-a/project/config.json", want: generic},
		{name: "path permission", line: "/opt/customer-a/project/config.json: permission denied", want: generic},
		{name: "module path", line: "Error: Cannot find module '/opt/customer-a/project/config.json'", want: generic},
		{name: "shaped command", line: "command not found: sk-" + strings.Repeat("a", 12), want: generic},
		{name: "second line ignored by caller", line: "The current working directory was deleted", want: "The current working directory was deleted"},
	}
	hidden := []string{home, "/Users/", "/home/", secret, token, "demoCredential_1234567890", "words remain", "review-fixture@example.test", "/opt/customer-a/project/config.json"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactProbeLine(tc.line, []string{home})
			if got != tc.want {
				t.Fatalf("got %q", got)
			}
			if tc.want == generic {
				for _, item := range hidden {
					if strings.Contains(got, item) {
						t.Fatalf("leaked %q in %q", item, got)
					}
				}
			}
		})
	}
	long := "The current working directory was deleted " + strings.Repeat("word ", 40)
	capped := redactProbeLine(long, nil)
	if capped != generic || strings.Contains(capped, "word") || len([]rune(capped)) > probeDetailLimit {
		t.Fatalf("unsafe tail kept %q", capped)
	}
	if redactProbeLine("-----BEGIN PRIVATE KEY-----\nabc", nil) != "" {
		t.Fatal("key block kept")
	}
	if redactProbeLine("bad\x00line", nil) != "" {
		t.Fatal("control line kept")
	}
	if got := capturedProbeLine([]byte("first\nsecond token="+token), false); got != "first" {
		t.Fatalf("first line %q", got)
	}
	if capturedProbeLine([]byte(strings.Repeat("a", 32)), true) != "" {
		t.Fatal("truncated line kept")
	}
}

func TestDiscoveryProbeDirectoryAndRedactedStartCause(t *testing.T) {
	home := physicalTemp(t)
	workspace := physicalTemp(t)
	path := filepath.Join(home, "claude")
	if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	secret := "sk-" + strings.Repeat("c", 20)
	leaked := filepath.Join(home, "trashed", "worktree")
	var dirs []string
	d := Discovery{Home: home, Workspace: workspace, LookPath: func(string) (string, error) { return path, nil }, Executor: executorFunc(func(_ context.Context, c Command) ([]byte, error) {
		dirs = append(dirs, c.Dir)
		if strings.Join(c.Args, " ") != "--version" {
			t.Fatalf("unexpected %v", c.Args)
		}
		return nil, &CommandError{ExitCode: 1, stderr: "The current working directory was deleted: " + leaked + "\n" + secret}
	})}
	_, err := d.Detect(t.Context(), "claude", "")
	if !errors.Is(err, harnesslaunch.ErrStart) || !strings.Contains(err.Error(), "the launcher must also work with the service PATH") || !strings.Contains(err.Error(), "The current working directory was deleted: ~/trashed/worktree") {
		t.Fatal(err)
	}
	if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sk-") {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != workspace {
		t.Fatalf("dirs %v", dirs)
	}
	d.Workspace = filepath.Join(workspace, "missing")
	dirs = nil
	_, err = d.Detect(t.Context(), "claude", "")
	if len(dirs) != 1 || dirs[0] != home {
		t.Fatalf("fallback %v (%v)", dirs, err)
	}
	d.Workspace = workspace
	dirs = nil
	d.Executor = executorFunc(func(_ context.Context, c Command) ([]byte, error) {
		dirs = append(dirs, c.Dir)
		switch strings.Join(c.Args, " ") {
		case "--version":
			return []byte("1.2.3"), nil
		case "auth status --json":
			if c.Dir != workspace {
				t.Fatalf("login dir %s", c.Dir)
			}
			return nil, &CommandError{ExitCode: 127, stderr: "loader failed " + secret}
		default:
			t.Fatalf("unexpected %v", c.Args)
		}
		return nil, errors.New("unexpected")
	})
	_, err = d.Detect(t.Context(), "claude", "")
	if !errors.Is(err, harnesslaunch.ErrStart) || !strings.Contains(err.Error(), genericProbeDetail("")) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sk-") || strings.Contains(err.Error(), "loader failed") {
		t.Fatal(err)
	}
	if len(dirs) != 2 || dirs[0] != workspace || dirs[1] != workspace {
		t.Fatalf("login dirs %v", dirs)
	}
	d.Executor = executorFunc(func(_ context.Context, c Command) ([]byte, error) {
		if strings.Join(c.Args, " ") == "--version" {
			return []byte("1.2.3"), nil
		}
		return nil, &CommandError{ExitCode: 1, stderr: "not signed in " + secret}
	})
	_, err = d.Detect(t.Context(), "claude", "")
	if err == nil || errors.Is(err, harnesslaunch.ErrStart) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "not signed in") {
		t.Fatal(err)
	}
}

func TestOSExecutorUsesProbeDirAndHidesStderrFromErrorText(t *testing.T) {
	home := physicalTemp(t)
	workspace := physicalTemp(t)
	marker := filepath.Join(workspace, "cwd")
	script := filepath.Join(home, "probe")
	secret := "stdout-" + strings.Repeat("d", 24)
	body := "#!/bin/sh\n/bin/pwd -P > \"$1\"\necho " + quoteShell(secret) + "\necho 'The current working directory was deleted' >&2\necho 'token=" + strings.Repeat("e", 32) + "' >&2\nexit 2\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := (OSExecutor{}).Run(t.Context(), Command{Path: script, Args: []string{marker}, Dir: workspace})
	var exit *CommandError
	if !errors.As(err, &exit) || exit.ExitCode != 2 || exit.Error() != "local command unavailable" {
		t.Fatal(err)
	}
	if strings.Contains(exit.Error(), "deleted") || strings.Contains(exit.Error(), secret) || strings.Contains(exit.Error(), "token") {
		t.Fatal(exit)
	}
	if got := probeFailureDetail(err, home); got != "The current working directory was deleted" || strings.Contains(exit.stderr, secret) {
		t.Fatalf("detail %q stderr %q", got, exit.stderr)
	}
	raw, readErr := os.ReadFile(marker)
	if readErr != nil || strings.TrimSpace(string(raw)) != workspace {
		t.Fatalf("pwd %q %v", raw, readErr)
	}
	if _, err = (OSExecutor{}).Run(t.Context(), Command{Path: script, Dir: "relative"}); err == nil || err.Error() != "command directory must be absolute" {
		t.Fatal(err)
	}
	if _, err = (OSExecutor{}).Run(t.Context(), Command{Path: script, Dir: filepath.Join(workspace, "missing")}); err == nil || err.Error() != "command directory unavailable" {
		t.Fatal(err)
	}
}

func TestVersionProbeSurvivesDeletedProcessDirectory(t *testing.T) {
	home := physicalTemp(t)
	workspace := physicalTemp(t)
	for _, dir := range []string{workspace, home} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			probe := dir
			scriptPath := filepath.Join(home, "claude-"+filepath.Base(dir))
			body := "#!/bin/sh\ngot=$(/bin/pwd -P) || { echo 'The current working directory was deleted' >&2; exit 1; }\nif [ \"$1\" = \"--version\" ]; then\n  if [ \"$got\" != " + quoteShell(probe) + " ]; then echo 'The current working directory was deleted' >&2; exit 1; fi\n  echo 1.2.3\n  exit 0\nfi\nif [ \"$1\" = \"auth\" ]; then\n  echo '{\"loggedIn\":true,\"email\":\"agent@example.test\",\"authMethod\":\"oauth\"}'\n  exit 0\nfi\nexit 1\n"
			if err := os.WriteFile(scriptPath, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			gone := filepath.Join(home, "gone-"+filepath.Base(dir))
			if err := os.Mkdir(gone, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(gone)
			if err := os.Remove(gone); err != nil {
				t.Fatal(err)
			}
			workspaceArg := workspace
			if dir == home {
				workspaceArg = filepath.Join(workspace, "missing")
			}
			d := Discovery{Home: home, Workspace: workspaceArg, LookPath: func(string) (string, error) { return scriptPath, nil }}
			c, err := d.Detect(t.Context(), "claude", "")
			if err != nil || c.Version != "1.2.3" || c.Login != "signed_in" || c.Label != "agent@example.test" {
				t.Fatal(err)
			}
		})
	}
}

func TestProbeDetailDropsGateLeaks(t *testing.T) {
	cases := []struct {
		name, line, sensitive string
	}{
		{name: "json_token", line: `startup failed {"access_token":"demoCredential_1234567890"}`, sensitive: "demoCredential_1234567890"},
		{name: "quoted_password", line: `startup failed password="synthetic words remain"`, sensitive: "words remain"},
		{name: "email", line: "startup failed for review-fixture@example.test", sensitive: "review-fixture@example.test"},
		{name: "absolute_path", line: "missing /opt/customer-a/project/config.json", sensitive: "/opt/customer-a/project/config.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, workspace := physicalTemp(t), physicalTemp(t)
			path := filepath.Join(home, "claude")
			body := "#!/bin/sh\nprintf '%s\\n' " + quoteShell(tc.line) + " >&2\nexit 1\n"
			if err := os.WriteFile(path, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			d := Discovery{Home: home, Workspace: workspace, LookPath: func(string) (string, error) { return path, nil }}
			_, err := d.Detect(t.Context(), "claude", "")
			if !errors.Is(err, harnesslaunch.ErrStart) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), tc.sensitive) || !strings.Contains(err.Error(), genericProbeDetail("claude")) {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeNodeCheckUsesHomeWhenWorkspaceMissing(t *testing.T) {
	d, path, node := npmFixture(t, "codex")
	t.Setenv("HOME", d.Home)
	body := "#!/bin/sh\ngot=$(/bin/pwd -P) || exit 126\n[ \"$got\" = " + quoteShell(d.Home) + " ] || exit 126\necho v22.19.0\n"
	if err := os.WriteFile(node.Path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if got := commandDir(d.Workspace); got != d.Workspace {
		t.Fatalf("workspace %q", got)
	}
	missing := filepath.Join(d.Workspace, "missing")
	if got := commandDir(missing); got != d.Home {
		t.Fatalf("fallback %q", got)
	}
	c := RuntimeConfig{Workspace: missing, Accounts: []RuntimeAccount{{Harness: "codex", Path: path, Node: node}}}
	if err := ValidateHarnessRuntimeDependencies(c); err != nil {
		t.Fatal(err)
	}
}

func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
