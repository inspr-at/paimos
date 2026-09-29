// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/inspr-at/paimos/internal/agentverification"
)

const VerificationPurpose = "pairing_verification"
const VerificationTask = "Reply exactly AEON_VERIFIED. Do not modify files, perform privileged actions, access external networks, or use external/MCP tools. Use the enforced read-only verification mode."

var ErrVerificationUnavailable = errors.New("verification blocked: this adapter has no qualified harmless execution mode")

// VerificationAdapter is an explicit execution capability, not a prompt hint.
// Unknown/future adapters cannot accidentally inherit the managed tool path.
type VerificationAdapter interface{ VerificationSupported() bool }

func (*ClaudeAdapter) VerificationSupported() bool { return verificationSupported(Claude) }

func (*PiAdapter) VerificationSupported() bool { return verificationSupported(Pi) }

func verificationSupported(harness string) bool {
	return agentverification.For(harness, runtime.GOOS, runtime.GOARCH).Supported
}

// Cursor ACP inherits the shared MCP lease when session/new receives an empty
// mcpServers array (CLI 2026.09.18-9a7762b, 1006.index.js). Ask mode does not bind
// an empty tool inventory, and ACP exposes no supported override for that lease.
// See https://cursor.com/docs/cli/acp#MCP-servers. Block before the account probe
// or session startup until a qualified isolation mechanism is available.
// The 2026.09.26-dd393fe package still resolves tools from mcpLease in ACP,
// loads team hooks, and exposes only internal CLI tool-filter headers. Moving
// CURSOR_CONFIG_DIR and selecting ask is not a qualified no-tools boundary.
func (*CursorAdapter) VerificationSupported() bool { return verificationSupported(Cursor) }

// Codex explicitly reports its unqualified capability. In codex-cli
// 0.157.1, thread/start cannot set the immutable ToolPolicy.allowed_tools ceiling
// (codex-rs/ext/extension-api/src/tool_policy.rs). Read-only sandboxing does not
// constrain inherited MCP actions, and even mcpServerStatus/list creates eager
// connections (codex-rs/codex-mcp/src/mcp/mod.rs). Checking a separate config
// snapshot before thread creation cannot enforce this run's no-mutation promise.
// Keep verification blocked before any vendor process until the public protocol
// can bind a no-tools/no-hooks policy before startup, or an equivalent qualified
// execution boundary exists. Source: github.com/openai/codex/tree/rust-v0.157.1.
func (*CodexAdapter) VerificationSupported() bool { return verificationSupported(Codex) }

func validExecutionMode(r Run, adapter Adapter) error {
	if err := validQueuedExecutionMode(r, adapter); err != nil {
		return err
	}
	if r.Purpose == VerificationPurpose && (r.AccountID == "" || r.RequestedAccountID != "" && r.AccountID != r.RequestedAccountID) {
		return errors.New("verification account binding is incomplete or unsafe")
	}
	return nil
}

// A queued verification is deliberately unbound until the server reserves its
// approved account. Check immutable policy/capability before probing or routing;
// adapters still require the strict, routed binding in validExecutionMode.
func validQueuedExecutionMode(r Run, adapter Adapter) error {
	if r.Purpose == "" || r.Purpose == "managed" {
		return nil
	}
	if r.Purpose != VerificationPurpose {
		return ErrUnsupported
	}
	a, ok := adapter.(VerificationAdapter)
	if !ok || !a.VerificationSupported() {
		reason := agentverification.For(adapter.Name(), runtime.GOOS, runtime.GOARCH).Reason
		if reason == "" { // A locally unqualified variant may be stricter.
			return ErrVerificationUnavailable
		}
		return fmt.Errorf("%w: %s", ErrVerificationUnavailable, reason)
	}
	if r.VerificationTask != VerificationTask || r.MaxDurationSeconds == nil || *r.MaxDurationSeconds < 1 || *r.MaxDurationSeconds > 60 || r.VerificationPolicy != "read_only" || r.RepositoryMutationAllowed == nil || *r.RepositoryMutationAllowed {
		return errors.New("verification binding is incomplete or unsafe")
	}
	return nil
}

func verificationScratch(workspace string) (string, error) {
	dir, err := os.MkdirTemp("", "aeon-verification-")
	if err != nil {
		return "", errors.New("verification scratch unavailable")
	}
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errors.New("verification scratch unavailable")
	}
	rel, err := filepath.Rel(workspace, physical)
	if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("verification scratch must be outside approved workspace")
	}
	// Retain bounded scratch audit evidence rather than recursively deleting
	// an adapter-populated tree. The path never grants access to the repository.
	return physical, nil
}
