// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"fmt"
	"maps"
	"strings"
)

// enrollmentReadiness compares approval with a complete daemon account report.
// Older daemons without account reports can still prove a missing harness when
// they report their harness set. An absent report alone proves no missing binding.
func enrollmentReadiness(v View, local LocalStatus) LocalStatus {
	if local.AccountStatuses == nil && len(local.HarnessDetails) == 0 && len(local.HarnessStatuses) == 0 {
		return local
	}
	local.HarnessDetails = maps.Clone(local.HarnessDetails)
	local.HarnessStatuses = maps.Clone(local.HarnessStatuses)
	if local.HarnessDetails == nil {
		local.HarnessDetails = map[string]HarnessDetail{}
	}
	if local.HarnessStatuses == nil {
		local.HarnessStatuses = map[string]string{}
	}
	accounts := maps.Clone(local.AccountStatuses)
	if accounts == nil {
		accounts = map[string]HarnessDetail{}
	}
	for _, a := range v.Enrollments {
		if a.State != "connected" {
			continue
		}
		d, found := local.AccountStatuses[a.AccountID]
		if local.AccountStatuses == nil {
			d, found = local.HarnessDetails[a.Harness]
			if !found && local.HarnessStatuses[a.Harness] != "" {
				d.State, found = local.HarnessStatuses[a.Harness], true
			}
		}
		if !found && (local.AccountStatuses != nil || len(local.HarnessStatuses) > 0 || len(local.HarnessDetails) > 0) {
			d, _ = HarnessReport(a.Harness, "blocked", "binding_missing")
			// A bound sibling remains ready, but it must retain this missing
			// enrollment in the existing partial-attention projection.
			current := local.HarnessDetails[a.Harness]
			if current.State == "ready" {
				current.Attention = append(append([]AccountAttention(nil), current.Attention...), AccountAttention{AccountID: a.AccountID, Reason: d.Reason})
				current.AttentionCount = len(current.Attention)
				local.HarnessDetails[a.Harness] = current
			} else {
				local.HarnessDetails[a.Harness], local.HarnessStatuses[a.Harness] = d, d.State
			}
		}
		if d.State != "" {
			accounts[a.AccountID] = d
		}
	}
	local.AccountStatuses = accounts
	return local
}

func harnessName(h string) string {
	return map[string]string{"claude": "Claude", "codex": "Codex", "cursor": "Cursor", "grok": "Grok", "pi": "pi", "gemini": "Gemini CLI", "opencode": "OpenCode"}[h]
}

func readinessAction(v View, local LocalStatus) (string, string) {
	stage := "provisioning"
	var actions []string
	for _, a := range v.Enrollments {
		if a.State != "connected" {
			continue
		}
		d := local.AccountStatuses[a.AccountID]
		name := harnessName(a.Harness)
		label := a.Label
		if label == "" {
			label = a.AccountID
		}
		account := fmt.Sprintf("%s account %q", name, label)
		var action string
		switch d.Reason {
		case "binding_missing":
			stage = "blocked"
			action = fmt.Sprintf("%s was approved but isn't set up on this computer. Run `aeon-agentd add-harness --harness %s`, or remove %s from this computer in Aeon.", name, a.Harness, name)
		case "probe_pending", "starting":
			if !local.Ready {
				action = account + " is waiting for its sign-in and availability check (up to 60 seconds after daemon startup)."
			}
		case "capacity_capture":
			if !local.Ready {
				action = account + " is capturing capacity; this short check is expected to finish within 10 seconds."
			}
		default:
			if !local.Ready && (d.State == "blocked" || d.State == "login_required") {
				stage = "blocked"
				reason := strings.ReplaceAll(d.Reason, "_", " ")
				switch d.Reason {
				case "probe_timeout":
					reason = "its sign-in and availability check did not finish within 60 seconds"
				case "probe_failed":
					reason = "its availability check failed or could not be confirmed by Aeon"
				case "capacity_timeout":
					reason = "capacity capture exceeded its 10-second limit"
				}
				action = account + " is blocked: " + reason + "."
				if fix := RecoveryFix(a.Harness, d.Reason); fix.Command != "" {
					action += " Run `" + fix.Command + "`."
				}
			}
		}
		if action != "" {
			actions = append(actions, action)
		}
	}
	return stage, strings.Join(actions, " ")
}

func verificationMessage(harness, reason string) string {
	cause := map[string]string{
		"adapter_unsupported":   "the installed adapter cannot enforce safe verification",
		"binding_incomplete":    "the verification binding is incomplete or unsafe",
		"local_binding_missing": "the approved account has no usable local binding",
	}[reason]
	if cause == "" {
		cause = "safe verification is unavailable"
	}
	return fmt.Sprintf("%s verification couldn't run on this computer (%s). The computer is paired; you can re-run verification from /agents with a new approval.", harnessName(harness), cause)
}
