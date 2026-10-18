// SPDX-License-Identifier: AGPL-3.0-only
package ticketbenefits

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMigratedWorkCompletionRequirements(t *testing.T) {
	valid := json.RawMessage(`{"pill_en":"Clear release notes","pill_de":"Klare Release Notes","benefit_en":"Work explains its benefit.","benefit_de":"Arbeit erklärt ihren Nutzen."}`)
	want := []string{"pill_en is required", "pill_de is required", "benefit_en is required", "benefit_de is required"}
	for _, after := range []string{"done", "accepted", "delivered"} {
		for _, before := range []string{"", "open", "in_progress", "qa"} {
			for _, fields := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"hide_from_release_notes":true}`)} {
				if got := Transition("work", before, after, fields); !reflect.DeepEqual(got, want) {
					t.Errorf("work %q -> %s with %s: %v, want %v", before, after, fields, got, want)
				}
			}
			if got := Transition("work", before, after, valid); len(got) != 0 {
				t.Errorf("valid work completion rejected: %v", got)
			}
		}
		for _, before := range []string{"done", "accepted", "delivered"} {
			if got := Transition("work", before, after, json.RawMessage(`{}`)); len(got) != 0 {
				t.Errorf("historical work edit blocked: %v", got)
			}
		}
	}
}
