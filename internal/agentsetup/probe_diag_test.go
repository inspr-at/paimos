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
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "deleted folder", line: "The current working directory was deleted", want: "The current working directory was deleted"},
		{name: "home path", line: "The current working directory was deleted: " + filepath.Join(home, "Code", "old"), want: "The current working directory was deleted: ~/Code/old"},
		{name: "home only", line: "missing " + home, want: "missing ~"},
		{name: "other home", line: "missing /home/other/proj", want: "missing ~/proj"},
		{name: "longer prefix", line: "missing /Users/adaburg", want: "missing ~"},
		{name: "token same line", line: "The current working directory was deleted " + secret, want: "The current working directory was deleted [redacted]"},
		{name: "assignment", line: "failed token=" + token, want: "failed [redacted]"},
		{name: "bearer", line: "Authorization: Bearer " + token, want: "[redacted]"},
		{name: "url user", line: "dial https://user:" + token + "@example.test/path", want: "dial https://example.test/path"},
		{name: "second line ignored by caller", line: "The current working directory was deleted", want: "The current working directory was deleted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactProbeLine(tc.line, []string{home})
			if got != tc.want || strings.Contains(got, home) || strings.Contains(got, "/Users/") || strings.Contains(got, "/home/") || strings.Contains(got, secret) || strings.Contains(got, token) {
				t.Fatalf("got %q", got)
			}
		})
	}
	long := "The current working directory was deleted " + strings.Repeat("word ", 40)
	capped := redactProbeLine(long, nil)
	if len([]rune(capped)) > probeDetailLimit || !strings.HasPrefix(capped, "The current working directory was deleted") || !strings.HasSuffix(capped, "...") {
		t.Fatalf("cap %q (%d)", capped, len([]rune(capped)))
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
	if !errors.Is(err, harnesslaunch.ErrStart) || !strings.Contains(err.Error(), "loader failed") || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sk-") {
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

func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
