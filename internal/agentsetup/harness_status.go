// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import "errors"

// HarnessIssue retains the diagnostic locally while exposing only its code.
type HarnessIssue struct {
	Reason string
	Err    error
}

func (e *HarnessIssue) Error() string { return e.Err.Error() }
func (e *HarnessIssue) Unwrap() error { return e.Err }

// HarnessFailureReason preserves a typed failure across startup, probe and
// refresh paths; callers never classify private diagnostics by matching text.
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
		case "pin_missing":
			d.Fix = "aeon-agentd add-harness --harness " + harness
		case "cli_unavailable":
			// Existing enrollments cannot be added again, and repin changes
			// only Node/SDK pins. Local status explains the required restoration.
			d.Fix = "aeon-agentd setup status"
		default:
			return HarnessDetail{}, false
		}
	default:
		return HarnessDetail{}, false
	}
	return d, true
}
