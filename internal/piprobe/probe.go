// SPDX-License-Identifier: AGPL-3.0-only

// Package piprobe reads only pi's public, prompt-free RPC projection. Pi owns
// authentication; Aeon never opens its credential or configuration files.
package piprobe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var providerID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

var ErrPrivateProfile = errors.New("pi local profile must be a private directory; review its permissions, then resume setup")
var ErrStart = errors.New("pi harness failed to start; restore the pinned pi and Node installation, then resume setup")
var ErrProviderUnavailable = errors.New("pi provider configuration unavailable; use pi /login and /model normally, then resume setup")

// Node is a private interpreter binding, never part of a pairing request.
type Node struct {
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

// NeedsNode inspects only the bounded entrypoint header. Unknown env launchers
// must not silently select an unpinned interpreter from the service PATH.
func NeedsNode(path string) (bool, error) {
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || physical != path {
		return false, ErrStart
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return false, ErrStart
	}
	f, err := os.Open(path)
	if err != nil {
		return false, ErrStart
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 256))
	if err != nil {
		return false, ErrStart
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	if !strings.HasPrefix(line, "#!") {
		return false, nil
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 || filepath.Base(fields[0]) != "env" {
		return false, nil
	}
	if len(fields) == 2 && fields[1] == "node" || len(fields) == 3 && fields[1] == "-S" && fields[2] == "node" {
		return true, nil
	}
	return false, ErrStart
}

func ValidProvider(provider string) bool { return providerID.MatchString(provider) }

// Environment excludes inherited provider keys and runtime injection variables.
// The daemon uses the same local profile that was inspected during pairing.
func Environment(home, nodePath string) []string {
	path := os.Getenv("PATH")
	if nodePath != "" {
		path = filepath.Dir(nodePath) + string(os.PathListSeparator) + path
	}
	return []string{"PATH=" + path, "HOME=" + os.Getenv("HOME"), "PI_CODING_AGENT_DIR=" + home, "PI_OFFLINE=1", "PI_SKIP_VERSION_CHECK=1"}
}

// Provider checks pi's configured authentication, not remote token validity or
// a person's identity. With no explicit provider, the selected model must also
// appear in get_available_models. Only the provider ID leaves this function.
// RPC reference: https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/rpc.md
func Provider(ctx context.Context, path, home, expected, nodePath string) (string, error) {
	unavailable := ErrProviderUnavailable
	if expected != "" && !ValidProvider(expected) {
		return "", unavailable
	}
	paths := []string{path, home}
	if nodePath != "" {
		paths = append(paths, nodePath)
	}
	for _, p := range paths {
		physical, err := filepath.EvalSymlinks(p)
		if err != nil || !filepath.IsAbs(p) || physical != p {
			return "", ErrStart
		}
	}
	for _, p := range []string{path, nodePath} {
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return "", ErrStart
		}
	}
	needsNode, err := NeedsNode(path)
	if err != nil || needsNode && (nodePath == "" || filepath.Base(nodePath) != "node") {
		return "", ErrStart
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return "", ErrStart
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", ErrPrivateProfile
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--mode", "rpc", "--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes")
	// No project configuration or project extensions participate in discovery.
	cmd.Dir = "/"
	cmd.Env = Environment(home, nodePath)
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		return "", ErrStart
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return "", ErrStart
	}
	if err = cmd.Start(); err != nil {
		return "", ErrStart
	}
	// A descendant retaining stdout must not defeat the probe deadline.
	stopClose := context.AfterFunc(ctx, func() { _ = output.Close() })
	defer stopClose()
	defer func() { input.Close(); cancel(); _ = cmd.Wait() }()
	encoder := json.NewEncoder(input)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	rpc := func(command string, result any) bool {
		if encoder.Encode(map[string]string{"id": command, "type": command}) != nil {
			return false
		}
		for i := 0; i < 32 && scanner.Scan(); i++ {
			var frame struct {
				ID, Type, Command string
				Success           bool
				Data              json.RawMessage
			}
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				return false
			}
			if frame.ID == command {
				return frame.Type == "response" && frame.Command == command && frame.Success && json.Unmarshal(frame.Data, result) == nil
			}
		}
		return false
	}
	type model struct{ Provider, ID string }
	var state struct{ Model *model }
	var available struct{ Models []model }
	if !rpc("get_state", &state) || !rpc("get_available_models", &available) {
		return "", ErrStart
	}
	if expected == "" && (state.Model == nil || !ValidProvider(state.Model.Provider) || state.Model.ID == "") {
		return "", unavailable
	}
	for _, m := range available.Models {
		if !ValidProvider(m.Provider) || m.ID == "" {
			continue
		}
		if expected != "" && m.Provider == expected || expected == "" && m == *state.Model {
			return m.Provider, nil
		}
	}
	return "", unavailable
}
