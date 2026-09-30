// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

// Observed from signed shipping binaries (AEON-435): Claude and native Codex
// CLIs, and Cursor.app. Changes require new
// signed-binary evidence. Pairing paths and local requests cannot extend this.
func attachVendorTeam(harness string) string {
	switch harness {
	case Claude:
		return "Q6L2SF6YDW"
	case Codex:
		return "2DC432GLL2"
	case Cursor:
		return "VDXQ22DGB9"
	}
	return ""
}

type attachSignature struct {
	TeamID string
	Signed bool
}

type AttachLocalError struct {
	Code string `json:"code"`
	Hint string `json:"hint"`
}

func (e *AttachLocalError) Error() string { return e.Code + ": " + e.Hint }
func attachIdentityError() error {
	return &AttachLocalError{Code: "harness_identity_mismatch", Hint: "The running image does not match the paired harness vendor or recorded installation. Update the vendor installation, or repair its local pin with repin/add-harness, then restart agentd."}
}
func attachUnsafeImageError() error {
	return &AttachLocalError{Code: "harness_executable_unsafe", Hint: "The running executable or an ancestor is untrusted. Use an installation outside the workspace, owned by you or root, without group or world write access (macOS admin directories are allowed)."}
}

func sameAttachImage(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return false
	}
	before, beforeOK := a.Sys().(*syscall.Stat_t)
	after, afterOK := b.Sys().(*syscall.Stat_t)
	return beforeOK && afterOK && before.Uid == after.Uid && before.Gid == after.Gid && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime() == b.ModTime() && a.Mode() == b.Mode()
}
func (m *AttachManager) unchangedHarnessImage(observed attachObservation, before os.FileInfo) bool {
	physical, after, err := agentsetup.TrustedAttachExecutable(observed.Executable, m.cfg.Workspace)
	return err == nil && physical == observed.Executable && sameAttachImage(before, after)
}

func (m *AttachManager) validateHarnessImage(ctx context.Context, observed attachObservation, harness string) (os.FileInfo, error) {
	approved := m.cfg.Executables[harness]
	if approved == "" || attachVendorTeam(harness) == "" && harness != Grok {
		return nil, attachIdentityError()
	}
	physical, info, err := agentsetup.TrustedAttachExecutable(observed.Executable, m.cfg.Workspace)
	if err != nil || physical != observed.Executable {
		return nil, attachUnsafeImageError()
	}
	if harness == Grok {
		// Grok remains outside AEON-435: preserve its exact-path contract.
		pinned, err := filepath.EvalSymlinks(approved)
		if err != nil || pinned != physical {
			return nil, attachIdentityError()
		}
	} else {
		// The vendor check always applies to signed images, including exact pins.
		// An unsigned legacy exact-file pin remains the narrow fallback it was.
		signature, err := m.signature(ctx, physical)
		if err != nil {
			return nil, attachIdentityError()
		}
		if signature.Signed {
			if signature.TeamID == "" || signature.TeamID != attachVendorTeam(harness) {
				return nil, attachIdentityError()
			}
		} else {
			identity, exists := m.cfg.Identities[harness]
			pinned, pinErr := filepath.EvalSymlinks(approved)
			if exists && !identity.Matches(physical, info) || !exists && (pinErr != nil || pinned != physical) {
				return nil, attachIdentityError()
			}
		}
	}
	// Kernel observation is repeated after signature and disk checks. A PID
	// reuse, exec, cwd change or replacement during verification fails closed.
	again, err := m.observe(observed.PID)
	if err != nil || again.Process != observed.Process || !m.unchangedHarnessImage(again, info) {
		return nil, &AttachLocalError{Code: "harness_image_changed", Hint: "The running image changed during verification; inspect the process and attach again."}
	}
	return info, nil
}

var errAttachSignature = errors.New("vendor signature unavailable or invalid")
