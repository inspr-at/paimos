// SPDX-License-Identifier: AGPL-3.0-only
package rulesimport

import "testing"

// A re-import keeps people's explanations: an unchanged rule stays unchanged
// although only the stored rule has one, and an updated rule keeps it (the
// server then asks for a check because the text changed).
func TestImportKeepsExplanations(t *testing.T) {
	rule := func(id, text string) DraftRule {
		return DraftRule{Identity: id, Text: text, Why: "w", Strength: "normal", Enabled: true, Source: DraftSource{Reference: "AEON-250", Identity: id, Revision: "r1"}}
	}
	explained := func(r DraftRule) DraftRule {
		r.TLDR = &DraftTLDR{EN: "For people.", Basis: "0123456789abcdef"}
		return r
	}
	// A stored import carries its baseline, so an update is not an Aeon edit.
	baselined := func(r DraftRule) DraftRule {
		r.Details = lineageMarker + `{"content_sha256":"` + contentFingerprint(r) + `"}`
		return r
	}
	existing := []DraftRule{explained(rule("same", "Same text.")), explained(baselined(rule("moved", "Old text.")))}
	imported := []DraftRule{rule("same", "Same text."), rule("moved", "New text.")}
	imported[1].Source.Revision = "r2"
	merged, added, updated, unchanged, err := mergeImported(existing, imported)
	if err != nil || added != 0 || updated != 1 || unchanged != 1 {
		t.Fatalf("counts: %v %d %d %d", err, added, updated, unchanged)
	}
	for _, r := range merged {
		if r.TLDR == nil || r.TLDR.EN != "For people." || r.TLDR.Basis != "0123456789abcdef" {
			t.Fatalf("%s lost its explanation: %+v", r.Identity, r.TLDR)
		}
	}
	if merged[1].Text != "New text." {
		t.Fatal("the update itself must apply")
	}
	if !sameDraftRules([]DraftRule{rule("same", "Same text.")}, []DraftRule{explained(rule("same", "Same text."))}) {
		t.Fatal("explanations do not make a rule different")
	}
}
