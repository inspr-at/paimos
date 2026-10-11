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

// textIssue is shared by write-time and completion validation. Empty draft
// fields remain optional; any non-empty pill has the same word-count rule.
func textIssue(key string, raw json.RawMessage, required bool) string {
	var value string
	if len(raw) == 0 || string(raw) == "null" {
		if required {
			return key + " is required"
		}
		return ""
	}
	if json.Unmarshal(raw, &value) != nil {
		if required {
			return key + " is required"
		}
		return key + " must be a string"
	}
	if strings.TrimSpace(value) == "" {
		if required {
			return key + " is required"
		}
		return ""
	}
	if strings.HasPrefix(key, "pill_") {
		count := len(strings.Fields(value))
		if count < 2 || count > 4 {
			return key + " must contain 2–4 words"
		}
	}
	return ""
}

// PillIssues rejects invalid supplied pills without requiring draft benefits.
// Reports and every write/transition use the same validator as completion.
func PillIssues(raw json.RawMessage) []string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	issues := []string{}
	for _, key := range []string{"pill_en", "pill_de"} {
		if issue := textIssue(key, fields[key], false); issue != "" {
			issues = append(issues, issue)
		}
	}
	return issues
}

// Issues names every missing/invalid field in a stable order. Hidden tickets
// have exactly the same completion requirements.
func Issues(raw json.RawMessage) []string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	issues := []string{}
	for _, key := range []string{"pill_en", "pill_de", "benefit_en", "benefit_de"} {
		if issue := textIssue(key, fields[key], true); issue != "" {
			issues = append(issues, issue)
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

// Transition validates supplied pills on every write. Entering completion also
// requires all four texts; historical records may still have missing benefits.
func Transition(kind, before, after string, fields json.RawMessage) []string {
	if kind != "work" && kind != "ticket" {
		return nil
	}
	if !Completed(before) && Completed(after) {
		return Issues(fields)
	}
	return PillIssues(fields)
}
