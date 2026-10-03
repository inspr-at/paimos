// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import "encoding/json"

// These sentences are the complete publishable diagnostic vocabulary. Never
// truncate an arbitrary error into reason_detail: even a short error can leak.
const (
	ProbeProfileMissing       = "the approved account profile is missing"
	ProbeProfilePhysical      = "the approved account profile is not a physical directory"
	ProbeProfilePrivate       = "the approved account profile is not private (requires mode 0700)"
	ProbeClaudeDefaultPrivate = "the default Claude profile is not private (requires mode 0700)"
	ProbeCommandFailed        = "the sign-in command could not start or finish"
	ProbeOutputInvalid        = "the sign-in command returned an unreadable or incomplete answer"
	ProbeIdentityMissing      = "the approved account identity is missing"
	ProbeEmailMissing         = "the sign-in answer did not identify the account"
	ProbeSignedOut            = "the approved account is signed out in the daemon's view"
	ProbeExitFailed           = "the sign-in command exited unsuccessfully"
)

const maxProbeDetail = 160

func SafeProbeDetail(reason, detail string) string {
	if reason == "login_required" && detail == ProbeSignedOut {
		return detail
	}
	if reason != "probe_failed" || len(detail) > maxProbeDetail {
		return ""
	}
	switch detail {
	case ProbeProfileMissing, ProbeProfilePhysical, ProbeProfilePrivate, ProbeClaudeDefaultPrivate,
		ProbeCommandFailed, ProbeOutputInvalid, ProbeIdentityMissing, ProbeEmailMissing, ProbeExitFailed:
		return detail
	}
	return ""
}

// Invalid advisory extensions never invalidate the lifecycle report itself.
func decodeProbeDetail(reason string, raw json.RawMessage) string {
	if len(raw) > 6*maxProbeDetail+2 {
		return ""
	}
	var detail string
	if json.Unmarshal(raw, &detail) != nil {
		return ""
	}
	return SafeProbeDetail(reason, detail)
}

// WithProbeDetail validates again at each persistence/projection boundary.
// The exact repair is derived here, never accepted from daemon text.
func (d HarnessDetail) WithProbeDetail(harness, detail string) HarnessDetail {
	d.ReasonDetail = ""
	d.Fix = HarnessFix{}
	report, ok := HarnessReport(harness, d.State, d.Reason)
	if !ok {
		return d
	}
	d.Fix = report.Fix
	if d.State != "blocked" && d.State != "login_required" {
		return d
	}
	d.ReasonDetail = SafeProbeDetail(d.Reason, detail)
	if harness != "claude" && d.ReasonDetail == ProbeClaudeDefaultPrivate {
		d.ReasonDetail = ""
	}
	if harness == "claude" && d.ReasonDetail == ProbeClaudeDefaultPrivate {
		d.Fix = HarnessFix{"permissions", `chmod 700 "$HOME/.claude"`}
	}
	return d
}
