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

// Reason codes are one vocabulary for blocked_accounts (per account, local
// status only) and harness_details (per harness, local status and the server):
//
//	repin_pending        a recorded Claude repin waits for active Claude runs; no fix
//	dependency_invalid   a pinned runtime dependency failed at probe or launch
//	pin_missing          no interpreter pin is recorded, but the launcher needs one
//	pin_partial          a pin lacks its path or version
//	pin_drifted          the pinned interpreter now reports another version
//	pin_invalid          the pinned path is unusable or no longer matches
//	pin_unsafe           the pinned path is writable by others or inside the workspace
//	login_required       the vendor sign-in is missing or not the approved identity
//	starting             not probed yet; no fix
//	harness_failed       the approved launcher failed to start
//	cli_unavailable      Claude's approved CLI executable changed or is missing
//	profile_permissions  the pi profile directory is not private
//
// The pin_* codes come from the static check before launch; dependency_invalid
// is the runtime failure of the same dependencies. For Claude the pins are the
// shared Node/SDK pins. Newer daemons may send other bounded code tokens: they
// stay visible as needing attention with the raw code and no guessed fix.
const (
	PinMissing    = "pin_missing"
	PinPartial    = "pin_partial"
	PinDrifted    = "pin_drifted"
	PinInvalid    = "pin_invalid"
	PinUnsafe     = "pin_unsafe"
	FixAddHarness = "add_harness"
	FixRepin      = "repin"
	FixLogin      = "login"
	FixRestart    = "restart"
)

// HarnessReasons lists the known reason codes in the order documented above.
var HarnessReasons = []string{"repin_pending", "dependency_invalid", PinMissing, PinPartial, PinDrifted, PinInvalid, PinUnsafe, "login_required", "starting", "harness_failed", "cli_unavailable", "profile_permissions"}

// HarnessFix is the one fix form for blocked_accounts and harness_details: a
// kind and the exact CLI line. Commands are derived from the harness and reason
// here and by the server, never accepted from daemon telemetry.
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

// RecoveryFix is the single repair mapping for account and harness reports:
//   - repin: Claude pin and dependency problems; repin replaces the shared
//     Node/SDK pins, including missing ones.
//   - add_harness: the same problems for other harnesses. For a connected
//     account it renews only that account's blocked interpreter pin.
//   - login: the vendor's normal sign-in.
//   - restart: resume setup after restoring the approved launcher, executable
//     or profile permissions. Service ownership stays with its owner,
//     including Nix/Home Manager.
func RecoveryFix(harness, reason string) HarnessFix {
	switch harness {
	case "claude", "codex", "cursor", "grok", "pi":
	default:
		return HarnessFix{}
	}
	switch reason {
	case "dependency_invalid", PinMissing, PinPartial, PinDrifted, PinInvalid, PinUnsafe:
		if harness == "claude" {
			return HarnessFix{FixRepin, "aeon-agentd repin --harness claude"}
		}
		return HarnessFix{FixAddHarness, "aeon-agentd add-harness --harness " + harness}
	case "login_required":
		command := harness + " login"
		switch harness {
		case "claude":
			command = "claude auth login"
		case "cursor":
			command = "cursor-agent login"
		case "pi":
			// pi signs in from its own prompt with /login.
			command = "pi"
		}
		return HarnessFix{FixLogin, command}
	case "harness_failed", "cli_unavailable", "profile_permissions":
		return HarnessFix{FixRestart, "aeon-agentd setup"}
	}
	return HarnessFix{}
}

// KnownHarnessState reports the states of the legacy harness_statuses map.
// Other bounded state tokens travel only in harness_details.
func KnownHarnessState(state string) bool {
	switch state {
	case "ready", "blocked", "login_required", "checking", "draining":
		return true
	}
	return false
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
