// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

// Observed with codesign -dv on shipping CLI binaries (AEON-441), including
// their signing identifiers. Cursor.app is a general-purpose Node runner,
// not cursor-agent, and must never identify a Cursor harness. Changes require new
// signed-binary evidence. Pairing paths and local requests cannot extend this.
func attachVendorTeam(harness string) string {
	switch harness {
	case Claude:
		return "Q6L2SF6YDW"
	case Codex:
		return "2DC432GLL2"
	}
	return ""
}

func attachVendorIdentifier(harness string) string {
	switch harness {
	case Claude:
		return "com.anthropic.claude-code"
	case Codex:
		return "codex"
	}
	return ""
}

type attachSignature struct {
	TeamID     string
	Identifier string
	Signed     bool
}

type AttachLocalError struct {
	Code string `json:"code"`
	Hint string `json:"hint"`
}

func (e *AttachLocalError) Error() string { return e.Code + ": " + e.Hint }
func attachIdentityError() error {
	return &AttachLocalError{Code: "harness_identity_mismatch", Hint: "The running image does not match the paired harness vendor or recorded installation. Update the vendor installation, or repair its local pin with repin/add-harness, then restart agentd."}
}
func attachUnsignedIdentityError() error {
	return &AttachLocalError{Code: "harness_identity_mismatch", Hint: "Unsigned Claude and Codex images cannot be attached on macOS. Install the vendor-signed CLI, then restart agentd."}
}
func attachUnsupportedIdentityError() error {
	return &AttachLocalError{Code: "harness_identity_unsupported", Hint: "Cursor attach is unavailable on macOS until a signed cursor-agent CLI is available. Cursor.app cannot identify a Cursor harness."}
}
func attachBunOptionsError() error {
	return &AttachLocalError{Code: "harness_identity_unsupported", Hint: "This Claude process was started with a BUN_* assignment other than BUN_INSTALL. The signed runtime can use those variables to run other JavaScript. Unset them, then attach again."}
}
func attachBunOptionsUnobservableError() error {
	return &AttachLocalError{Code: "harness_identity_unsupported", Hint: "This Claude process cannot be attached because macOS hid its environment, and the signed executable can run JavaScript from a BUN_* variable."}
}
func attachUnsafeImageError() error {
	return &AttachLocalError{Code: "harness_executable_unsafe", Hint: "The running executable or an ancestor is untrusted. Use an installation outside the workspace, owned by you or root, without group or world write access (macOS admin directories are allowed)."}
}
func attachIdentityUnavailableError() error {
	return &AttachLocalError{Code: "harness_identity_unavailable", Hint: "The running image could not be verified in time. Retry attach; if verification remains unavailable, restart agentd and try again."}
}
func attachImageChangedError() error {
	return &AttachLocalError{Code: "harness_image_changed", Hint: "The running image changed during verification; inspect the process and attach again."}
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
	if approved == "" || harness != Claude && harness != Codex && harness != Cursor && harness != Grok {
		return nil, attachIdentityError()
	}
	if harness == Cursor && runtime.GOOS == "darwin" {
		return nil, attachUnsupportedIdentityError()
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
		// Only Linux may use a recorded install root or legacy exact-file pin.
		// Unsigned macOS images, including Rosetta processes, have no vendor identity.
		// A path may name a different inode after a rename over a running image.
		// On Darwin codesign must validate the dynamic code identified by PID.
		signature, err := m.signature(ctx, strconv.Itoa(observed.PID))
		if err != nil {
			if errors.Is(err, errAttachSignatureUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return nil, attachIdentityUnavailableError()
			}
			if errors.Is(err, errAttachRuntimeDenied) {
				return nil, attachBunOptionsError()
			}
			if errors.Is(err, errAttachRuntimeUnobservable) {
				return nil, attachBunOptionsUnobservableError()
			}
			return nil, attachIdentityError()
		}
		if signature.Signed {
			if signature.TeamID == "" || signature.TeamID != attachVendorTeam(harness) || signature.Identifier == "" || signature.Identifier != attachVendorIdentifier(harness) {
				return nil, attachIdentityError()
			}
		} else {
			if runtime.GOOS != "linux" {
				return nil, attachUnsignedIdentityError()
			}
			if err := m.validateLinuxAttachFallback(harness, physical, info); err != nil {
				return nil, err
			}
		}
	}
	// Kernel observation is repeated after signature and disk checks. A PID
	// reuse, exec, cwd change or replacement during verification fails closed.
	again, err := m.observe(observed.PID)
	if err != nil || again.Process != observed.Process || !m.unchangedHarnessImage(again, info) {
		return nil, attachImageChangedError()
	}
	return info, nil
}

// Kept separate so the Linux-only fallback boundaries can be tested on macOS
// too; the running-image validator never calls this for an unsigned Mac image.
func (m *AttachManager) validateLinuxAttachFallback(harness, physical string, info os.FileInfo) error {
	approved := m.cfg.Executables[harness]
	identity, exists := m.cfg.Identities[harness]
	pinned, pinErr := filepath.EvalSymlinks(approved)
	if exists && !identity.Matches(physical, info) || !exists && (pinErr != nil || pinned != physical) {
		return attachIdentityError()
	}
	return nil
}

var errAttachSignature = errors.New("vendor signature unavailable or invalid")
var errAttachSignatureUnavailable = errors.New("vendor signature verification unavailable")
var errAttachRuntimeDenied = errors.New("signed runtime accepted an injected environment")
var errAttachRuntimeUnobservable = errors.New("signed runtime environment is not visible")

// AttachConfig is immutable for a manager's lifetime. Session mutations remain
// locked, but slow external verification must not block unrelated watches.
// The caller holds m.mu; it is reacquired before this method returns.
func (m *AttachManager) validateHarnessImageUnlocked(ctx context.Context, observed attachObservation, harness string) (os.FileInfo, error) {
	m.mu.Unlock()
	defer m.mu.Lock()
	return m.validateHarnessImage(ctx, observed, harness)
}

func (m *AttachManager) recheckHarnessImage(ctx context.Context, peer, observed attachObservation, id string, s *localAttach) error {
	info, err := m.validateHarnessImageUnlocked(ctx, observed, s.snapshot.Harness)
	// A detach, sweep or close during verification must never resurrect a watch.
	if m.closed || m.sessions[id] != s {
		return attachImageChangedError()
	}
	if err != nil {
		return err
	}
	if !sameAttachImage(s.image, info) || !independentAttachPeer(peer, observed, m.ancestry) || s.tail != nil && s.tail.check() != nil {
		return attachImageChangedError()
	}
	return nil
}
