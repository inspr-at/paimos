// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type Command struct {
	Path         string
	Args         []string
	Input        []byte
	Env          []string
	StatusStderr bool
	OutputLimit  int
}
type Executor interface {
	Run(context.Context, Command) ([]byte, error)
}
type OSExecutor struct{}
type CommandError struct{ ExitCode int }

func (e *CommandError) Error() string { return "local command unavailable" }

type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	maximum := b.max
	if maximum == 0 {
		maximum = 16 << 10
	}
	if b.Len()+len(p) > maximum {
		return 0, errors.New("command output exceeds bound")
	}
	return b.Buffer.Write(p)
}
func (OSExecutor) Run(ctx context.Context, c Command) ([]byte, error) {
	if !filepath.IsAbs(c.Path) {
		return nil, errors.New("command executable must be pinned")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Stdin = bytes.NewReader(c.Input)
	cmd.Env = c.Env
	limit := c.OutputLimit
	if limit < 0 || limit > 256<<10 {
		return nil, errors.New("invalid output bound")
	}
	b := &boundedBuffer{max: limit}
	cmd.Stdout = b
	cmd.Stderr = io.Discard
	stderr := &boundedBuffer{}
	if c.StatusStderr {
		cmd.Stderr = stderr
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, &CommandError{ExitCode: exit.ExitCode()}
		}
		return nil, errors.New("local command unavailable")
	}
	if c.StatusStderr && stderr.Len() > 0 {
		if b.Len() > 0 {
			return nil, errors.New("account status channels are ambiguous")
		}
		return stderr.Bytes(), nil
	}
	return b.Bytes(), nil
}

type Platform struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Service string `json:"service"`
}

func SupportedPlatform(goos, arch string) (Platform, error) {
	if (goos != "darwin" && goos != "linux") || (arch != "amd64" && arch != "arm64") {
		return Platform{}, errors.New("supported platforms are macOS and Linux systemd user on arm64/amd64")
	}
	service := "launchd"
	if goos == "linux" {
		service = "systemd-user"
	}
	return Platform{goos, arch, service}, nil
}
func CurrentPlatform() (Platform, error) { return SupportedPlatform(runtime.GOOS, runtime.GOARCH) }

type Candidate struct {
	Key       string `json:"account_key"`
	Harness   string `json:"harness"`
	Label     string `json:"label"`
	ProfileID string `json:"model_profile_id,omitempty"`
	Path      string `json:"-"`
	Home      string `json:"-"`
	Version   string `json:"-"`
	Identity  string `json:"-"`
	Login     string `json:"-"`
	Managed   bool   `json:"-"`
}

type Discovery struct {
	Executor      Executor
	LookPath      func(string) (string, error)
	Home          string
	CodexIdentity func(context.Context, string, string) (string, error)
}

var safeLabel = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,128}$`)
var safeVersion = regexp.MustCompile(`(?:^|[[:space:]])v?([0-9]+\.[0-9]+\.[0-9]+(?:[-+.][a-zA-Z0-9.-]+)?)(?:$|[[:space:]])`)

func (d Discovery) Detect(ctx context.Context, harness, accountContext string) (Candidate, error) {
	c := Candidate{Harness: harness, Login: "missing"}
	name, authArgs := harness, []string{}
	switch harness {
	case "codex":
		authArgs = []string{"login", "status"}
		c.Home = filepath.Join(d.Home, ".codex")
	case "claude":
		authArgs = []string{"auth", "status", "--json"}
		c.Home = filepath.Join(d.Home, ".claude")
	case "cursor":
		name = "cursor-agent"
		authArgs = []string{"status", "--format", "json"}
	case "grok":
		// Native Grok's existing probe opens its auth store. Guided enrollment
		// cannot use it; an authenticated, value-free status API is required.
		return c, errors.New("Grok guided enrollment blocked: safe account identity probe unavailable; vendor login stores are not read")
	default:
		return c, errors.New("unsupported guided harness")
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.Executor == nil {
		d.Executor = OSExecutor{}
	}
	path, err := d.LookPath(name)
	if err != nil {
		return c, errors.New("harness executable missing; install it through the vendor's normal installer")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(physical) {
		return c, errors.New("harness executable cannot be pinned")
	}
	info, err := os.Stat(physical)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return c, errors.New("harness executable unavailable")
	}
	c.Path = physical
	c.Managed = strings.HasPrefix(physical, "/nix/store/") || strings.Contains(path, "/.nix-profile/")
	raw, err := d.Executor.Run(ctx, Command{Path: physical, Args: []string{"--version"}})
	if err != nil {
		return c, errors.New("harness version unavailable")
	}
	match := safeVersion.FindSubmatch(raw)
	if len(match) != 2 {
		return c, errors.New("harness version not recognized")
	}
	c.Version = string(match[1])
	raw, err = d.Executor.Run(ctx, Command{Path: physical, Args: authArgs, StatusStderr: harness == "codex"})
	if err != nil {
		return c, errors.New("vendor sign-in unavailable; use the vendor's normal login, then resume setup")
	}
	switch harness {
	case "codex":
		if strings.TrimSpace(string(raw)) != "Logged in using ChatGPT" {
			return c, errors.New("Codex subscription sign-in required; run codex login normally and resume setup")
		}
		inspect := d.CodexIdentity
		if inspect == nil {
			inspect = CodexIdentity
		}
		identity, err := inspect(ctx, physical, c.Home)
		if err != nil {
			return c, errors.New("Codex account identity unavailable; use normal vendor login and resume")
		}
		if !safeLabel.MatchString(identity) || accountContext != "" && !strings.EqualFold(accountContext, identity) {
			return c, errors.New("Codex signed-in identity differs from the selected account context")
		}
		c.Identity = identity
		c.Label = identity
	case "claude":
		var a struct {
			LoggedIn   bool   `json:"loggedIn"`
			Email      string `json:"email"`
			AuthMethod string `json:"authMethod"`
		}
		if json.Unmarshal(raw, &a) != nil || !a.LoggedIn || !safeLabel.MatchString(a.Email) || a.AuthMethod == "api_key" {
			return c, errors.New("Claude subscription identity unavailable; use claude auth login normally and resume setup")
		}
		c.Identity = a.Email
		c.Label = a.Email
	case "cursor":
		var a struct {
			Status string `json:"status"`
			Auth   bool   `json:"isAuthenticated"`
			User   *struct {
				ID    json.RawMessage `json:"userId"`
				Email string          `json:"email"`
			} `json:"userInfo"`
		}
		if json.Unmarshal(raw, &a) != nil || a.Status != "authenticated" || !a.Auth || a.User == nil || !safeLabel.MatchString(a.User.Email) {
			return c, errors.New("Cursor subscription identity unavailable; use cursor-agent login normally and resume setup")
		}
		c.Identity = strings.Trim(string(a.User.ID), "\"")
		c.Label = a.User.Email
		if !safeLabel.MatchString(c.Identity) {
			return c, errors.New("Cursor account identity unavailable")
		}
	}
	c.Login = "signed_in"
	return c, nil
}

// ManagedPath uses metadata only. No vendor configuration or credentials are
// opened. Any symlinked service/config path is left to its owning configuration.
func ManagedPath(path string) bool {
	for p := path; p != "." && p != "/"; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return strings.HasPrefix(path, "/nix/store/")
}
