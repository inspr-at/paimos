// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
)

func TestAutomaticProvenanceFitsTheRulesBudget(t *testing.T) {
	if rules.MaxApplicableSets() != 2000 {
		t.Fatalf("rules budget is %d; update migration 0924", rules.MaxApplicableSets())
	}
	limit := automaticProvenanceLimit()
	raw, err := os.ReadFile("../db/migrations/0924_instruction_provenance_sources.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), fmt.Sprintf("ordinal < %d", limit)) {
		t.Fatalf("migration ordinal bound is not %d", limit)
	}
	version := "260929120000.0.0"
	hash := strings.Repeat("a", 64)
	size := int64(12)
	merged := ProvenanceItem{Kind: "rules_merged", LogicalName: "merged-rules", HashKind: "content", ContentSHA256: &hash, Version: &version, ByteSize: &size}
	sets := make([]ProvenanceItem, rules.MaxApplicableSets())
	for i := range sets {
		sets[i] = budgetSetItem(i)
	}
	// 64 constituent sets plus merged-rules used to exceed the old cap and
	// roll the receipt back. The budget itself must fit too.
	narrow, err := normalizeAutomaticProvenance(append([]ProvenanceItem{merged}, sets[:64]...))
	if err != nil || len(narrow) != 65 || !provenanceHasMerged(narrow, version) {
		t.Fatalf("64 sets + merged: %d %v", len(narrow), err)
	}
	fitted, err := normalizeAutomaticProvenance(append([]ProvenanceItem{merged}, sets...))
	if err != nil || len(fitted) != 1+len(sets) || !provenanceHasMerged(fitted, version) {
		t.Fatalf("full budget: %d %v", len(fitted), err)
	}
	over := append([]ProvenanceItem{merged}, sets...)
	over = append(over, extraSkills(maxLocalProvenanceItems+1)...)
	got, err := normalizeAutomaticProvenance(over)
	if err != nil || len(got) != limit || !provenanceHasMerged(got, version) {
		t.Fatalf("overflow kept %d (limit %d) err %v", len(got), limit, err)
	}
}

func budgetSetItem(i int) ProvenanceItem {
	version := "260929110000.0.0"
	hash := strings.Repeat("b", 64)
	return ProvenanceItem{
		Kind: "rules_set", LogicalName: fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1),
		HashKind: "content", ContentSHA256: &hash, Version: &version,
	}
}

func extraSkills(n int) []ProvenanceItem {
	hash := strings.Repeat("c", 64)
	size := int64(4)
	out := make([]ProvenanceItem, n)
	for i := range out {
		out[i] = ProvenanceItem{
			Kind: "skill", LogicalName: fmt.Sprintf("skill%02d/SKILL.md", i),
			HashKind: "content", ContentSHA256: &hash, ByteSize: &size,
		}
	}
	return out
}

func provenanceHasMerged(items []ProvenanceItem, version string) bool {
	for _, item := range items {
		if item.Kind == "rules_merged" && item.LogicalName == "merged-rules" && item.Version != nil && *item.Version == version {
			return true
		}
	}
	return false
}
