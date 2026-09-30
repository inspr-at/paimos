// SPDX-License-Identifier: AGPL-3.0-only

// Package codename names releases (AEON-430). Every release sequence gets an
// alliterative science-fiction codename such as "Amber Aurora", "Brisk Binary"
// or "Galactic Gyroscope", from the frozen, append-only lists in words.txt.
//
// # The letter
//
// The letter is (sequence - 1) mod 26: release 1 is A, 26 is Z and 27 starts
// the next A round, so neighbouring releases always start differently and a
// letter's round k is the release 26k + letter + 1.
//
// # Within a letter
//
// Round k of a letter takes the first combination, in a fixed diagonal order,
// that this letter has not used yet and that is allowed at that round's
// sequence. The diagonal order walks adjective r with noun (r + q) mod n for
// q = 0, 1, …, so both words change from one round to the next, and the lists
// may have different lengths: every combination comes up exactly once. Nothing
// repeats until the letter's capacity (adjectives × nouns, less the denied
// combinations) is used up; only then does the letter start a second cycle.
//
// # Stability
//
// A word, a retirement or a denied combination belongs to the list version
// that added it, and takes effect only from that version's first sequence.
// Rounds before it see exactly the lists they saw when they were named, so a
// release's codename never changes when the lists grow; the golden file in
// testdata guards sequences 1 to 200. Codename is presentation only: the
// release is identified by its version, never by its name.
package codename

import (
	_ "embed"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

//go:embed words.txt
var wordsFile string

var defaultLists = mustParse(wordsFile)

// Codename is the codename of a release sequence, or "" below 1.
func Codename(sequence int) string { return defaultLists.Name(sequence) }

// Version is the newest word-list version.
func Version() int { return defaultLists.version }

// Capacity is how many distinct names the newest lists hold for new releases.
func Capacity() int { return defaultLists.Capacity(defaultLists.latest()) }

// LetterCapacity is how many distinct names the newest lists hold for letter.
func LetterCapacity(letter byte) int {
	return defaultLists.letterCapacity(int(letter-'A'), defaultLists.latest())
}

// word is one list entry. It is eligible from since up to, not including, retired.
type word struct {
	text           string
	since, retired int
}

func (w word) eligible(seq int) bool { return w.since <= seq && (w.retired == 0 || seq < w.retired) }

type letterLists struct{ adj, noun []word }

// Lists are parsed word lists. The zero value holds no words.
type Lists struct {
	version int
	froms   []int // first sequence of each version, ascending
	letters [26]letterLists
	// deny maps an adjective and noun index pair of a letter to the first
	// sequence it is denied from.
	deny [26]map[[2]int]int
}

var wordRE = regexp.MustCompile(`^[A-Z][a-z]{1,11}$`)

// Parse reads a word-list file. See words.txt for the format.
func Parse(src string) (*Lists, error) {
	l := &Lists{}
	for i := range l.deny {
		l.deny[i] = map[[2]int]int{}
	}
	from := 0
	for n, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		fail := func(format string, args ...any) error {
			return fmt.Errorf("words line %d: %s", n+1, fmt.Sprintf(format, args...))
		}
		if f[0] == "version" {
			if len(f) != 4 || f[2] != "from" {
				return nil, fail("want version N from S")
			}
			v, err1 := strconv.Atoi(f[1])
			s, err2 := strconv.Atoi(f[3])
			if err1 != nil || err2 != nil || v != l.version+1 || s < 1 || (v == 1) != (s == 1) || s <= from {
				return nil, fail("version %s from %s must follow version %d from %d", f[1], f[3], l.version, from)
			}
			l.version, from = v, s
			l.froms = append(l.froms, s)
			continue
		}
		if l.version == 0 {
			return nil, fail("a version line must come first")
		}
		switch {
		case len(f[0]) == 1 && f[0][0] >= 'A' && f[0][0] <= 'Z':
			if len(f) < 3 || (f[1] != "adj" && f[1] != "noun") {
				return nil, fail("want %s adj|noun Word …", f[0])
			}
			li := int(f[0][0] - 'A')
			for _, w := range f[2:] {
				if !wordRE.MatchString(w) || w[0] != f[0][0] {
					return nil, fail("%q is not one capitalised word starting with %s", w, f[0])
				}
				if _, _, ok := l.find(li, w); ok {
					return nil, fail("%q is already listed", w)
				}
				if f[1] == "adj" {
					l.letters[li].adj = append(l.letters[li].adj, word{text: w, since: from})
				} else {
					l.letters[li].noun = append(l.letters[li].noun, word{text: w, since: from})
				}
			}
		case f[0] == "retire":
			if len(f) != 2 {
				return nil, fail("want retire Word")
			}
			li := int(f[1][0] - 'A')
			list, i, ok := l.find(li, f[1])
			if !ok || (*list)[i].retired != 0 {
				return nil, fail("%q is not a listed, active word", f[1])
			}
			(*list)[i].retired = from
		case f[0] == "deny":
			if len(f) != 3 || f[1][0] != f[2][0] {
				return nil, fail("want deny Adjective Noun with one letter")
			}
			li := int(f[1][0] - 'A')
			a := slices.IndexFunc(l.letters[li].adj, func(w word) bool { return w.text == f[1] })
			b := slices.IndexFunc(l.letters[li].noun, func(w word) bool { return w.text == f[2] })
			if a < 0 || b < 0 {
				return nil, fail("deny %s %s: not a listed adjective and noun", f[1], f[2])
			}
			if _, dup := l.deny[li][[2]int{a, b}]; dup {
				return nil, fail("deny %s %s is already listed", f[1], f[2])
			}
			l.deny[li][[2]int{a, b}] = from
		default:
			return nil, fail("unknown line %q", f[0])
		}
	}
	if l.version == 0 {
		return nil, fmt.Errorf("words: no version line")
	}
	for li, ll := range l.letters {
		if len(ll.adj) == 0 || len(ll.noun) == 0 {
			return nil, fmt.Errorf("words: letter %c needs at least one adjective and one noun", 'A'+li)
		}
	}
	return l, nil
}

func mustParse(src string) *Lists {
	l, err := Parse(src)
	if err != nil {
		panic(err)
	}
	return l
}

// find locates a word in either list of a letter.
func (l *Lists) find(li int, text string) (*[]word, int, bool) {
	if li < 0 || li >= 26 {
		return nil, 0, false
	}
	for _, list := range []*[]word{&l.letters[li].adj, &l.letters[li].noun} {
		if i := slices.IndexFunc(*list, func(w word) bool { return w.text == text }); i >= 0 {
			return list, i, true
		}
	}
	return nil, 0, false
}

// latest is the first sequence of the newest version: every word, retirement
// and denial is in effect from there.
func (l *Lists) latest() int { return l.froms[len(l.froms)-1] }

// epoch is the index of the version in effect at seq.
func (l *Lists) epoch(seq int) int {
	e := 0
	for i, s := range l.froms {
		if s <= seq {
			e = i
		}
	}
	return e
}

func eligible(list []word, seq int) []int {
	out := []int{}
	for i, w := range list {
		if w.eligible(seq) {
			out = append(out, i)
		}
	}
	return out
}

func (l *Lists) denied(li, a, n, seq int) bool {
	s, ok := l.deny[li][[2]int{a, n}]
	return ok && s <= seq
}

// Capacity is how many distinct names all letters hold at seq.
func (l *Lists) Capacity(seq int) int {
	total := 0
	for li := range l.letters {
		total += l.letterCapacity(li, seq)
	}
	return total
}

func (l *Lists) letterCapacity(li, seq int) int {
	if li < 0 || li >= 26 {
		return 0
	}
	ea, en := eligible(l.letters[li].adj, seq), eligible(l.letters[li].noun, seq)
	n := len(ea) * len(en)
	for _, a := range ea {
		for _, b := range en {
			if l.denied(li, a, b, seq) {
				n--
			}
		}
	}
	return n
}

// Name is the codename of a release sequence, or "" below 1.
func (l *Lists) Name(sequence int) string {
	if sequence < 1 {
		return ""
	}
	names := l.letterNames((sequence-1)%26, (sequence-1)/26+1)
	return names[len(names)-1]
}

// Names are the codenames of sequences 1 to n.
func (l *Lists) Names(n int) []string {
	out := make([]string, n)
	for li := 0; li < 26 && li < n; li++ {
		for k, name := range l.letterNames(li, (n-1-li)/26+1) {
			out[li+26*k] = name
		}
	}
	return out
}

// letterNames are the names of a letter's first rounds, in order.
func (l *Lists) letterNames(li, rounds int) []string {
	names := make([]string, 0, rounds)
	used := map[[2]int]bool{}
	var ea, en []int
	cursor, epoch := 0, -1
	for k := 0; k < rounds; k++ {
		seq := li + 1 + 26*k
		if e := l.epoch(seq); e != epoch {
			// New words, retirements or denials: rescan the new order from its start.
			epoch, cursor = e, 0
			ea, en = eligible(l.letters[li].adj, seq), eligible(l.letters[li].noun, seq)
		}
		pick, ok := [2]int{}, false
		for pass := 0; pass < 2 && !ok; pass++ {
			for total := len(ea) * len(en); cursor < total; cursor++ {
				r, q := cursor%len(ea), cursor/len(ea)
				p := [2]int{ea[r], en[(r+q)%len(en)]}
				if !used[p] && !l.denied(li, p[0], p[1], seq) {
					pick, ok = p, true
					cursor++
					break
				}
			}
			if !ok {
				// Capacity used up: the letter starts its next cycle.
				clear(used)
				cursor = 0
			}
		}
		if !ok {
			names = append(names, "")
			continue
		}
		used[pick] = true
		names = append(names, l.letters[li].adj[pick[0]].text+" "+l.letters[li].noun[pick[1]].text)
	}
	return names
}
