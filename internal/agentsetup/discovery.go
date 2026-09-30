// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/grokprobe"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/piprobe"
)

type Command struct {
	DiscardOutput bool
	Path          string
	Args          []string
	Input         []byte
	Env           []string
	Dir           string
	StatusStderr  bool
	OutputLimit   int
}
type Executor interface {
	Run(context.Context, Command) ([]byte, error)
}
type OSExecutor struct{}
type CommandError struct {
	ExitCode int
	stderr   string
	command  string
}

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
	if c.Dir != "" {
		if !filepath.IsAbs(c.Dir) {
			return nil, errors.New("command directory must be absolute")
		}
		info, err := os.Stat(c.Dir)
		if err != nil || !info.IsDir() {
			return nil, errors.New("command directory unavailable")
		}
		cmd.Dir = c.Dir
	}
	limit := c.OutputLimit
	if limit < 0 || limit > 256<<10 {
		return nil, errors.New("invalid output bound")
	}
	b := &boundedBuffer{max: limit}
	cmd.Stdout = b
	if c.DiscardOutput {
		cmd.Stdout = io.Discard
	}
	stderr := &boundedBuffer{}
	snippet := &truncBuffer{max: 512}
	cmd.Stderr = snippet
	if c.StatusStderr {
		cmd.Stderr = io.MultiWriter(stderr, snippet)
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, &CommandError{ExitCode: exit.ExitCode(), stderr: capturedProbeLine(snippet.buf.Bytes(), snippet.truncated), command: probeCommandName(c.Path)}
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
	Key       string             `json:"account_key"`
	Harness   string             `json:"harness"`
	Label     string             `json:"label"`
	ProfileID string             `json:"model_profile_id,omitempty"`
	Provider  string             `json:"provider,omitempty"`
	Path      string             `json:"-"`
	Home      string             `json:"-"`
	Version   string             `json:"-"`
	Identity  string             `json:"-"`
	Login     string             `json:"-"`
	Managed   bool               `json:"-"`
	Grok      grokprobe.Binding  `json:"-"`
	PiNode    piprobe.Node       `json:"-"`
	Node      harnesslaunch.Node `json:"-"`
}

// Interpreter retains the legacy pi field while sharing validation with other launchers.
func (c Candidate) Interpreter() harnesslaunch.Node {
	if c.Harness == "pi" {
		return c.PiNode
	}
	return c.Node
}

type Discovery struct {
	PiHome        string // Explicit private per-account pi profile; never uploaded.
	Executor      Executor
	LookPath      func(string) (string, error)
	Home          string
	NodePath      string
	Workspace     string
	CodexIdentity func(context.Context, string, string) (string, error)
	GrokProbe     func(context.Context, grokprobe.Binding) (grokprobe.Identity, error)
	PiProvider    func(context.Context, string, string, string, string) (string, error)
}

var safeLabel = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,128}$`)
var safeVersion = regexp.MustCompile(`(?:^|[[:space:]])v?([0-9]+\.[0-9]+\.[0-9]+(?:[-+.][a-zA-Z0-9.-]+)?)(?:$|[[:space:]])`)

// Available offers only accounts identified by the vendor's read-only status
// command. A failed or missing sign-in never becomes an enrollment candidate.
func (d Discovery) Available(ctx context.Context, accountContext string) []Candidate {
	var candidates []Candidate
	for _, harness := range []string{"claude", "codex", "cursor", "grok"} {
		if ctx.Err() != nil {
			break
		}
		if c, err := d.Detect(ctx, harness, accountContext); err == nil {
			candidates = append(candidates, c)
		}
	}
	return candidates
}

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
		return d.detectGrok(ctx, accountContext)
	case "pi":
		c.Home = filepath.Join(d.Home, ".pi", "agent")
		if d.PiHome != "" {
			c.Home = d.PiHome
		}
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
	probeDir, err := d.probeDirectory()
	if err != nil {
		return c, err
	}
	c.Node, err = d.ResolveNode(ctx, physical)
	if err != nil {
		return c, err
	}
	childEnv := harnesslaunch.Environment(os.Environ(), c.Node.Path)
	versionCommand := Command{Path: physical, Args: []string{"--version"}, Env: childEnv, Dir: probeDir}
	if harness == "pi" {
		c.PiNode, c.Node = c.Node, harnesslaunch.Node{}
		versionCommand.Env = piprobe.Environment(c.Home, c.PiNode.Path)
	}
	raw, err := d.Executor.Run(ctx, versionCommand)
	// A shell guard around npm Codex hides its env-node shebang. If the
	// service PATH cannot start it, retry the same guard with a trusted Node
	// pin; never bypass the guard or inherit the interactive shell's PATH.
	var exit *CommandError
	if harness == "codex" && c.Node.Path == "" && d.NodePath == "" && errors.As(err, &exit) && exit.ExitCode == 127 {
		if node, lookupErr := d.LookPath("node"); lookupErr == nil {
			retry := d
			retry.NodePath = node
			c.Node, err = retry.ResolveNode(ctx, physical)
			if err != nil {
				return c, err
			}
			childEnv = harnesslaunch.Environment(os.Environ(), c.Node.Path)
			versionCommand.Env = childEnv
			raw, err = d.Executor.Run(ctx, versionCommand)
		}
	}
	if err != nil {
		return c, annotateProbe(fmt.Errorf("%w; the launcher must also work with the service PATH", harnesslaunch.ErrStart), err, d.Home, d.Workspace)
	}
	match := safeVersion.FindSubmatch(raw)
	if len(match) != 2 {
		return c, fmt.Errorf("%w; harness version not recognized", harnesslaunch.ErrStart)
	}
	c.Version = string(match[1])
	if harness == "pi" {
		probe := d.PiProvider
		if probe == nil {
			probe = piprobe.Provider
		}
		provider, err := probe(ctx, c.Path, c.Home, accountContext, c.PiNode.Path)
		if errors.Is(err, piprobe.ErrStart) {
			return c, piprobe.ErrStart
		}
		if errors.Is(err, piprobe.ErrPrivateProfile) {
			return c, piprobe.ErrPrivateProfile
		}
		if err != nil || !piprobe.ValidProvider(provider) || accountContext != "" && provider != accountContext {
			return c, errors.New("pi provider configuration unavailable; use pi /login and /model normally, then resume setup")
		}
		// The public RPC identifies a configured provider, not a person. Keep
		// the profile path private and do not invent an email or subscription.
		c.Identity, c.Label, c.Login = provider, "pi / "+provider+" (local profile)", "signed_in"
		c.Provider = provider
		return c, nil
	}
	raw, err = d.Executor.Run(ctx, Command{Path: physical, Args: authArgs, Env: childEnv, Dir: probeDir, StatusStderr: harness == "codex"})
	if err != nil {
		var exit *CommandError
		if errors.As(err, &exit) && (exit.ExitCode == 126 || exit.ExitCode == 127) {
			return c, annotateProbe(harnesslaunch.ErrStart, err, d.Home, d.Workspace)
		}
		return c, errors.New("vendor sign-in unavailable; use the vendor's normal login, then resume setup")
	}
	switch harness {
	case "codex":
		if strings.TrimSpace(string(raw)) != "Logged in using ChatGPT" {
			return c, errors.New("Codex subscription sign-in required; run codex login normally and resume setup")
		}
		inspect := d.CodexIdentity
		if inspect == nil {
			inspect = func(ctx context.Context, path, home string) (string, error) {
				return codexIdentity(ctx, path, home, c.Node.Path, probeDir)
			}
		}
		identity, err := inspect(ctx, physical, c.Home)
		if err != nil {
			return c, errors.New("Codex account identity unavailable; use normal vendor login and resume")
		}
		// A confirmed ChatGPT account need not disclose an email. Keep explicit
		// account-context matching strict: an unnamed account cannot prove it.
		if identity == "" && accountContext == "" {
			identity = CodexChatGPTLogin
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

// ResolveNode inspects the launcher and pins Node without executing package scripts.
func (d Discovery) ResolveNode(ctx context.Context, path string) (harnesslaunch.Node, error) {
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.Executor == nil {
		d.Executor = OSExecutor{}
	}
	needed, err := harnesslaunch.NeedsNode(path)
	if err != nil || !needed && d.NodePath == "" {
		return harnesslaunch.Node{}, err
	}
	action := fmt.Errorf("%w; pass --node-path to an installed Node executable outside the workspace", harnesslaunch.ErrStart)
	node := d.NodePath
	if node == "" {
		node, err = d.LookPath("node")
		if err != nil {
			return harnesslaunch.Node{}, action
		}
	}
	physical, err := pinnedRegular(node, d.Workspace, true)
	if err != nil {
		return harnesslaunch.Node{}, fmt.Errorf("%w; %w", action, err)
	}
	if filepath.Base(physical) != "node" {
		return harnesslaunch.Node{}, action
	}
	raw, err := d.Executor.Run(ctx, Command{Path: physical, Args: []string{"--version"}, Env: harnesslaunch.Environment(nil, physical), Dir: commandDir(d.Workspace, d.Home)})
	match := safeVersion.FindSubmatch(raw)
	if err != nil || len(match) != 2 {
		return harnesslaunch.Node{}, annotateProbe(action, err, d.Home, d.Workspace)
	}
	return harnesslaunch.Node{Path: physical, Version: string(match[1])}, nil
}

func validateNode(path, workspace string, node harnesslaunch.Node) error {
	needed, err := harnesslaunch.NeedsNode(path)
	if err != nil {
		return err
	}
	if node.Path == "" && node.Version == "" && !needed {
		return nil
	}
	physical, err := pinnedRegular(node.Path, workspace, true)
	if err != nil || physical != node.Path || filepath.Base(physical) != "node" || node.Version == "" {
		return harnesslaunch.ErrStart
	}
	match := safeVersion.FindStringSubmatch(node.Version)
	if len(match) != 2 || match[1] != node.Version {
		return harnesslaunch.ErrStart
	}
	return nil
}

func (d Discovery) detectGrok(ctx context.Context, accountContext string) (Candidate, error) {
	c := Candidate{Harness: "grok", Login: "missing"}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return c, errors.New("native Grok guided setup requires macOS arm64")
	}
	if !filepath.IsAbs(d.Home) || filepath.Clean(d.Home) != d.Home || d.Home == "/" {
		return c, errors.New("user home unavailable")
	}
	physicalHome, err := filepath.EvalSymlinks(d.Home)
	if err != nil || physicalHome != d.Home {
		return c, errors.New("user home must be physical")
	}
	info, err := os.Stat(d.Home)
	if err != nil || !info.IsDir() {
		return c, errors.New("user home unavailable")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() {
		return c, errors.New("user home is not owned by this user")
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	probe := d.GrokProbe
	if probe == nil {
		probe = grokprobe.Probe
	}
	scratch := filepath.Join(d.Home, ".local", "share", "aeon", "grok-scratch")
	var failure error
	for _, option := range []struct{ name, variant string }{{"grok-native", "npm-grok-1.0.30"}, {"xai-grok-pager", "source-xai-grok-pager-1.0.32"}} {
		path, err := d.LookPath(option.name)
		if err != nil {
			continue
		}
		physical, err := filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(physical) || filepath.Base(physical) != option.name {
			failure = errors.New("qualified native Grok executable unavailable")
			continue
		}
		if err := os.MkdirAll(scratch, 0700); err != nil {
			return c, errors.New("private Grok scratch unavailable")
		}
		b := grokprobe.Binding{Variant: option.variant, BinaryPath: physical, AuthPath: filepath.Join(d.Home, ".grok", "auth.json"), ScratchRoot: scratch}
		verified, err := probe(ctx, b)
		if err != nil {
			if strings.Contains(err.Error(), "account unavailable") {
				failure = errors.New("Grok sign-in required; use the vendor's normal login, then resume setup")
			} else {
				failure = err
			}
			continue
		}
		if verified.Binding != (grokprobe.Binding{Variant: b.Variant, BinaryPath: b.BinaryPath, AuthPath: b.AuthPath, ScratchRoot: b.ScratchRoot, PrincipalSHA256: verified.Binding.PrincipalSHA256}) ||
			!safeLabel.MatchString(verified.Label) {
			return c, errors.New("native Grok identity unavailable")
		}
		if accountContext != "" && !strings.EqualFold(accountContext, verified.Label) && accountContext != verified.Binding.PrincipalSHA256 {
			return c, errors.New("Grok signed-in identity differs from selected account context")
		}
		c.Path, c.Home, c.Version = physical, filepath.Join(d.Home, ".grok"), option.variant
		c.Identity, c.Label, c.Grok = verified.Binding.PrincipalSHA256, verified.Label, verified.Binding
		c.Login = "signed_in"
		return c, nil
	}
	if failure != nil {
		return c, failure
	}
	return c, errors.New("qualified native Grok executable missing; install a supported pinned build and sign in normally")
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
