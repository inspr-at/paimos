// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"path/filepath"
	"slices"
)

// InstallCapability advertises that the server stores the reported install
// method. An older strict server rejects the unknown progress field.
const InstallCapability = "agent-install-v1"

// InstallMethod names the path-proof add-harness entry point that reaches the
// running installation: "homebrew", "nix" or "direct". It never executes brew
// or trusts PATH. Empty means no entry point provably reaches it; the web
// then keeps a manual choice.
//
// Homebrew: <prefix>/Cellar/aeon-agentd/<version>/bin/aeon-agentd with
// <prefix>/bin/aeon-agentd linked into the same formula.
// Nix: ~/.nix-profile/bin/aeon-agentd resolves into /nix/store.
// Direct: ~/.local/bin/aeon-agentd resolves into ~/.local/lib/aeon.
// A newer release behind the same entry point still counts: add-harness runs
// there, not in this process.
func InstallMethod(executable, home string) string {
	return installMethod(executable, home, "/nix/store")
}

func installMethod(executable, home, store string) string {
	physical, err := filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(physical) {
		return ""
	}
	reaches := func(entry, root string) bool {
		resolved, err := filepath.EvalSymlinks(entry)
		return err == nil && within(root, physical) && within(root, resolved)
	}
	formula := filepath.Dir(filepath.Dir(filepath.Dir(physical)))
	cellar := filepath.Dir(formula)
	if filepath.Base(physical) == "aeon-agentd" && filepath.Base(filepath.Dir(physical)) == "bin" && filepath.Base(formula) == "aeon-agentd" && filepath.Base(cellar) == "Cellar" &&
		reaches(filepath.Join(filepath.Dir(cellar), "bin", "aeon-agentd"), formula) {
		return "homebrew"
	}
	if !filepath.IsAbs(home) {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if reaches(filepath.Join(home, ".nix-profile", "bin", "aeon-agentd"), store) {
		return "nix"
	}
	if reaches(filepath.Join(home, ".local", "bin", "aeon-agentd"), filepath.Join(home, ".local", "lib", "aeon")) {
		return "direct"
	}
	return ""
}

// reportInstall adds the detected method only for a server that stores it.
func (e *Engine) reportInstall(view View, progress *SetupProgress) *SetupProgress {
	if progress != nil && e.InstallMethod != "" && slices.Contains(view.ServerCapabilities, InstallCapability) {
		progress.InstallMethod = e.InstallMethod
	}
	return progress
}
