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

func (a *CodexAdapter) Probe(ctx context.Context, key string) bool {
	if strings.TrimSpace(a.Emails[key]) == "" {
		return false
	}
	home, err := localHome(a.Homes, key)
	if err != nil {
		return false
	}
	raw, err := probeCommand(ctx, a.Path, withEnv("CODEX_HOME", home), "login", "status")
	if err != nil {
		return false
	}
	status := strings.TrimSpace(string(raw))
	return status == "Logged in using ChatGPT"
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
	defer a.probeMu.Unlock()
	home, err := localHome(a.Homes, key)
	if err != nil {
		return false, piprobe.ErrStart
	}
	if _, err := pinnedExecutable(a.Path); err != nil {
		return false, piprobe.ErrStart
	}
	node := a.Nodes[key]
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
		if cached, ok := a.probes[key]; !fresh && ok && time.Now().Before(cached.expires) && cached.path == a.Path && cached.home == home && cached.provider == expected && cached.node == node {
			return cached.available, cached.err
		}
		provider, err := piprobe.Provider(ctx, a.Path, home, expected, node.Path)
		available := err == nil && provider == expected
		if errors.Is(err, piprobe.ErrProviderUnavailable) {
			err = nil
		}
		if a.probes == nil {
			a.probes = map[string]piProbeResult{}
		}
		a.probes[key] = piProbeResult{path: a.Path, home: home, provider: expected, node: node, expires: time.Now().Add(time.Minute), available: available, err: err}
		return available, err
	}
	return true, nil
}

func (a *CursorAdapter) Probe(ctx context.Context, key string) bool {
	expected := a.Identities[key]
	if expected == "" {
		return false
	}
	raw, err := probeCommand(ctx, a.Path, nil, "status", "--format", "json")
	if err != nil {
		return false
	}
	var status struct {
		Status          string `json:"status"`
		IsAuthenticated bool   `json:"isAuthenticated"`
		UserInfo        *struct {
			UserID json.RawMessage `json:"userId"`
		} `json:"userInfo"`
	}
	return json.Unmarshal(raw, &status) == nil && status.Status == "authenticated" && status.IsAuthenticated && status.UserInfo != nil && strings.Trim(string(status.UserInfo.UserID), "\"") == expected
}

func (a *ClaudeAdapter) Probe(ctx context.Context, key string) bool {
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
