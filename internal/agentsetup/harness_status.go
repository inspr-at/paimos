// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

// HarnessIssue retains the diagnostic locally while exposing only its code.
type HarnessIssue struct {
	Reason string
	Err    error
}

func (e *HarnessIssue) Error() string { return e.Err.Error() }
func (e *HarnessIssue) Unwrap() error { return e.Err }

// HarnessDetail complements the legacy string status map without sending local
// diagnostics. Reason and Fix share the blocked_accounts vocabulary.
type HarnessDetail struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// HarnessReport returns only known codes and fixed commands. Callers must still
// scope the report to an active enrollment; no client-supplied command is trusted.
func HarnessReport(harness, state, reason string) (HarnessDetail, bool) {
	switch harness {
	case "claude", "codex", "cursor", "grok", "pi":
	default:
		return HarnessDetail{}, false
	}
	d := HarnessDetail{State: state, Reason: reason}
	switch state {
	case "ready", "draining":
		return d, reason == ""
	case "checking":
		return d, reason == "" || reason == "starting"
	case "login_required":
		if reason != "" && reason != "login_required" {
			return HarnessDetail{}, false
		}
		d.Reason = "login_required"
		switch harness {
		case "claude":
			d.Fix = "claude auth login"
		case "cursor":
			d.Fix = "cursor-agent login"
		default:
			d.Fix = harness + " login"
		}
	case "blocked":
		switch reason {
		case "":
		case "repin_pending":
		case "dependency_invalid":
			if harness == "claude" {
				d.Fix = "aeon-agentd repin --harness claude"
			} else {
				d.Fix = "aeon-agentd add-harness --harness " + harness
			}
		case "pin_missing", "cli_unavailable":
			d.Fix = "aeon-agentd add-harness --harness " + harness
		default:
			return HarnessDetail{}, false
		}
	default:
		return HarnessDetail{}, false
	}
	return d, true
}
