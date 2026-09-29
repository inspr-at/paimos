// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
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
			return nil, 0, errors.New("account probe unavailable")
		}
		code = exit.ExitCode()
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
		return nil, errors.New("account probe unavailable")
	}
	return out, nil
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
	if strings.TrimSpace(a.Emails[key]) == "" {
		return probeUnavailable
	}
	home, err := localHome(a.Homes, key)
	if err != nil {
		return probeUnavailable
	}
	raw, code, err := probeRun(ctx, a.Path, withEnv("CODEX_HOME", home), "login", "status")
	if err != nil {
		return probeUnavailable
	}
	// The whole output must be exactly one recognized answer; extra lines or a
	// contradiction are unavailable, never a sign-out.
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

func (a *PiAdapter) Probe(_ context.Context, key string) bool {
	if _, err := localHome(a.Homes, key); err != nil {
		return false
	}
	_, err := pinnedExecutable(a.Path)
	return err == nil
}

func (a *CursorAdapter) Probe(ctx context.Context, key string) bool {
	return a.ProbeStatus(ctx, key).OK
}

// ProbeStatus reads `cursor-agent status --format json`. A parsed answer that
// says unauthenticated, or authenticated as another user, is an authentication
// failure; anything unreadable is unavailable.
func (a *CursorAdapter) ProbeStatus(ctx context.Context, key string) ProbeStatus {
	expected := a.Identities[key]
	if expected == "" {
		return probeUnavailable
	}
	environment, err := a.accountEnvironment(key)
	if err != nil {
		return probeUnavailable
	}
	raw, code, err := probeRun(ctx, a.Path, environment, "status", "--format", "json")
	if err != nil {
		return probeUnavailable
	}
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
func (a *ClaudeAdapter) ProbeStatus(ctx context.Context, key string) ProbeStatus {
	home, err := localHome(a.Homes, key)
	if err != nil {
		return probeUnavailable
	}
	raw, code, err := probeRun(ctx, a.ClaudePath, claudeEnvironment(home, a.NodePath, a.ClaudePath), "auth", "status", "--json")
	if err != nil {
		return probeUnavailable
	}
	var status struct {
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
