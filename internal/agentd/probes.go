// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/openrouter"
	"github.com/inspr-at/paimos/internal/piprobe"
)

// probeCapture keeps at most max bytes of a probe stream. It deliberately has
// no embedded buffer, so io.Copy cannot promote a ReadFrom past the cap; any
// byte beyond it sets overflow and stops the copy.
type probeCapture struct {
	buf      []byte
	max      int
	overflow bool
}

func (c *probeCapture) Write(p []byte) (int, error) {
	if c.overflow || len(c.buf)+len(p) > c.max {
		c.overflow = true
		return 0, errors.New("probe output exceeds bound")
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

// probeRun runs a vendor status command. A non-zero exit still returns its
// bounded output, so a clear "signed out" answer can be told apart from a probe
// that could not run (missing binary, timeout, oversized or ambiguous output).
func probeRun(ctx context.Context, path string, env []string, args ...string) ([]byte, int, error) {
	path, err := pinnedExecutable(path)
	if err != nil {
		return nil, 0, err
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(op, path, args...)
	if env != nil {
		cmd.Env = env
	}
	stdout, stderr := &probeCapture{max: probeOutputMax}, &probeCapture{max: probeOutputMax}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	code := 0
	err = cmd.Run()
	// Oversized output is unavailable whatever the exit: its kept prefix is not
	// the whole answer and must never be parsed.
	if stdout.overflow || stderr.overflow {
		return nil, 0, errors.New("account probe output exceeds bound")
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || op.Err() != nil || exit.ExitCode() < 0 {
			// AEON-341: a launcher that cannot run or never answers failed to
			// start; callers without start semantics still read it as unavailable.
			return nil, 0, harnesslaunch.ErrStart
		}
		code = exit.ExitCode()
		if code == 126 || code == 127 {
			return nil, 0, harnesslaunch.ErrStart
		}
	}
	if len(stdout.buf) != 0 && len(stderr.buf) != 0 {
		return nil, 0, errors.New("account probe output ambiguous")
	}
	if len(stdout.buf) != 0 {
		return stdout.buf, code, nil
	}
	return stderr.buf, code, nil
}

// probeOutputMax caps each probe stream; vendor status answers are far smaller.
const probeOutputMax = 4096

// decodeProbeJSON decodes one JSON document into v, rejecting any object that
// repeats a key. Keys are compared after unescaping and case-insensitively,
// because encoding/json matches field names that way: "loggedIn" twice, or
// "loggedIn" and "LOGGEDIN", would otherwise let the last one silently win.
func decodeProbeJSON(raw []byte, v any) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func rejectDuplicateKeys(raw []byte) error {
	type frame struct {
		object  bool
		wantKey bool
		keys    []string
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			stack = stack[:len(stack)-1]
			if len(stack) > 0 && stack[len(stack)-1].object {
				stack[len(stack)-1].wantKey = true
			}
			continue
		}
		if top != nil && top.object && top.wantKey {
			key, _ := tok.(string)
			for _, seen := range top.keys {
				if strings.EqualFold(seen, key) {
					return errors.New("duplicate JSON key")
				}
			}
			top.keys = append(top.keys, key)
			top.wantKey = false
			continue
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{object: true, wantKey: true})
		case json.Delim('['):
			stack = append(stack, &frame{})
		default:
			if top != nil && top.object {
				top.wantKey = true
			}
		}
	}
}

func probeCommand(ctx context.Context, path string, env []string, args ...string) ([]byte, error) {
	out, code, err := probeRun(ctx, path, env, args...)
	if err != nil || code != 0 {
		if errors.Is(err, harnesslaunch.ErrStart) {
			return nil, err
		}
		return nil, errors.New("account probe unavailable")
	}
	return out, nil
}

// launcherReady checks the same service environment before classifying login
// (AEON-341).
func launcherReady(ctx context.Context, path, node string, env []string) error {
	if err := harnesslaunch.Validate(path, node); err != nil {
		return err
	}
	if _, err := probeCommand(ctx, path, env, "--version"); err != nil {
		return harnesslaunch.ErrStart
	}
	return nil
}

var (
	probeOK          = ProbeStatus{OK: true}
	probeAuthFailed  = ProbeStatus{Failure: ProbeAuthFailed}
	probeUnavailable = ProbeStatus{Failure: ProbeUnavailable}
)

func (a *CodexAdapter) Probe(ctx context.Context, key string) bool { return a.ProbeStatus(ctx, key).OK }

// ProbeStatus reads `codex login status`. Only its explicit "Not logged in"
// answer is an authentication failure.
func (a *CodexAdapter) ProbeStatus(ctx context.Context, key string) ProbeStatus {
	status, _ := a.ProbeHarness(ctx, key)
	return status
}

// ProbeHarness is ProbeStatus plus a start failure (AEON-341): the launcher and
// its pinned Node are checked in the service environment before sign-in is
// classified.
func (a *CodexAdapter) ProbeHarness(ctx context.Context, key string) (ProbeStatus, error) {
	home, err := localHome(a.Homes, key)
	if err != nil {
		return probeUnavailable, harnesslaunch.ErrStart
	}
	env := harnesslaunch.Environment(withEnv("CODEX_HOME", home), a.Nodes[key].Path)
	if err := launcherReady(ctx, a.Path, a.Nodes[key].Path, env); err != nil {
		return probeUnavailable, err
	}
	if strings.TrimSpace(a.Emails[key]) == "" {
		return probeUnavailable, nil
	}
	raw, code, err := probeRun(ctx, a.Path, env, "login", "status")
	if errors.Is(err, harnesslaunch.ErrStart) {
		return probeUnavailable, err
	}
	if err != nil {
		return probeUnavailable, nil
	}
	return codexStatus(raw, code), nil
}

// codexStatus: the whole output must be exactly one recognized answer; extra
// lines or a contradiction are unavailable, never a sign-out.
func codexStatus(raw []byte, code int) ProbeStatus {
	switch strings.TrimSpace(string(raw)) {
	case "Logged in using ChatGPT":
		if code == 0 {
			return probeOK
		}
	case "Not logged in":
		return probeAuthFailed
	}
	return probeUnavailable
}

func (a *PiAdapter) Probe(ctx context.Context, key string) bool {
	available, _ := a.ProbeStatus(ctx, key)
	return available
}

// ProbeStatus distinguishes startup failures from missing provider configuration.
// Polls reuse results for one minute; a launch always requests a fresh check.
func (a *PiAdapter) ProbeStatus(ctx context.Context, key string) (bool, error) {
	return a.probe(ctx, key, false)
}

func (a *PiAdapter) probe(ctx context.Context, key string, fresh bool) (bool, error) {
	a.probeMu.Lock()
	if a.probeLocks == nil {
		a.probeLocks = map[string]*sync.Mutex{}
	}
	lock := a.probeLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		a.probeLocks[key] = lock
	}
	a.probeMu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	// Report permission errors distinctly, before localHome rejects the profile.
	if info, err := os.Stat(a.Homes[key]); err == nil && info.IsDir() && info.Mode().Perm()&0077 != 0 {
		return false, piprobe.ErrPrivateProfile
	}
	home, err := localHome(a.Homes, key)
	if err != nil {
		return false, piprobe.ErrStart
	}
	if _, err := pinnedExecutable(a.Path); err != nil {
		return false, piprobe.ErrStart
	}
	node := a.Nodes[key]
	if err := harnesslaunch.Validate(a.Path, node.Path); err != nil {
		return false, err
	}
	if node.Path != "" {
		if _, err := pinnedExecutable(node.Path); err != nil {
			return false, piprobe.ErrStart
		}
	}
	if a.Providers != nil {
		expected := a.Providers[key]
		if !piprobe.ValidProvider(expected) {
			return false, piprobe.ErrStart
		}
		a.probeMu.Lock()
		cached, ok := a.probes[key]
		a.probeMu.Unlock()
		if !fresh && ok && time.Now().Before(cached.expires) && cached.path == a.Path && cached.home == home && cached.provider == expected && cached.node == node {
			return cached.available, cached.err
		}
		var credits *openrouter.Credits
		provider := expected
		var err error
		if expected == "openrouter" {
			// No prompt or completion. The key stays inside the local profile reader.
			err = launcherReady(ctx, a.Path, node.Path, piprobe.Environment(home, node.Path))
			if err == nil {
				credits, err = agentsetup.OpenRouterCredits(ctx, home, a.OpenRouter)
			}
		} else {
			provider, err = piprobe.Provider(ctx, a.Path, home, expected, node.Path)
		}
		available := err == nil && provider == expected
		if errors.Is(err, piprobe.ErrProviderUnavailable) {
			err = nil
		}
		a.probeMu.Lock()
		if a.probes == nil {
			a.probes = map[string]piProbeResult{}
		}
		a.probes[key] = piProbeResult{credits: credits, path: a.Path, home: home, provider: expected, node: node, expires: time.Now().Add(time.Minute), available: available, err: err}
		a.probeMu.Unlock()
		return available, err
	}
	return true, nil
}

func (a *CursorAdapter) Probe(ctx context.Context, key string) bool {
	return a.ProbeStatus(ctx, key).OK
}

// ProbeStatus reads `cursor-agent status --format json`. A parsed answer that
// says unauthenticated, or authenticated as another user, is an authentication
// failure; anything unreadable is unavailable.
func (a *CursorAdapter) ProbeStatus(ctx context.Context, key string) ProbeStatus {
	status, _ := a.ProbeHarness(ctx, key)
	return status
}

// ProbeHarness is ProbeStatus plus a start failure (AEON-341).
func (a *CursorAdapter) ProbeHarness(ctx context.Context, key string) (ProbeStatus, error) {
	environment, err := a.launchEnvironment(key)
	if err != nil {
		return probeUnavailable, harnesslaunch.ErrStart
	}
	if err := launcherReady(ctx, a.Path, a.Nodes[key].Path, environment); err != nil {
		return probeUnavailable, err
	}
	expected := a.Identities[key]
	if expected == "" {
		return probeUnavailable, nil
	}
	raw, code, err := probeRun(ctx, a.Path, environment, "status", "--format", "json")
	if errors.Is(err, harnesslaunch.ErrStart) {
		return probeUnavailable, err
	}
	if err != nil {
		return probeUnavailable, nil
	}
	return cursorStatus(raw, code, expected), nil
}

// launchEnvironment is the account's isolated environment (AEON-298), or the
// service environment for legacy enrollments, with the pinned Node first on
// PATH (AEON-341).
func (a *CursorAdapter) launchEnvironment(key string) ([]string, error) {
	env, err := a.accountEnvironment(key)
	if err != nil {
		return nil, err
	}
	node := a.Nodes[key].Path
	if env == nil {
		return harnesslaunch.Environment(os.Environ(), node), nil
	}
	if node != "" {
		for i, entry := range env {
			if rest, ok := strings.CutPrefix(entry, "PATH="); ok {
				env[i] = "PATH=" + filepath.Dir(node) + string(os.PathListSeparator) + rest
			}
		}
	}
	return env, nil
}

func cursorStatus(raw []byte, code int, expected string) ProbeStatus {
	var status struct {
		Status          *string `json:"status"`
		IsAuthenticated *bool   `json:"isAuthenticated"`
		UserInfo        *struct {
			UserID json.RawMessage `json:"userId"`
		} `json:"userInfo"`
	}
	if decodeProbeJSON(raw, &status) != nil || status.Status == nil || status.IsAuthenticated == nil {
		return probeUnavailable
	}
	// Only an explicit, consistent answer counts: signed out, or signed in as
	// another user. Unknown status words, contradictions and a missing or null
	// identity are unavailable.
	if !*status.IsAuthenticated {
		if cursorSignedOut[*status.Status] {
			return probeAuthFailed
		}
		return probeUnavailable
	}
	if *status.Status != "authenticated" || status.UserInfo == nil {
		return probeUnavailable
	}
	id, ok := cursorIdentity(status.UserInfo.UserID)
	want := strings.TrimSpace(expected)
	if !ok || want == "" {
		return probeUnavailable
	}
	if id != want {
		return probeAuthFailed
	}
	if code != 0 {
		return probeUnavailable
	}
	return probeOK
}

// cursorIdentity decodes a Cursor userId: a non-empty string or an integer,
// normalized to its text ("42" and 42 are the same account). Anything else
// (objects, arrays, booleans, null, blank or fractional values) is not an identity.
func cursorIdentity(raw json.RawMessage) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return "", false
	}
	switch id := v.(type) {
	case string:
		id = strings.TrimSpace(id)
		return id, id != ""
	case json.Number:
		n, err := strconv.ParseInt(id.String(), 10, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatInt(n, 10), true
	}
	return "", false
}

// cursorSignedOut lists the status words cursor-agent uses for a signed-out
// session; any other word with isAuthenticated false is not a confirmed sign-out.
var cursorSignedOut = map[string]bool{"unauthenticated": true, "not_authenticated": true, "logged_out": true, "signed_out": true}

func (a *ClaudeAdapter) Probe(ctx context.Context, key string) bool {
	return a.ProbeStatus(ctx, key).OK
}

// ProbeStatus reads `claude auth status --json`. loggedIn false, an API key
// login, or a different email is an authentication failure for this account.
// A local dependency failure (AEON-342) is unavailable, never a sign-out.
func (a *ClaudeAdapter) ProbeStatus(ctx context.Context, key string) ProbeStatus {
	status, _ := a.ProbeAccountStatus(ctx, key)
	return status
}

// ProbeAccount separates local dependency failures from vendor sign-in state.
// Dependency diagnostics are value-free; vendor output is never surfaced.
func (a *ClaudeAdapter) ProbeAccount(ctx context.Context, key string) (bool, error) {
	status, err := a.ProbeAccountStatus(ctx, key)
	return status.OK, err
}

// ProbeAccountStatus is ProbeAccount with the probe's cause (AEON-298).
func (a *ClaudeAdapter) ProbeAccountStatus(ctx context.Context, key string) (ProbeStatus, error) {
	resolved, err := a.resolved("")
	if err != nil {
		return probeUnavailable, err
	}
	return resolved.probeResolved(ctx, key), nil
}

func (a *ClaudeAdapter) probeResolved(ctx context.Context, key string) ProbeStatus {
	home, err := localHome(a.Homes, key)
	if err != nil {
		return probeUnavailable
	}
	raw, code, err := probeRun(ctx, a.ClaudePath, claudeEnvironment(home, a.NodePath, a.ClaudePath), "auth", "status", "--json")
	if err != nil {
		return probeUnavailable
	}
	var status struct {
		AccountID  string `json:"accountId"`
		LoggedIn   *bool  `json:"loggedIn"`
		Email      string `json:"email"`
		AuthMethod string `json:"authMethod"`
	}
	if decodeProbeJSON(raw, &status) != nil || status.LoggedIn == nil {
		return probeUnavailable
	}
	if !*status.LoggedIn {
		return probeAuthFailed
	}
	if a.Emails != nil {
		if a.Emails[key] == "" {
			return probeUnavailable
		}
		// An API-key login or a different email is explicitly another account;
		// a login without an email says nothing about which account it is.
		if status.AuthMethod == "api_key" {
			return probeAuthFailed
		}
		if strings.TrimSpace(status.Email) == "" {
			return probeUnavailable
		}
		if !strings.EqualFold(status.Email, a.Emails[key]) {
			return probeAuthFailed
		}
	}
	if code != 0 {
		return probeUnavailable
	}
	if status.AccountID != "" {
		a.quotaIdentities().Store(key, status.AccountID)
	}
	return probeOK
}

func (a *GrokAdapter) Probe(ctx context.Context, key string) bool {
	b, ok := a.Bindings[key]
	if !ok {
		return false
	}
	return a.probeNative(ctx, b)
}

var _ io.Writer = (*probeCapture)(nil)
