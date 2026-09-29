// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

type boundedProbe struct {
	bytes.Buffer
	max int
}

func (b *boundedProbe) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("probe output exceeds bound")
	}
	return b.Buffer.Write(p)
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
	stdout, stderr := &boundedProbe{max: 4096}, &boundedProbe{max: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || op.Err() != nil || exit.ExitCode() < 0 {
			return nil, 0, errors.New("account probe unavailable")
		}
		code = exit.ExitCode()
	}
	if stdout.Len() != 0 && stderr.Len() != 0 {
		return nil, 0, errors.New("account probe output ambiguous")
	}
	if stdout.Len() != 0 {
		return stdout.Bytes(), code, nil
	}
	return stderr.Bytes(), code, nil
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
	status := strings.TrimSpace(string(raw))
	if code == 0 && status == "Logged in using ChatGPT" {
		return probeOK
	}
	if strings.HasPrefix(strings.ToLower(status), "not logged in") {
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
	raw, code, err := probeRun(ctx, a.Path, nil, "status", "--format", "json")
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
	if json.Unmarshal(raw, &status) != nil || status.Status == nil || status.IsAuthenticated == nil {
		return probeUnavailable
	}
	if *status.Status != "authenticated" || !*status.IsAuthenticated {
		return probeAuthFailed
	}
	if status.UserInfo == nil || len(status.UserInfo.UserID) == 0 {
		return probeUnavailable
	}
	if strings.Trim(string(status.UserInfo.UserID), "\"") != expected {
		return probeAuthFailed
	}
	if code != 0 {
		return probeUnavailable
	}
	return probeOK
}

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
	if json.Unmarshal(raw, &status) != nil || status.LoggedIn == nil {
		return probeUnavailable
	}
	if !*status.LoggedIn {
		return probeAuthFailed
	}
	if a.Emails != nil {
		if a.Emails[key] == "" {
			return probeUnavailable
		}
		if status.AuthMethod == "api_key" || !strings.EqualFold(status.Email, a.Emails[key]) {
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

var _ io.Writer = (*boundedProbe)(nil)
