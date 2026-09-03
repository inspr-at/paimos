// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const workspaceProbeLimit = 16 << 10

type workspaceInspector func(context.Context, string, string) (WorkspaceProvenance, error)

func inspectWorkspace(ctx context.Context, canonicalPath, mode string) (WorkspaceProvenance, error) {
	base := WorkspaceProvenance{CanonicalPath: canonicalPath, Kind: WorkspaceDirectory, Mode: mode}
	base.Identity = workspaceIdentity(canonicalPath, canonicalPath)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		tracked, markerErr := workspaceHasGitMarker(canonicalPath)
		if markerErr != nil {
			return WorkspaceProvenance{}, markerErr
		}
		if tracked {
			return WorkspaceProvenance{}, errors.New("workspace Git executable is unavailable")
		}
		return base, nil
	}
	gitPath, err = filepath.Abs(gitPath)
	if err != nil {
		return WorkspaceProvenance{}, errors.New("workspace Git executable is invalid")
	}
	gitPath, err = filepath.EvalSymlinks(gitPath)
	if err != nil {
		return WorkspaceProvenance{}, errors.New("workspace Git executable cannot be pinned")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := runWorkspaceGit(probeCtx, gitPath, canonicalPath,
		"rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		tracked, markerErr := workspaceHasGitMarker(canonicalPath)
		if markerErr != nil {
			return WorkspaceProvenance{}, markerErr
		}
		if tracked {
			return WorkspaceProvenance{}, errors.New("workspace Git provenance is unavailable")
		}
		return base, nil
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) != 3 {
		return WorkspaceProvenance{}, errors.New("workspace Git identity is malformed")
	}
	top, gitDir, commonDir := lines[0], lines[1], lines[2]
	for _, value := range []string{top, gitDir, commonDir} {
		if !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
			return WorkspaceProvenance{}, errors.New("workspace Git identity is malformed")
		}
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil {
		return WorkspaceProvenance{}, errors.New("workspace Git top-level is unavailable")
	}
	gitDir, err = filepath.EvalSymlinks(gitDir)
	if err != nil {
		return WorkspaceProvenance{}, errors.New("workspace Git directory is unavailable")
	}
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		return WorkspaceProvenance{}, errors.New("workspace Git common directory is unavailable")
	}
	branchRaw, detached, branchErr := runWorkspaceGitBranch(probeCtx, gitPath, canonicalPath)
	if branchErr != nil {
		return WorkspaceProvenance{}, branchErr
	}
	branch := "detached"
	if !detached {
		branch = strings.TrimSpace(string(branchRaw))
	}
	if branch == "" || len(branch) > 512 || strings.ContainsAny(branch, "\x00\r\n") {
		return WorkspaceProvenance{}, errors.New("workspace Git branch is malformed")
	}
	kind := WorkspaceWorktree
	if gitDir == commonDir {
		kind = WorkspacePrimary
	}
	return WorkspaceProvenance{
		CanonicalPath: canonicalPath,
		GitTopLevel:   top,
		GitBranch:     branch,
		Identity:      workspaceIdentity(top, gitDir),
		Kind:          kind,
		Mode:          mode,
	}, nil
}

func runWorkspaceGitBranch(ctx context.Context, gitPath, workspace string) ([]byte, bool, error) {
	command := exec.CommandContext(ctx, gitPath, "-C", workspace, "symbolic-ref", "--quiet", "--short", "HEAD") // #nosec G204 -- pinned executable and fixed internal argv.
	command.Env = workspaceGitEnvironment(os.Environ())
	var output boundedWorkspaceBuffer
	command.Stdout = &output
	command.Stderr = &boundedWorkspaceBuffer{}
	err := command.Run()
	if output.overflow {
		return nil, false, errors.New("workspace Git branch exceeded its bound")
	}
	if err == nil {
		return output.Bytes(), false, nil
	}
	var exitErr *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil, true, nil
	}
	return nil, false, errors.New("workspace Git branch is unavailable")
}

// workspaceHasGitMarker distinguishes an ordinary non-Git directory from a
// repository whose Git probe failed. Silently relabelling the latter as a
// directory would give nested paths different identities and bypass exclusive
// ownership. Lstat avoids following a hostile marker symlink.
func workspaceHasGitMarker(path string) (bool, error) {
	for {
		_, err := os.Lstat(filepath.Join(path, ".git"))
		switch {
		case err == nil:
			return true, nil
		case !errors.Is(err, os.ErrNotExist):
			return false, errors.New("workspace Git marker cannot be inspected")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false, nil
		}
		path = parent
	}
}

func workspaceIdentity(top, gitDir string) string {
	digest := sha256.Sum256([]byte("paimos:agentd-workspace:v1\x00" + top + "\x00" + gitDir))
	return hex.EncodeToString(digest[:])
}

func runWorkspaceGit(ctx context.Context, gitPath, workspace string, args ...string) ([]byte, error) {
	argv := append([]string{"-C", workspace}, args...)
	command := exec.CommandContext(ctx, gitPath, argv...) // #nosec G204 -- pinned executable and fixed internal argv.
	command.Env = workspaceGitEnvironment(os.Environ())
	var output boundedWorkspaceBuffer
	command.Stdout = &output
	command.Stderr = &boundedWorkspaceBuffer{}
	if err := command.Run(); err != nil || output.overflow {
		return nil, errors.New("workspace Git probe failed")
	}
	return output.Bytes(), nil
}

func workspaceGitEnvironment(base []string) []string {
	out := make([]string, 0, len(base)+4)
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "GIT_") || key == "LANG" || key == "LC_ALL" {
			continue
		}
		out = append(out, value)
	}
	return append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

type boundedWorkspaceBuffer struct {
	bytes.Buffer
	overflow bool
}

func (buffer *boundedWorkspaceBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := workspaceProbeLimit - buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		buffer.overflow = true
	}
	_, _ = buffer.Buffer.Write(value)
	return original, nil
}
