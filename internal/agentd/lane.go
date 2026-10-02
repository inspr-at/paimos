// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/laneprotocol"
)

// LaneRepository is a local, operator-approved mapping. Neither a ticket nor
// the server can provide its path, remote URL, shell command or base revision.
// The base must be a full local commit ID. Worktrees are retained, never pruned.
type LaneRepository struct{ Path, BaseRevision string }

// LaneExecutionAdapter is an explicit qualification boundary. Ordinary managed
// adapters do not implement this interface: tool/branch/network enforcement and
// bounded process startup/stop must be qualified before automatic lane launch.
// A true result promises that Start honors its deadline, returns promptly, and
// confines the process and its children to the approved execution boundaries.
type LaneExecutionAdapter interface{ LaneExecutionSupported() bool }
type laneClaimAPI interface {
	ClaimLane(context.Context, string, string, string, []string, string) (Run, error)
}

var laneUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var laneCommit = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (s *Supervisor) laneWorkspace(ctx context.Context, run Run, adapter Adapter, entry *owned) (string, string, error) {
	qualified, ok := adapter.(LaneExecutionAdapter)
	if !ok || !qualified.LaneExecutionSupported() {
		return "", "", errors.New("lane adapter boundaries are not qualified")
	}
	if _, ok := s.api.(laneClaimAPI); !ok {
		return "", "", errors.New("lane claim protocol unavailable")
	}
	if !laneUUID.MatchString(run.ID) || !laneUUID.MatchString(run.LaneEnvelopeID) || !laneUUID.MatchString(run.LaneProjectID) {
		return "", "", ErrScope
	}
	binding, ok := s.laneRepositories[run.LaneProjectID]
	if !ok || !laneCommit.MatchString(binding.BaseRevision) {
		return "", "", errors.New("approved local lane project mapping required")
	}
	physical, err := filepath.EvalSymlinks(binding.Path)
	if err != nil || !filepath.IsAbs(binding.Path) || physical != binding.Path {
		return "", "", errors.New("lane repository must be a physical local path")
	}
	root := filepath.Join(filepath.Dir(s.journal.JournalPath()), "lane-workspaces")
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", "", err
	}
	physical, err = filepath.EvalSymlinks(root)
	if err != nil || physical != root {
		return "", "", errors.New("lane workspace root is not physical")
	}
	workspace := filepath.Join(root, run.ID)
	branch := "aeon/lane/" + run.ID
	if _, err = os.Lstat(workspace); err == nil {
		if entry == nil {
			return "", "", errors.New("lane workspace already exists; reconciliation required")
		}
		entry.mu.Lock()
		r := entry.record
		entry.mu.Unlock()
		if r.Workspace != workspace || r.LaneEnvelopeID != run.LaneEnvelopeID || r.Generation != s.generation || r.LaunchState != launchPrepared {
			return "", "", ErrNotOwned
		}
		actual, err := exec.CommandContext(ctx, "git", "-C", workspace, "branch", "--show-current").Output()
		if err != nil || strings.TrimSpace(string(actual)) != branch {
			return "", "", errors.New("lane workspace branch changed")
		}
		return workspace, branch, nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Separate argument vector; no shell, remote access, reset, checkout reuse,
	// force, or deletion. Disable checkout hooks and submodule recursion.
	cmd := exec.CommandContext(bounded, "git", "-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false", "-C", binding.Path, "worktree", "add", "-b", branch, workspace, binding.BaseRevision)
	if err = cmd.Run(); err != nil {
		return "", "", errors.New("isolated lane worktree creation failed; retained for inspection")
	}
	return workspace, branch, nil
}

// grantDeadline starts from the monotonic instant BEFORE the claim request.
// Network latency, setup, replay and wall-clock corrections cannot add time.
func grantDeadline(g *laneprotocol.Grant, run Run, daemon, generation, workspace string, sent time.Time) (time.Time, error) {
	if g == nil || g.EnvelopeID != run.LaneEnvelopeID || g.RunID != run.ID || g.ProjectID != run.LaneProjectID || g.WorkspaceID != workspace || g.DaemonID != daemon || g.Generation != generation || g.RemainingMS <= g.StopAllowanceMS || g.RemainingMS > 36_000_000_000 || g.StopAllowanceMS != laneprotocol.StopAllowanceMS || g.ExpiresAt.IsZero() {
		return time.Time{}, errors.New("invalid claim-bound lane grant")
	}
	return sent.Add(time.Duration(g.RemainingMS-g.StopAllowanceMS) * time.Millisecond), nil
}
