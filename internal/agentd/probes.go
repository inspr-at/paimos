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
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/piprobe"
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

func probeCommand(ctx context.Context, path string, env []string, args ...string) ([]byte, error) {
	path, err := pinnedExecutable(path)
	if err != nil {
		return nil, err
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
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if op.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() == 126 || exit.ExitCode() == 127 {
			return nil, harnesslaunch.ErrStart
		}
		return nil, errors.New("account probe unavailable")
	}
	if stdout.Len() != 0 && stderr.Len() != 0 {
		return nil, errors.New("account probe output ambiguous")
	}
	if stdout.Len() != 0 {
		return stdout.Bytes(), nil
	}
	return stderr.Bytes(), nil
}

// launcherReady checks the same service environment before classifying login.
func launcherReady(ctx context.Context, path, node string, env []string) error {
	if err := harnesslaunch.Validate(path, node); err != nil {
		return err
	}
	if _, err := probeCommand(ctx, path, env, "--version"); err != nil {
		return harnesslaunch.ErrStart
	}
	return nil
}

func (a *CodexAdapter) Probe(ctx context.Context, key string) bool {
	available, _ := a.ProbeStatus(ctx, key)
	return available
}
func (a *CodexAdapter) ProbeStatus(ctx context.Context, key string) (bool, error) {
	home, err := localHome(a.Homes, key)
	if err != nil {
		return false, harnesslaunch.ErrStart
	}
	env := harnesslaunch.Environment(withEnv("CODEX_HOME", home), a.Nodes[key].Path)
	if err := launcherReady(ctx, a.Path, a.Nodes[key].Path, env); err != nil {
		return false, err
	}
	if strings.TrimSpace(a.Emails[key]) == "" {
		return false, nil
	}
	raw, err := probeCommand(ctx, a.Path, env, "login", "status")
	if errors.Is(err, harnesslaunch.ErrStart) {
		return false, err
	}
	return err == nil && strings.TrimSpace(string(raw)) == "Logged in using ChatGPT", nil
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
		provider, err := piprobe.Provider(ctx, a.Path, home, expected, node.Path)
		available := err == nil && provider == expected
		if errors.Is(err, piprobe.ErrProviderUnavailable) {
			err = nil
		}
		a.probeMu.Lock()
		if a.probes == nil {
			a.probes = map[string]piProbeResult{}
		}
		a.probes[key] = piProbeResult{path: a.Path, home: home, provider: expected, node: node, expires: time.Now().Add(time.Minute), available: available, err: err}
		a.probeMu.Unlock()
		return available, err
	}
	return true, nil
}

func (a *CursorAdapter) Probe(ctx context.Context, key string) bool {
	available, _ := a.ProbeStatus(ctx, key)
	return available
}
func (a *CursorAdapter) ProbeStatus(ctx context.Context, key string) (bool, error) {
	env := harnesslaunch.Environment(os.Environ(), a.Nodes[key].Path)
	if err := launcherReady(ctx, a.Path, a.Nodes[key].Path, env); err != nil {
		return false, err
	}
	expected := a.Identities[key]
	if expected == "" {
		return false, nil
	}
	raw, err := probeCommand(ctx, a.Path, env, "status", "--format", "json")
	if errors.Is(err, harnesslaunch.ErrStart) {
		return false, err
	}
	if err != nil {
		return false, nil
	}
	var status struct {
		Status          string `json:"status"`
		IsAuthenticated bool   `json:"isAuthenticated"`
		UserInfo        *struct {
			UserID json.RawMessage `json:"userId"`
		} `json:"userInfo"`
	}
	return json.Unmarshal(raw, &status) == nil && status.Status == "authenticated" && status.IsAuthenticated && status.UserInfo != nil && strings.Trim(string(status.UserInfo.UserID), "\"") == expected, nil
}

func (a *ClaudeAdapter) Probe(ctx context.Context, key string) bool {
	available, _ := a.ProbeAccount(ctx, key)
	return available
}

// ProbeAccount separates local dependency failures from vendor sign-in state.
// Dependency diagnostics are value-free; vendor output is never surfaced.
func (a *ClaudeAdapter) ProbeAccount(ctx context.Context, key string) (bool, error) {
	resolved, err := a.resolved("")
	if err != nil {
		return false, err
	}
	return resolved.probeResolved(ctx, key), nil
}

func (a *ClaudeAdapter) probeResolved(ctx context.Context, key string) bool {
	home, err := localHome(a.Homes, key)
	if err != nil {
		return false
	}
	raw, err := probeCommand(ctx, a.ClaudePath, claudeEnvironment(home, a.NodePath, a.ClaudePath), "auth", "status", "--json")
	if err != nil {
		return false
	}
	var status struct {
		LoggedIn   bool   `json:"loggedIn"`
		Email      string `json:"email"`
		AuthMethod string `json:"authMethod"`
	}
	return json.Unmarshal(raw, &status) == nil && status.LoggedIn && (a.Emails == nil || status.AuthMethod != "api_key" && strings.EqualFold(status.Email, a.Emails[key]) && a.Emails[key] != "")
}

func (a *GrokAdapter) Probe(ctx context.Context, key string) bool {
	b, ok := a.Bindings[key]
	if !ok {
		return false
	}
	return a.probeNative(ctx, b)
}

var _ io.Writer = (*boundedProbe)(nil)
