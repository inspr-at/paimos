// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"crypto/sha256"
	_ "embed"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Increment when the comparison algorithm changes. Table and Unicode library
// upgrades also invalidate every saved corpus, even when its git pin is unchanged.
const normalizerCodeVersion = "aeon-guard-normalizer-1"

// Including the code itself also invalidates a corpus when a future edit
// accidentally forgets to increment the human-readable algorithm revision.
//
//go:embed normalize.go
var normalizerSource string

func normalizerVersion() [32]byte {
	return sha256.Sum256([]byte(normalizerCodeVersion + "\x00" + normalizerSource + "\x00" + unicodeGuardTableSHA256 + "\x00" + unicode.Version + "\x00" + norm.Version))
}

// normalizeProposalText is only for comparison; proposed git bytes are kept.
// Strip Default_Ignorable_Code_Point BEFORE any decomposition or separator
// handling (notably soft hyphen, Hangul fillers, variation and bidi controls).
// All private/public corpus text and identity literals share this pipeline.
func normalizeProposalText(text string) string {
	text = strings.Map(func(r rune) rune {
		if isIgnoredFormat(r) {
			return -1
		}
		return r
	}, text)
	text = norm.NFKD.String(text)
	text = strings.Map(func(r rune) rune {
		if unicode.IsMark(r) {
			return -1
		}
		return r
	}, text)
	text = cases.Fold().String(text)
	var b strings.Builder
	for _, r := range text {
		if mapped, ok := canonicalConfusables[r]; ok {
			b.WriteString(mapped)
		} else if unicode.Is(unicode.Pd, r) {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func skeletonPass(text string) string {
	var b strings.Builder
	for _, r := range text {
		if latin, ok := unicodeSmallCapitals[r]; ok {
			for _, c := range latin {
				writeSkeleton(&b, c)
			}
		} else {
			writeSkeleton(&b, r)
		}
	}
	// Skeletons can introduce uppercase letters or combining marks. Apply the
	// same caseless/accentless policy to their output as to the input.
	text = cases.Fold().String(norm.NFKD.String(b.String()))
	text = strings.Map(func(r rune) rune {
		if unicode.IsMark(r) || isIgnoredFormat(r) {
			return -1
		}
		if unicode.Is(unicode.Pd, r) {
			return '-'
		}
		return r
	}, text)
	return text
}

// Derived deterministically from the pinned UTS table. An explicit lower-case
// mapping wins over an alias; subsequent skeleton passes resolve its target.
var caselessConfusables = func() map[rune]string {
	table := make(map[rune]string, len(unicodeConfusables))
	keys := make([]int, 0, len(unicodeConfusables))
	for r, s := range unicodeConfusables {
		table[r] = s
		keys = append(keys, int(r))
	}
	sort.Ints(keys)
	for _, key := range keys {
		runes := []rune(cases.Fold().String(string(rune(key))))
		if len(runes) == 1 {
			if _, ok := table[runes[0]]; !ok {
				table[runes[0]] = unicodeConfusables[rune(key)]
			}
		}
	}
	return table
}()

// Compile the caseless skeleton to a fixed point once, rather than iterating
// per request. Casefold/skeleton cycles (e.g. Cherokee upper/lowercase) choose
// the same lexical representative from either entry point. The bounded build
// fails closed if a future data upgrade introduces unbounded expansion.
var canonicalConfusables = func() map[rune]string {
	table := make(map[rune]string, len(caselessConfusables))
	keys := make(map[rune]bool, len(caselessConfusables))
	for r, target := range caselessConfusables {
		keys[r] = true
		for _, c := range target + cases.Fold().String(norm.NFKD.String(target)) {
			keys[c] = true
		}
	}
	for r := range unicodeSmallCapitals {
		keys[r] = true
	}
	for r := range keys {
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
				table[r] = best
				break
			}
			if len(states) >= 32 || len(current) > 1024 {
				panic("Unicode guard table does not converge")
			}
			seen[current] = len(states)
			states = append(states, current)
			current = skeletonPass(current)
		}
	}
	return table
}()

func writeSkeleton(b *strings.Builder, r rune) {
	if mapped, ok := caselessConfusables[r]; ok {
		b.WriteString(mapped)
	} else {
		b.WriteRune(r)
	}
}

func isIgnoredFormat(r rune) bool {
	for _, span := range unicodeDefaultIgnorables {
		if r < span[0] {
			return false
		}
		if r <= span[1] {
			return true
		}
	}
	return false
}

func proposalWords(text string) []string {
	return strings.FieldsFunc(normalizeProposalText(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// Compatibility decomposition intentionally removes accents. For a few UTS
// mappings it destroys the entire glyph (a spacing accent -> space + mark),
// or contradicts its casefold (a tone letter shaped like a digit). Those
// cannot be reconciled by the requested pipeline. Refuse that entire derived
// class rather than choosing an interpretation that can leak a quotation.
func ambiguousPublicText(text string) bool {
	for _, r := range text {
		if r <= unicode.MaxASCII || isIgnoredFormat(r) {
			continue
		}
		mapped, ok := unicodeConfusables[r]
		if !ok || !asciiAlphanumeric(mapped) {
			continue
		}
		if normalizeProposalText(string(r)) != normalizeProposalText(mapped) {
			return true
		}
	}
	return false
}

func asciiAlphanumeric(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
