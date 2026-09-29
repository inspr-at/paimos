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
	if raw, ok := fields["hide_from_release_notes"]; ok {
		var value any
		_ = json.Unmarshal(raw, &value)
		if _, ok := value.(bool); !ok {
			issues = append(issues, "hide_from_release_notes must be a boolean")
		}
	}
	return issues
}

// Completed matches the product's successful ticket completion states, excluding
// cancellation and archival. Tenant-defined states are not inferred.
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
	if kind != "ticket" || Completed(before) || !Completed(after) {
		return nil
	}
	return Issues(fields)
}
