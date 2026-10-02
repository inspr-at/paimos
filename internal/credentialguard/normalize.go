// SPDX-License-Identifier: AGPL-3.0-only

package credentialguard

import (
	"strings"
	"unicode"

	"github.com/inspr-at/paimos/internal/unicodeguard"
	"golang.org/x/text/unicode/norm"
)

func ignoredFormat(r rune) bool {
	if unicode.Is(unicode.Cf, r) {
		return true
	}
	for _, span := range unicodeguard.DefaultIgnorables {
		if r < span[0] {
			return false
		}
		if r <= span[1] {
			return true
		}
	}
	return false
}

func compatibleText(text string) string {
	return norm.NFKC.String(strings.Map(func(r rune) rune {
		if ignoredFormat(r) {
			return -1
		}
		return r
	}, text))
}

// ASCII token bytes and case are meaningful to entropy and Basic decoding:
// UTS #39's ASCII substitutions (I -> l, 1 -> l, m -> rn) would erase prefixes,
// change entropy and corrupt Basic authorization values. Only non-ASCII glyphs
// are mapped; neither the input nor the mapping output is casefolded.
func confusableText(text string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(text) {
		if unicode.IsMark(r) || ignoredFormat(r) {
			continue
		}
		if mapped, ok := credentialConfusables[r]; ok {
			b.WriteString(mapped)
		} else {
			writeCredentialRune(&b, r)
		}
	}
	return b.String()
}

func writeCredentialRune(b *strings.Builder, r rune) {
	if unicode.IsMark(r) || ignoredFormat(r) {
		return
	}
	if unicode.Is(unicode.Pd, r) {
		b.WriteByte('-')
	} else {
		b.WriteRune(r)
	}
}

// Resolve chained mappings once. Preserve ASCII endpoints, and choose one
// deterministic representative for cycles, as the quotation normalizer does.
var credentialConfusables = func() map[rune]string {
	table := make(map[rune]string, len(unicodeguard.Confusables))
	for r, target := range unicodeguard.Confusables {
		if r > unicode.MaxASCII {
			table[r] = target
		}
	}
	for r, target := range unicodeguard.SmallCapitals {
		table[r] = target
	}
	resolved := make(map[rune]string, len(table))
	for r := range table {
		current := string(r)
		seen := map[string]int{}
		var states []string
		for {
			if start, ok := seen[current]; ok {
				best := current
				for _, state := range states[start:] {
					if state < best {
						best = state
					}
				}
				resolved[r] = best
				break
			}
			if len(states) >= 32 || len(current) > 1024 {
				panic("Unicode credential guard table does not converge")
			}
			seen[current] = len(states)
			states = append(states, current)
			var b strings.Builder
			for _, c := range norm.NFKD.String(current) {
				if unicode.IsMark(c) || ignoredFormat(c) {
					continue
				}
				if mapped, ok := table[c]; ok {
					b.WriteString(mapped)
				} else {
					writeCredentialRune(&b, c)
				}
			}
			current = b.String()
		}
	}
	return resolved
}()
