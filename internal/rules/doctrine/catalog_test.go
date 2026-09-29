// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"strings"
	"testing"
)

func TestCatalogMatchAndPointer(t *testing.T) {
	commit := strings.Repeat("ab", 20)
	cat := Catalog{
		Releases: []Release{{Repository: "inspr-at/inspr-modules", Ref: "v260922101217.0.0", Commit: commit}},
		Rules: []Indexed{
			{Identity: "inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#secrets", Key: "no-env-dump", Text: "Never print the environment."},
			{Identity: "inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#git", Key: "small-commits", Text: "Prefer small commits."},
		},
	}
	hit, ok := cat.Match("Never print the environment.", "", "")
	if !ok || hit.Identity != "inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#secrets" {
		t.Fatalf("text match %+v %v", hit, ok)
	}
	hit, ok = cat.Match("A different sentence.", "inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#git", "")
	if !ok || hit.Key != "small-commits" {
		t.Fatalf("identity match %+v %v", hit, ok)
	}
	hit, ok = cat.Match("A different sentence.", "", "doctrine:inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#secrets")
	if !ok || !strings.HasSuffix(hit.Identity, "#secrets") {
		t.Fatalf("reference match %+v %v", hit, ok)
	}
	if _, ok = cat.Match("Prefer small commits", "", ""); ok {
		t.Fatal("prefix is not a copy")
	}
	if _, ok = cat.Match("Keep the floor.", "no-env-dump", "test:ADR004"); ok {
		t.Fatal("a short key is not the doctrine identity")
	}
	pointer := cat.Pointer()
	if !strings.Contains(pointer, "inspr-at/inspr-modules@v260922101217.0.0 "+commit) || strings.Contains(pointer, "Never print") {
		t.Fatalf("pointer %q", pointer)
	}
	if (Catalog{}).Pointer() != "" {
		t.Fatal("no pin, no pointer")
	}
}
