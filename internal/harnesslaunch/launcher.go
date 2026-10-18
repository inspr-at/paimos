// SPDX-License-Identifier: AGPL-3.0-only

// Package harnesslaunch pins npm launcher interpreters independently of shell PATH.
package harnesslaunch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrStart = errors.New("harness failed to start; restore its pinned installation and Node interpreter, then resume setup")

// ServicePath is launchd's default PATH, also a conservative systemd baseline.
// Never include an empty component: it would search the run's workspace.
const ServicePath = "/usr/bin:/bin:/usr/sbin:/sbin"

// Node is a private interpreter binding, never part of a pairing request.
type Node struct {
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

// NeedsNode inspects only the bounded entrypoint header. Unknown env launchers
// must not silently select an unpinned interpreter from the service PATH.
func NeedsNode(path string) (_ bool, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("inspect harness launcher %s: %w", path, resultErr)
		}
	}()
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrStart, err)
	}
	if !filepath.IsAbs(path) || physical != path {
		return false, ErrStart
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrStart, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return false, ErrStart
	}
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrStart, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 256))
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrStart, err)
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

// Environment keeps the caller's credential policy and replaces shell lookup
// and Node injection settings with the reviewed interpreter and service PATH.
func Environment(base []string, nodePath string) []string {
	result := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if key != "PATH" && key != "NODE_OPTIONS" && key != "NODE_PATH" {
			result = append(result, entry)
		}
	}
	path := ServicePath
	if nodePath != "" {
		path = filepath.Dir(nodePath) + string(os.PathListSeparator) + path
	}
	return append(result, "PATH="+path)
}

// Validate refuses an env-node launcher without its physical interpreter pin.
// Setup additionally checks ownership, writable ancestors and workspace scope.
func Validate(path, nodePath string) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("validate harness launcher %s with Node pin %s: %w", path, nodePath, resultErr)
		}
	}()
	needed, err := NeedsNode(path)
	if err != nil {
		return err
	}
	if needed && nodePath == "" {
		return ErrStart
	}
	if nodePath == "" {
		return nil
	}
	physical, err := filepath.EvalSymlinks(nodePath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStart, err)
	}
	if !filepath.IsAbs(nodePath) || physical != nodePath || filepath.Base(nodePath) != "node" {
		return ErrStart
	}
	info, err := os.Stat(nodePath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStart, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return ErrStart
	}
	return nil
}
