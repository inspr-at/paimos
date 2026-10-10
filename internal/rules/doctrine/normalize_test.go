// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
)

func TestEveryASCIIConfusableMapping(t *testing.T) {
	checked, missed := 0, 0
	for source, target := range unicodeConfusables {
		ascii := target != ""
		for _, r := range target {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				ascii = false
			}
		}
		if !ascii {
			continue
		}
		checked++
		plain := strings.Join([]string{"keep" + target, "quiet" + target, "violet" + target, "notebooks" + target, "behind" + target, "eastern" + target, "staircase" + target}, " ")
		attack := strings.ReplaceAll(plain, target, string(source))
		if New(nil, Options{}).guardPublic(publicRepository, attack) == nil && guardPrivateQuotes(quoteCorpus(plain), nil, attack) == nil {
			missed++
			if missed <= 20 {
				t.Errorf("U+%04X mapping to %q evaded the guard", source, target)
			}
		}
	}
	t.Logf("ASCII confusable mappings checked=%d missed=%d", checked, missed)
	if checked < 1000 || missed != 0 {
		t.Fatalf("coverage checked=%d missed=%d", checked, missed)
	}
}

func TestDefaultIgnorablesRemovedBeforeNormalization(t *testing.T) {
	const sample = "Keep quiet violet notebooks behind the eastern staircase."
	want := normalizeProposalText(sample)
	for _, span := range unicodeDefaultIgnorables {
		for r := span[0]; r <= span[1]; r++ {
			if got := normalizeProposalText("note" + string(r) + "book"); got != normalizeProposalText("notebook") {
				t.Fatalf("default ignorable U+%04X split or changed a word", r)
			}
		}
	}
	for _, r := range []rune{'\u00ad', '\u200b', '\u200d'} {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			for i := 0; i <= len(sample); i++ {
				attack := sample[:i] + string(r) + sample[i:]
				if normalizeProposalText(attack) != want || guardPrivateQuotes(quoteCorpus(sample), nil, attack) == nil {
					t.Fatalf("insertion at position %d escaped", i)
				}
			}
			for _, identity := range []string{"hsb" + "424242", "barta.cm", "pm.barta", "inspr-doctrine-private"} {
				for i := 0; i <= len(identity); i++ {
					if New(nil, Options{}).guardPublic(publicRepository, identity[:i]+string(r)+identity[i:]) == nil {
						t.Fatalf("identity insertion escaped at %d", i)
					}
				}
			}
		})
	}
}

func TestEveryNamedLatinSmallCapital(t *testing.T) {
	for from, to := range unicodeSmallCapitals {
		if normalizeProposalText(string(from)) != normalizeProposalText(to) {
			t.Errorf("U+%04X small-cap fold differs", from)
		}
	}
	if unicodeSmallCapitals['\ua7af'] != "q" {
		t.Fatal("small-cap Q absent")
	}
}

func TestAllFormatControlsRemainInvisibleToComparison(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.Is(unicode.Cf, r) && normalizeProposalText("note"+string(r)+"book") != normalizeProposalText("notebook") {
			t.Fatalf("format control U+%04X became a word separator", r)
		}
	}
}

func TestPinnedSkeletonConverges(t *testing.T) {
	for r, target := range unicodeConfusables {
		for _, text := range []string{string(r), target} {
			folded := normalizeProposalText(text)
			if normalizeProposalText(folded) != folded {
				t.Fatalf("skeleton did not converge for U+%04X: %U -> %U -> %U", r, []rune(text), []rune(folded), []rune(normalizeProposalText(folded)))
			}
		}
	}
}
