// SPDX-License-Identifier: AGPL-3.0-only

// Package ticketbenefits validates the structural completion requirements.
// Sentence count, plain language and translation fidelity remain editorial:
// punctuation counting would reject abbreviations and cannot prove a benefit.
package ticketbenefits

import (
	"encoding/json"
	"strings"
)

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

// Transition checks only entering done. Historical already-done records stay
// editable; reopening them subjects the next completion to today's requirements.
func Transition(kind, before, after string, fields json.RawMessage) []string {
	if kind != "ticket" || before == "done" || after != "done" {
		return nil
	}
	return Issues(fields)
}
