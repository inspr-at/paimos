// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"encoding/json"
	"errors"
	"regexp"
)

// HarnessIssue retains the diagnostic locally while exposing only its code.
type HarnessIssue struct {
	Reason string
	Err    error
}

func (e *HarnessIssue) Error() string { return e.Err.Error() }
func (e *HarnessIssue) Unwrap() error { return e.Err }

// HarnessFailureReason never classifies private diagnostics by matching text.
func HarnessFailureReason(err error) string {
	if err == nil {
		return ""
	}
	var issue *HarnessIssue
	if errors.As(err, &issue) {
		return issue.Reason
	}
	return "dependency_invalid"
}

const (
	PinMissing    = "pin_missing"
	PinPartial    = "pin_partial"
	PinDrifted    = "pin_drifted"
	PinInvalid    = "pin_invalid"
	PinUnsafe     = "pin_unsafe"
	FixAddHarness = "add_harness"
	FixRepin      = "repin"
)

// HarnessFix is shared by blocked_accounts and harness_details. Commands are
// derived locally and by the server, never accepted from daemon telemetry.
type HarnessFix struct {
	Kind    string `json:"kind"`
	Command string `json:"command"`
}

// HarnessDetail uses the same reason tokens and structured fix as BlockedAccount.
// Unknown bounded tokens survive version skew; local diagnostics never leave the host.
type HarnessDetail struct {
	State  string     `json:"state"`
	Reason string     `json:"reason,omitempty"`
	Fix    HarnessFix `json:"fix,omitzero"`
}

// Ignore advisory extensions and legacy string fixes without weakening the
// strict decoder for lifecycle identities, fences or cleanup acknowledgements.
func (d *HarnessDetail) UnmarshalJSON(raw []byte) error {
	var wire struct {
		State  string          `json:"state"`
		Reason string          `json:"reason"`
		Fix    json.RawMessage `json:"fix"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*d = HarnessDetail{State: wire.State, Reason: wire.Reason}
	if len(wire.Fix) > 0 && wire.Fix[0] == '{' {
		_ = json.Unmarshal(wire.Fix, &d.Fix)
	}
	return nil
}

var harnessCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// RecoveryFix is the single repair mapping for account and harness reports.
// Only Claude supports repin; other interpreter pins need enrollment renewal.
// Restart means resume setup after restoring the approved installation; service
// ownership remains with its existing owner, including Nix/Home Manager.
func RecoveryFix(harness, reason string) HarnessFix {
	switch harness {
	case "claude", "codex", "cursor", "grok", "pi":
	default:
		return HarnessFix{}
	}
	switch reason {
	case "dependency_invalid", PinPartial, PinDrifted, PinInvalid, PinUnsafe:
		if harness == "claude" {
			return HarnessFix{FixRepin, "aeon-agentd repin --harness claude"}
		}
		return HarnessFix{FixAddHarness, "aeon-agentd add-harness --harness " + harness}
	case PinMissing:
		return HarnessFix{FixAddHarness, "aeon-agentd add-harness --harness " + harness}
	case "login_required":
		command := harness + " login"
		switch harness {
		case "claude":
			command = "claude auth login"
		case "cursor":
			command = "cursor-agent login"
		case "pi":
			command = "pi"
		}
		return HarnessFix{"login", command}
	case "harness_failed", "cli_unavailable", "profile_permissions":
		return HarnessFix{"restart", "aeon-agentd setup"}
	}
	return HarnessFix{}
}

// HarnessReport retains unknown code tokens so newer daemons remain diagnosable.
// Callers scope reports to enrolled harnesses and check legacy-state agreement.
func HarnessReport(harness, state, reason string) (HarnessDetail, bool) {
	switch harness {
	case "claude", "codex", "cursor", "grok", "pi":
	default:
		return HarnessDetail{}, false
	}
	if !harnessCode.MatchString(state) || reason != "" && !harnessCode.MatchString(reason) {
		return HarnessDetail{}, false
	}
	if state == "login_required" && reason == "" {
		reason = "login_required"
	}
	if (state == "ready" || state == "draining") && reason != "" {
		return HarnessDetail{}, false
	}
	d := HarnessDetail{State: state, Reason: reason}
	switch state {
	case "blocked", "login_required", "checking":
		d.Fix = RecoveryFix(harness, reason)
	}
	return d, true
}
