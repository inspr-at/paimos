// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// managedToolServer is one ephemeral, run-bound MCP listener. No credential is
// persisted; a server is closed as soon as the owned process finishes.
type managedToolServer struct {
	listener net.Listener
	server   *http.Server
	tools    RunTools
}

func (s *managedToolServer) Close() error {
	if s == nil {
		return nil
	}
	return s.server.Close()
}

type toolBinding struct {
	api                                   RunToolAPI
	workOrderID, runID, workspace, branch string
	active                                func() bool
	replySender                           func(string) (string, bool)
	requestDone                           func()
}

func startManagedTools(credential string, b toolBinding) (*managedToolServer, error) {
	if credential == "" || b.api == nil || b.workOrderID == "" || b.runID == "" || b.workspace == "" || b.active == nil {
		return nil, errors.New("managed tools are not bound")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "aeon-managed-run", Version: "1"}, nil)
	addManagedTools(s, b)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A loopback bind alone does not prevent browser requests or DNS rebinding.
		if r.Host != listener.Addr().String() || r.Header.Get("Origin") != "" ||
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(credential)) != 1 ||
			!b.active() {
			http.Error(w, "run capability unavailable", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	out := &managedToolServer{listener: listener, server: httpServer,
		tools: RunTools{URL: "http://" + listener.Addr().String(), Token: credential}}
	go func() { _ = httpServer.Serve(listener) }()
	return out, nil
}

type commentArgs struct {
	Body string `json:"body" jsonschema:"Markdown comment on this run's work order"`
}
type statusArgs struct {
	Status           string `json:"status" jsonschema:"running, blocked, or done"`
	ExpectedRevision int64  `json:"expected_revision" jsonschema:"Current work order revision"`
}
type criterionArgs struct {
	CriterionID string `json:"criterion_id" jsonschema:"Criterion ID from the run contract"`
	Checked     bool   `json:"checked" jsonschema:"Whether this criterion is satisfied"`
}
type evidenceArgs struct {
	CriterionID string `json:"criterion_id" jsonschema:"Criterion ID from the run contract"`
	Reference   string `json:"reference" jsonschema:"Bounded text evidence, for example a test result or commit ID"`
}
type approvalArgs struct {
	Scope     string `json:"scope" jsonschema:"Requested run permission scope"`
	Rationale string `json:"rationale" jsonschema:"Reason for the request"`
}
type replyArgs struct {
	MessageID      string `json:"message_id" jsonschema:"Received inbox message ID"`
	Body           string `json:"body" jsonschema:"Reply body"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Stable retry key"`
}
type terminalArgs struct {
	Command   string   `json:"command" jsonschema:"go, npm, or git"`
	Args      []string `json:"args" jsonschema:"Argument array; no shell syntax"`
	Directory string   `json:"directory,omitempty" jsonschema:"root or web; npm runs in web"`
}

func addManagedTools(s *mcp.Server, b toolBinding) {
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_comment", Description: "Comment on the bound work order."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in commentArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() || len(in.Body) > 65536 || strings.TrimSpace(in.Body) == "" {
				return nil, "", errors.New("run or comment unavailable")
			}
			return nil, "comment recorded", b.api.Comment(ctx, b.workOrderID, in.Body)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_status", Description: "Set the bound work order status with its revision. A done request is applied after successful run completion and server checks."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in statusArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() || in.ExpectedRevision < 1 || (in.Status != "running" && in.Status != "blocked" && in.Status != "done") {
				return nil, "", errors.New("invalid status transition")
			}
			if in.Status == "done" {
				order, err := b.api.WorkOrder(ctx, b.workOrderID)
				if err != nil {
					return nil, "", err
				}
				if order.Revision != in.ExpectedRevision {
					return nil, "", errors.New("work order revision changed")
				}
				for _, criterion := range order.Criteria {
					if criterion.CheckedAt == nil {
						return nil, "", errors.New("acceptance criteria remain unchecked")
					}
				}
				if b.requestDone == nil {
					return nil, "", errors.New("completion handoff unavailable")
				}
				b.requestDone()
				return nil, "done requested; the daemon will apply it after this run completes successfully", nil
			}
			_, err := b.api.SetWorkStatus(ctx, b.workOrderID, in.ExpectedRevision, in.Status)
			return nil, "status recorded", err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_check_criterion", Description: "Check one criterion of the bound work order."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in criterionArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() || !boundCriterion(ctx, b, in.CriterionID) {
				return nil, "", errors.New("criterion is not bound to this run")
			}
			return nil, "criterion recorded", b.api.CheckCriterion(ctx, b.workOrderID, in.CriterionID, in.Checked)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_evidence", Description: "Attach text evidence to one criterion of this run's work order."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in evidenceArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() || !boundCriterion(ctx, b, in.CriterionID) || len(in.Reference) > 65536 || strings.TrimSpace(in.Reference) == "" {
				return nil, "", errors.New("evidence target or body invalid")
			}
			return nil, "evidence recorded", b.api.Evidence(ctx, b.workOrderID, b.runID, in.CriterionID, in.Reference)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_request_approval", Description: "Ask a person for a scoped approval on this run; this does not grant permission."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in approvalArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() || !scopePattern.MatchString(in.Scope) || len(in.Rationale) > 8000 || strings.TrimSpace(in.Rationale) == "" {
				return nil, "", errors.New("approval request invalid")
			}
			expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			return nil, "approval requested", b.api.RequestApproval(ctx, b.runID, in.Scope, in.Rationale, expires)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_reply", Description: "Reply to a received inbox message as this run's agent."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in replyArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() || !uuidPattern.MatchString(in.MessageID) || len(in.Body) > 65536 || strings.TrimSpace(in.Body) == "" || len(in.IdempotencyKey) > 128 || in.IdempotencyKey == "" {
				return nil, "", errors.New("inbox reply invalid")
			}
			if b.replySender == nil {
				return nil, "", errors.New("inbox delivery is not bound")
			}
			sender, ok := b.replySender(in.MessageID)
			if !ok || !uuidPattern.MatchString(sender) {
				return nil, "", errors.New("inbox delivery is not bound")
			}
			return nil, "reply recorded", b.api.ReplyInbox(ctx, in.MessageID, sender, in.Body, in.IdempotencyKey)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_terminal", Description: "Run an allowlisted test, build, or git operation in this run's workspace. No shell and no push."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in terminalArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() {
				return nil, "", ErrNotOwned
			}
			out, err := runTerminal(ctx, b.workspace, b.branch, in)
			return nil, out, err
		})
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

func boundCriterion(ctx context.Context, b toolBinding, id string) bool {
	if !uuidPattern.MatchString(id) {
		return false
	}
	// The API method is purposely used instead of trusting a criterion ID in a
	// prompt, since the work order can change during the run.
	order, err := b.api.WorkOrder(ctx, b.workOrderID)
	if err != nil {
		return false
	}
	for _, c := range order.Criteria {
		if c.ID == id {
			return true
		}
	}
	return false
}

func runTerminal(ctx context.Context, workspace, branch string, in terminalArgs) (string, error) {
	if in.Directory != "" && in.Directory != "root" && in.Directory != "web" {
		return "", errors.New("terminal directory denied")
	}
	if (in.Command == "npm") != (in.Directory == "web") {
		return "", errors.New("terminal directory does not match command")
	}
	if len(in.Args) > 32 {
		return "", errors.New("too many arguments")
	}
	for _, arg := range in.Args {
		if arg == "" || len(arg) > 1024 || strings.ContainsAny(arg, "\x00\r\n") {
			return "", errors.New("invalid terminal argument")
		}
	}
	if !allowedTerminal(in.Command, in.Args) {
		return "", errors.New("terminal command denied")
	}
	// Reject unsupported execution before even launching toolchain probes.
	if runtime.GOOS != "darwin" {
		return "", errors.New("bounded terminal requires the macOS sandbox")
	}
	if !ownedprocess.TrackingSupported() {
		return "", errors.New("safe child lifetime observation unsupported")
	}
	physicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil || !filepath.IsAbs(workspace) {
		return "", errors.New("terminal workspace unavailable")
	}
	workspace = physicalWorkspace
	toolPath, err := exec.LookPath(in.Command)
	if err != nil {
		return "", errors.New("terminal toolchain unavailable")
	}
	toolPath, err = filepath.EvalSymlinks(toolPath)
	if err != nil || pathWithin(workspace, toolPath) {
		return "", errors.New("terminal toolchain is not independent of workspace")
	}
	if in.Command == "git" && (in.Args[0] == "add" || in.Args[0] == "commit") {
		current, err := exec.CommandContext(ctx, toolPath, "-C", workspace, "branch", "--show-current").Output()
		if err != nil || strings.TrimSpace(string(current)) != branch || branch == "" || branch == "main" || branch == "master" {
			return "", errors.New("run branch is not checked out")
		}
		if in.Args[0] == "add" {
			for _, path := range in.Args[2:] {
				if !safeStagePath(toolPath, workspace, path) {
					return "", errors.New("git add path denied")
				}
			}
		} else if !safeStagedSet(ctx, toolPath, workspace) {
			return "", errors.New("git staged file set denied")
		}
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// Tests and package scripts execute repository code. On the local macOS
	// daemon they run under an OS network fence, including subprocesses. Package
	// dependencies must already be cached; only loopback test services work.
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("terminal home unavailable")
	}
	physicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", errors.New("terminal home is not physical")
	}
	if pathWithin(workspace, physicalHome) {
		return "", errors.New("terminal workspace contains home")
	}
	tmp, err := os.MkdirTemp("", "aeon-terminal-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) // This invocation owns the private cache and temp tree.
	for _, name := range []string{"go-build", "go-mod", "npm-cache"} {
		if err := os.Mkdir(filepath.Join(tmp, name), 0700); err != nil {
			return "", err
		}
	}
	for _, name := range []string{"gitconfig", "npmrc"} {
		if err := os.WriteFile(filepath.Join(tmp, name), nil, 0600); err != nil {
			return "", err
		}
	}
	physicalTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return "", err
	}
	// A private module cache is filled only from the host's existing download
	// cache. The host cache is read-only to sandboxed code; no registry access or
	// GOFLAGS=-modcacherw is needed. Go's build cache and npm cache are private.
	moduleProxy := "off"
	moduleCache := ""
	goRoot := ""
	if in.Command == "go" {
		out, err := exec.CommandContext(deadline, toolPath, "env", "GOROOT", "GOMODCACHE").Output()
		if err != nil {
			return "", errors.New("Go toolchain unavailable")
		}
		paths := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(paths) != 2 {
			return "", errors.New("Go toolchain paths unavailable")
		}
		goRoot, err = filepath.EvalSymlinks(paths[0])
		if err != nil || !filepath.IsAbs(goRoot) || pathWithin(goRoot, physicalHome) {
			return "", errors.New("Go root is not physical")
		}
		moduleCache = paths[1]
		if !filepath.IsAbs(moduleCache) {
			return "", errors.New("Go module cache is not absolute")
		}
		moduleCache, err = filepath.EvalSymlinks(moduleCache)
		if err != nil && !os.IsNotExist(err) {
			return "", errors.New("Go module cache is not physical")
		}
		if err == nil && pathWithin(moduleCache, physicalHome) {
			return "", errors.New("Go module cache contains home")
		}
		if err == nil {
			moduleProxy = (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(moduleCache, "cache", "download"))}).String()
		} else {
			moduleCache = ""
		}
	}
	profile := `(version 1) (allow default) (deny network*) (allow network-outbound (remote ip "localhost:*")) (allow network-bind (local ip "localhost:*")) (allow network-inbound (local ip "localhost:*")) (deny file-write*)`
	profile += fmt.Sprintf(" (allow file-write* (subpath %q)) (allow file-write* (subpath %q))", physicalWorkspace, physicalTmp)
	// Git opens the null device read/write while staging. It has no persistent
	// backing data and grants no filesystem write outside the two run roots.
	profile += ` (allow file-write* (literal "/dev/null"))`
	// Deny the rest of home even to test binaries and package scripts. Read
	// exceptions are limited to this workspace and offline dependency cache.
	for _, path := range []string{home, physicalHome} {
		profile += fmt.Sprintf(" (deny file-read* (subpath %q))", path)
	}
	for _, path := range []string{physicalWorkspace, physicalTmp, moduleCache, goRoot} {
		if path != "" {
			profile += fmt.Sprintf(" (allow file-read* (subpath %q))", path)
		}
	}
	// A toolchain installed under home may be needed to execute the command.
	// Resolve links so a Nix profile grants only its immutable store target.
	toolNames := []string{in.Command}
	if in.Command == "npm" {
		toolNames = append(toolNames, "node")
	}
	for _, name := range toolNames {
		path, lookupErr := exec.LookPath(name)
		if lookupErr != nil {
			return "", fmt.Errorf("terminal toolchain unavailable: %s", name)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		profile += fmt.Sprintf(" (allow file-read* (literal %q))", path)
		if pathWithin(physicalHome, path) {
			root := filepath.Dir(filepath.Dir(path))
			if root == physicalHome {
				return "", errors.New("terminal toolchain root is home")
			}
			profile += fmt.Sprintf(" (allow file-read* (subpath %q))", root)
		}
	}
	for _, path := range []string{"Secrets", ".ssh", ".inspr/secrets", ".aws", ".gnupg", ".config/gh", "Library/Keychains"} {
		full := filepath.Join(physicalHome, path)
		profile += fmt.Sprintf(" (deny file-read* (subpath %q)) (deny file-write* (subpath %q))", full, full)
	}
	argv := append([]string{"-p", profile, toolPath}, in.Args...)
	cmd := exec.Command("/usr/bin/sandbox-exec", argv...)
	cmd.Dir = workspace
	if in.Directory == "web" {
		web := filepath.Join(workspace, "web")
		physical, err := filepath.EvalSymlinks(web)
		if err != nil || physical != web {
			return "", errors.New("web directory is not physical")
		}
		cmd.Dir = web
	}
	cmd.Env = terminalEnvironment(physicalTmp, moduleProxy, goRoot)
	if !ownedprocess.Configure(cmd) {
		return "", errors.New("owned process groups are unsupported")
	}
	// CombinedOutput is capped by a pipe reader so a noisy child cannot grow
	// memory indefinitely. Closing the pipe kills the owned process on overflow.
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer pipeR.Close()
	cmd.Stdout, cmd.Stderr = pipeW, pipeW
	if err := cmd.Start(); err != nil {
		_ = pipeW.Close()
		return "", err
	}
	_ = pipeW.Close()
	lifetime := ownedprocess.Track(cmd)
	if err := lifetime.Verify(); err != nil {
		// No waiter or cancellation worker exists yet. If group verification
		// fails, clean up only the exact unreaped child returned by Start.
		_ = cmd.Process.Kill()
		_ = lifetime.Wait()
		return "", err
	}
	return collectTerminal(deadline, pipeR, lifetime)
}

// terminalLifetime permits deterministic scheduling of the signal/reap race in
// tests. Production always supplies the verified ownedprocess.Lifetime above.
type terminalLifetime interface {
	Signal(force bool) error
	Wait() error
}

func collectTerminal(ctx context.Context, output io.Reader, lifetime terminalLifetime) (string, error) {
	finished := make(chan struct{})
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		select {
		case <-ctx.Done():
			_ = lifetime.Signal(true)
		case <-finished:
		}
	}()
	defer func() {
		close(finished)
		<-cancelDone
	}()
	// Keep cancellation active through Wait: EOF does not imply child exit.
	// Lifetime fences a delayed signal against reaping; joining the worker
	// also prevents it escaping this invocation on any return path.
	out, readErr := io.ReadAll(io.LimitReader(output, (64<<10)+1))
	if len(out) > 64<<10 {
		_ = lifetime.Signal(true)
		_ = lifetime.Wait()
		return "", errors.New("terminal output exceeds 64 KiB")
	}
	if readErr != nil {
		_ = lifetime.Signal(true)
		_ = lifetime.Wait()
		return "", readErr
	}
	err := lifetime.Wait()
	if err != nil {
		return string(out), fmt.Errorf("terminal command failed: %w", err)
	}
	return string(out), nil
}

func allowedTerminal(command string, args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch command {
	case "go":
		if args[0] != "test" && args[0] != "build" && args[0] != "vet" {
			return false
		}
		for _, a := range args[1:] {
			if a == "-race" || a == "-count=1" || a == "-v" {
				continue
			}
			if !localGoPackage(a) {
				return false
			}
		}
		return true
	case "npm":
		return len(args) == 2 && args[0] == "run" && (args[1] == "typecheck" || args[1] == "build" || args[1] == "test")
	case "git":
		switch args[0] {
		case "status":
			return len(args) == 1 || len(args) == 2 && args[1] == "--short"
		case "diff":
			return false
		case "rev-parse":
			return len(args) == 2 && args[1] == "HEAD"
		case "add":
			return len(args) >= 3 && args[1] == "--"
		case "commit":
			return len(args) == 3 && args[1] == "-m" && len(args[2]) <= 200 && !strings.HasPrefix(args[2], "-")
		}
	}
	return false
}

var goPackageSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func localGoPackage(arg string) bool {
	if arg == "." {
		return true
	}
	if !strings.HasPrefix(arg, "./") || strings.Contains(arg, "\\") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(arg, "./"), "/")
	for i, part := range parts {
		if part == "..." && i == len(parts)-1 {
			continue
		}
		if !goPackageSegment.MatchString(part) {
			return false
		}
	}
	return true
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func safeStagePath(gitPath, workspace, path string) bool {
	if path == "." || filepath.IsAbs(path) || strings.HasPrefix(path, "-") {
		return false
	}
	clean := filepath.Clean(path)
	if clean != path || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == "secrets" || strings.HasPrefix(lower, ".env") || strings.HasSuffix(lower, ".age") || strings.HasSuffix(lower, ".gpg") || strings.HasPrefix(lower, "id_") || strings.HasSuffix(lower, "_rsa") || strings.HasSuffix(lower, "_ed25519") {
			return false
		}
	}
	full := filepath.Join(workspace, clean)
	info, err := os.Lstat(full)
	if err == nil {
		return info.Mode().IsRegular()
	}
	if !os.IsNotExist(err) {
		return false
	}
	// A deleted tracked file is safe to stage by its exact path. A missing
	// directory or pathspec that expands to several files is not.
	out, err := exec.Command(gitPath, "-C", workspace, "ls-files", "-z", "--", clean).Output()
	return err == nil && string(out) == clean+"\x00"
}

func safeStagedSet(ctx context.Context, gitPath, workspace string) bool {
	cmd := exec.CommandContext(ctx, gitPath, "-C", workspace, "diff", "--cached", "--name-only", "-z")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 || len(out) > 64<<10 {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if !safeStagePath(gitPath, workspace, part) {
			return false
		}
	}
	return true
}

func terminalEnvironment(tmp, moduleProxy, goRoot string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true, "AEON_TEST_DATABASE_URL": true}
	out := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(tmp, "gitconfig"), "GIT_ALLOW_PROTOCOL=file", "GOPROXY=" + moduleProxy, "GOSUMDB=off", "GOENV=off", "GOTOOLCHAIN=local", "GOCACHE=" + filepath.Join(tmp, "go-build"), "GOMODCACHE=" + filepath.Join(tmp, "go-mod"), "NPM_CONFIG_CACHE=" + filepath.Join(tmp, "npm-cache"), "NPM_CONFIG_USERCONFIG=" + filepath.Join(tmp, "npmrc"), "NPM_CONFIG_OFFLINE=true", "NPM_CONFIG_AUDIT=false", "TMPDIR=" + tmp, "CI=1"}
	if goRoot != "" {
		out = append(out, "GOROOT="+goRoot)
	}
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && allowed[name] {
			out = append(out, entry)
		}
	}
	return out
}
