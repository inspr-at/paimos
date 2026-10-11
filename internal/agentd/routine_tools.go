// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/routineguard/pathpolicy"
)

var ErrRoutineToolsUnavailable = errors.New("routine_tool_enforcement_unavailable")

// RoutineToolAdapter is a trusted implementation capability, independent of
// workspace allocation, spend admission and manual managed controls. It must
// fence inherited MCP, hooks, direct network effects and every child process;
// workspace code must not be able to rewrite that fence or its own gates.
// Qualification performs no login, vendor probe or network work. A server
// qualification ID alone cannot make an adapter implement this boundary.
type RoutineToolAdapter interface {
	RoutineToolsSupported(agentsetup.RoutineGrant) bool
}

// Separate from coding: a native capture coordinator has a capture-only
// inventory. Implementations must qualify its external launcher boundary.
type RoutineBrowserToolAdapter interface {
	RoutineBrowserToolsSupported(agentsetup.RoutineGrant) bool
}

func validRoutineToolMode(run Run, adapter Adapter) error {
	grant, err := routineGrant(run)
	if err != nil {
		return err
	}
	if grant == nil {
		return nil
	}
	if grant.RuntimeCapability == agentsetup.RoutineBrowserCapability {
		browser, ok := adapter.(RoutineBrowserToolAdapter)
		if !ok || adapter.Name() != Claude || !browser.RoutineBrowserToolsSupported(*grant) ||
			run.ReadOnlyReview || run.Purpose != "" && run.Purpose != "managed" {
			return ErrRoutineToolsUnavailable
		}
		return nil
	}
	qualified, ok := adapter.(RoutineToolAdapter)
	if !ok || !qualified.RoutineToolsSupported(*grant) || grant.RuntimeCapability != "routine_native_coding_v1" ||
		run.ReadOnlyReview || run.Purpose != "" && run.Purpose != "managed" {
		return ErrRoutineToolsUnavailable
	}
	return nil
}

// Local file edits use the same compiled floor as artifact validation. This
// does not grant outward action authority or replace the broker's full policy
// and fixed different-family evaluation before an artifact leaves the tree.
func routineWritablePath(workspace, name string) error {
	if len(name) > pathpolicy.MaxPathBytes {
		return errManagedFile
	}
	rel, err := managedRelative(workspace, name)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	for _, segment := range strings.Split(strings.ToLower(rel), "/") {
		switch segment {
		case ".git", ".config", ".codex", ".claude", ".cursor", ".pi", ".gemini", ".opencode", ".mcp.json", ".npmrc", ".gitconfig", ".gitmodules", "hooks":
			return ErrRoutineToolsUnavailable
		}
	}
	if !pathpolicy.LocalWriteAllowed(rel) {
		return ErrRoutineToolsUnavailable
	}
	return nil
}

// The current terminal fence denies network/foreign trees but grants writes
// throughout the workspace. Tests/package scripts could therefore rewrite a
// protected runtime or gate. Until an external fence is qualified, refuse even
// local tests/commits for routine bindings before toolchain probes or execution.
func routineTerminalAllowed(in terminalArgs) error {
	if !allowedTerminal(in.Command, in.Args) {
		return errors.New("terminal command denied")
	}
	return ErrRoutineToolsUnavailable
}
