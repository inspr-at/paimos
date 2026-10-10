// SPDX-License-Identifier: AGPL-3.0-only

// Package ticketbenefits validates the structural completion requirements.
// Sentence count, plain language and translation fidelity remain editorial:
// punctuation counting would reject abbreviations and cannot prove a benefit.
package ticketbenefits

import (
	"encoding/json"
	"strings"
)

// RequiredCode marks a ticket entering done, accepted or delivered without the
// four benefit texts. Clients that only read "error" are unchanged.
const RequiredCode = "benefit_required"

// Issues names every missing/invalid field in a stable order. Hidden tickets
// have exactly the same requirements. Drafts may be saved with these warnings.
func Issues(raw json.RawMessage) []string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	issues := []string{}
	for _, key := range []string{"pill_en", "pill_de", "benefit_en", "benefit_de"} {
		var value string
		if json.Unmarshal(fields[key], &value) != nil || strings.TrimSpace(value) == "" {
			issues = append(issues, key+" is required")
		} else if strings.HasPrefix(key, "pill_") && (len(strings.Fields(value)) < 2 || len(strings.Fields(value)) > 4) {
			issues = append(issues, key+" must contain 2–4 words")
		}
	}
	for _, key := range []string{"hide_from_release_notes", "no_release_needed"} {
		if raw, ok := fields[key]; ok {
			var value any
			_ = json.Unmarshal(raw, &value)
			if _, ok := value.(bool); !ok {
				issues = append(issues, key+" must be a boolean")
			}
		}
	}
	return issues
}

// OmitFromReleaseNotes applies to both public notes and unavailable members.
// Content work is never a software release note, regardless of its hide toggle.
func OmitFromReleaseNotes(raw json.RawMessage) bool {
	var flags struct {
		Hidden    bool `json:"hide_from_release_notes"`
		NoRelease bool `json:"no_release_needed"`
	}
	_ = json.Unmarshal(raw, &flags)
	return flags.Hidden || flags.NoRelease
}

// Completed matches resolved successful completion categories, excluding
// cancellation and archival. Mutation callers resolve tenant-defined states
// with aeon_work_status_category before checking the transition.
func Completed(state string) bool {
	switch state {
	case "done", "accepted", "delivered":
		return true
	default:
		return false
	}
}

// Transition checks only entering completion. Historical completed records stay
// editable; reopening them subjects the next completion to today's requirements.
func Transition(kind, before, after string, fields json.RawMessage) []string {
	if kind != "work" && kind != "ticket" || Completed(before) || !Completed(after) {
		return nil
	}
	return Issues(fields)
}
