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
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// Tests and package scripts execute repository code. On the local macOS
	// daemon they run under a default-deny OS fence, including subprocesses.
	// Dependencies must be cached; neither loopback nor Unix sockets are allowed.
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
		probe := exec.CommandContext(deadline, toolPath, "env", "GOROOT", "GOMODCACHE")
		probe.Dir = physicalTmp
		probe.Env = terminalEnvironment(physicalTmp, "off", "", toolPath)
		// The only host cache eligible for offline dependency reads is the
		// standard module download cache, never an inherited environment path.
		for i, value := range probe.Env {
			if strings.HasPrefix(value, "GOMODCACHE=") {
				probe.Env[i] = "GOMODCACHE=" + filepath.Join(physicalHome, "go", "pkg", "mod")
			}
		}
		out, err := probe.Output()
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
	readRoots := []string{goRoot}
	if moduleCache != "" {
		readRoots = append(readRoots, filepath.Join(moduleCache, "cache", "download"))
	}
	// Resolve each required executable; grant its package only, never its
	// profile, store, home or installation prefix. Other tools fail closed.
	toolNames := []string{in.Command}
	if in.Command == "npm" {
		toolNames = append(toolNames, "node", "sh", "env")
	}
	var toolPaths []string
	for _, name := range toolNames {
		path, lookupErr := exec.LookPath(name)
		if lookupErr != nil {
			return "", fmt.Errorf("terminal toolchain unavailable: %s", name)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(path) || pathWithin(workspace, path) {
			return "", errors.New("terminal toolchain is not independent of workspace")
		}
		toolPaths = append(toolPaths, path)
		if root := terminalToolRoot(path); root != "" {
			readRoots = append(readRoots, root)
		}
	}
	readRoots, err = terminalToolDependencies(deadline, workspace, physicalTmp, readRoots)
	if err != nil {
		return "", err
	}
	profile := terminalSandboxProfile(physicalWorkspace, physicalTmp, readRoots, toolPaths)
	childEnv := terminalEnvironment(physicalTmp, moduleProxy, goRoot, toolPaths...)
	// Even read-only Git queries can trigger repository-configured helpers.
	// Keep branch/staging checks inside exactly the same sandbox as the command.
	gitProbe := func(args ...string) ([]byte, error) {
		out, err := runSandboxedTerminal(deadline, workspace, profile, toolPath, childEnv, args)
		return []byte(out), err
	}
	if in.Command == "git" && (in.Args[0] == "add" || in.Args[0] == "commit") {
		current, err := gitProbe("branch", "--show-current")
		if err != nil || strings.TrimSpace(string(current)) != branch || branch == "" || branch == "main" || branch == "master" {
			return "", errors.New("run branch is not checked out")
		}
		if in.Args[0] == "add" {
			for _, path := range in.Args[2:] {
				if !safeStagePath(workspace, path, gitProbe) {
					return "", errors.New("git add path denied")
				}
			}
		} else if !safeStagedSet(workspace, gitProbe) {
			return "", errors.New("git staged file set denied")
		}
	}
	directory := workspace
	if in.Directory == "web" {
		web := filepath.Join(workspace, "web")
		physical, err := filepath.EvalSymlinks(web)
		if err != nil || physical != web {
			return "", errors.New("web directory is not physical")
		}
		directory = web
	}
	return runSandboxedTerminal(deadline, directory, profile, toolPath, childEnv, in.Args)
}

func runSandboxedTerminal(ctx context.Context, directory, profile, toolPath string, childEnv, args []string) (string, error) {
	argv := append([]string{"-p", profile, toolPath}, args...)
	cmd := exec.Command("/usr/bin/sandbox-exec", argv...)
	cmd.Dir = directory
	cmd.Env = childEnv
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
	return collectTerminal(ctx, pipeR, lifetime)
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

type terminalGitProbe func(...string) ([]byte, error)

func safeStagePath(workspace, path string, probe terminalGitProbe) bool {
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
	out, err := probe("ls-files", "-z", "--", clean)
	return err == nil && string(out) == clean+"\x00"
}

func safeStagedSet(workspace string, probe terminalGitProbe) bool {
	out, err := probe("diff", "--cached", "--name-only", "-z")
	if err != nil || len(out) == 0 || len(out) > 64<<10 {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if !safeStagePath(workspace, part, probe) {
			return false
		}
	}
	return true
}

// Only package-specific roots qualify for recursive tool reads. In particular,
// /usr, /opt/homebrew, /nix/store and a user's home are never tool roots.
func terminalToolRoot(path string) string {
	for _, prefix := range []string{"/nix/store/", "/opt/homebrew/Cellar/", "/usr/local/Cellar/"} {
		if strings.HasPrefix(path, prefix) {
			parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
			count := 1
			if prefix != "/nix/store/" {
				count = 2
			}
			if len(parts) > count {
				return prefix + strings.Join(parts[:count], "/")
			}
		}
	}
	if i := strings.Index(path, "/node_modules/npm/"); i >= 0 {
		return path[:i] + "/node_modules/npm"
	}
	return ""
}

// Nix binaries reference shared libraries and wrappers in other immutable
// packages. Enumerate only their local closure, never grant the entire store.
func terminalToolDependencies(ctx context.Context, workspace, tmp string, roots []string) ([]string, error) {
	var packages []string
	seen := map[string]bool{}
	for _, root := range roots {
		if strings.HasPrefix(root, "/nix/store/") {
			root = terminalToolRoot(root + "/bin")
			if !seen[root] {
				seen[root] = true
				packages = append(packages, root)
			}
		}
	}
	if len(packages) == 0 {
		return roots, nil
	}
	query, err := exec.LookPath("nix-store")
	if err != nil {
		return nil, errors.New("terminal toolchain dependency query unavailable")
	}
	query, err = filepath.EvalSymlinks(query)
	if err != nil || !filepath.IsAbs(query) || pathWithin(workspace, query) {
		return nil, errors.New("terminal dependency query is not independent of workspace")
	}
	cmd := exec.CommandContext(ctx, query, append([]string{"--query", "--requisites"}, packages...)...)
	cmd.Dir = tmp
	cmd.Env = terminalEnvironment(tmp, "off", "", query)
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("terminal toolchain dependency query failed")
	}
	closure, err := terminalStoreClosure(string(output))
	if err != nil {
		return nil, err
	}
	return append(roots, closure...), nil
}

func terminalStoreClosure(output string) ([]string, error) {
	roots := strings.Fields(output)
	if len(output) > 1<<20 || len(roots) == 0 || len(roots) > 4096 {
		return nil, errors.New("terminal toolchain closure exceeds bounds")
	}
	for _, root := range roots {
		if !strings.HasPrefix(root, "/nix/store/") || filepath.Clean(root) != root ||
			strings.Contains(strings.TrimPrefix(root, "/nix/store/"), "/") ||
			len(strings.TrimPrefix(root, "/nix/store/")) < 34 {
			return nil, errors.New("terminal toolchain closure contains an invalid package")
		}
	}
	return roots, nil
}

func terminalSandboxProfile(workspace, tmp string, readRoots, toolPaths []string) string {
	// No network or socket permission, including localhost and Unix sockets.
	profile := `(version 1) (deny default) (allow process-exec process-fork) (allow sysctl-read)`
	for _, path := range []string{workspace, tmp} {
		profile += fmt.Sprintf(" (allow file-read* file-write* (subpath %q))", path)
	}
	// OS runtime and entropy/null devices only; no blanket filesystem reads.
	for _, path := range append([]string{"/System/Library", "/usr/lib"}, readRoots...) {
		if path != "" {
			profile += fmt.Sprintf(" (allow file-read* (subpath %q))", path)
		}
	}
	for _, path := range append([]string{"/dev/null", "/dev/random", "/dev/urandom"}, toolPaths...) {
		profile += fmt.Sprintf(" (allow file-read* (literal %q))", path)
	}
	profile += ` (allow file-write* (literal "/dev/null"))`
	return profile
}

func terminalEnvironment(tmp, moduleProxy, goRoot string, toolPaths ...string) []string {
	// Construct from constants and verified paths. No inherited AEON_*, DB,
	// provider credentials, loader flags, user configuration or daemon HOME.
	path := []string{}
	for _, tool := range toolPaths {
		path = append(path, filepath.Dir(tool))
	}
	path = append(path, "/usr/bin", "/bin")
	out := []string{"PATH=" + strings.Join(path, string(os.PathListSeparator)), "HOME=" + tmp, "LANG=C", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(tmp, "gitconfig"), "GIT_ALLOW_PROTOCOL=file", "GOPROXY=" + moduleProxy, "GOSUMDB=off", "GOENV=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOCACHE=" + filepath.Join(tmp, "go-build"), "GOMODCACHE=" + filepath.Join(tmp, "go-mod"), "NPM_CONFIG_CACHE=" + filepath.Join(tmp, "npm-cache"), "NPM_CONFIG_USERCONFIG=" + filepath.Join(tmp, "npmrc"), "NPM_CONFIG_OFFLINE=true", "NPM_CONFIG_AUDIT=false", "TMPDIR=" + tmp, "CI=1"}
	if goRoot != "" {
		out = append(out, "GOROOT="+goRoot)
	}
	return out
}
