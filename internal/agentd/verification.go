// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const VerificationPurpose = "pairing_verification"
const VerificationTask = "Reply exactly AEON_VERIFIED. Do not modify files, perform privileged actions, access external networks, or use external/MCP tools. Use the enforced read-only verification mode."

var ErrVerificationUnavailable = errors.New("verification blocked: this adapter has no qualified harmless execution mode")

// VerificationAdapter is an explicit execution capability, not a prompt hint.
// Unknown/future adapters cannot accidentally inherit the managed tool path.
type VerificationAdapter interface{ VerificationSupported() bool }

func (*ClaudeAdapter) VerificationSupported() bool { return true }
func (*CursorAdapter) VerificationSupported() bool { return true }

func validExecutionMode(r Run, adapter Adapter) error {
	if r.Purpose == "" || r.Purpose == "managed" {
		return nil
	}
	if r.Purpose != VerificationPurpose {
		return ErrUnsupported
	}
	a, ok := adapter.(VerificationAdapter)
	if !ok || !a.VerificationSupported() {
		return ErrVerificationUnavailable
	}
	if r.AccountID == "" || r.VerificationTask != VerificationTask || r.MaxDurationSeconds == nil || *r.MaxDurationSeconds < 1 || *r.MaxDurationSeconds > 60 || r.VerificationPolicy != "read_only" || r.RepositoryMutationAllowed == nil || *r.RepositoryMutationAllowed {
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
