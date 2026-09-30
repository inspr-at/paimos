// SPDX-License-Identifier: AGPL-3.0-only

// Package codename names releases (AEON-430). Every release sequence gets an
// alliterative science-fiction codename such as "Cyan Comet", "Brisk Beacon"
// or "Galactic Gyroscope", from the frozen, append-only lists in words.txt.
//
// # The letter
//
// Only letters rich enough for thousands of good names take part: words.txt
// declares that cycle once, in version 1 (15 letters, A B C D E F G H I L M P R
// S T). The letter of a sequence is cycle[(sequence - 1) mod len(cycle)], so
// release 1 is A, neighbouring releases always start differently, and round k
// of the letter at cycle position p is the release len(cycle)·k + p + 1.
//
// # Within a letter
//
// Round k of a letter takes the first combination, in a fixed order, that this
// letter has not used yet and that is allowed at that round's sequence. The
// order puts short names first: step by step it takes the cheapest name left,
// where a name costs its letters plus 2 for every earlier use of each of its
// words, so the early rounds ("Cool Chip", "Solar Star") are short and still
// vary their words, and long names ("Intuitive Interface") come years later.
// Ties go by the FNV-1a hash of the name, then alphabetically. Nothing repeats
// until the letter's capacity (adjectives × nouns, less the denied
// combinations) is used up; only then does the letter start a second cycle.
// The first repeat is at about len(cycle) × the smallest letter capacity.
//
// # Stability
//
// A word, a retirement or a denied combination belongs to the list version
// that added it, and takes effect only from that version's first sequence.
// Rounds before it see exactly the lists they saw when they were named, so a
// release's codename never changes when the lists grow: from a new version on,
// its order is rebuilt over all words then eligible and the used names are
// skipped, so new short names come up early and long ones later. The golden
// file in testdata guards sequences 1 to 200. Codename is presentation only:
// the release is identified by its version, never by its name.
package codename

import (
	_ "embed"
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// LetterCapacity is how many distinct names the newest lists hold for letter;
// 0 for a letter outside the cycle.
func LetterCapacity(letter byte) int {
	return defaultLists.letterCapacity(int(letter)-'A', defaultLists.latest())
}

// Named is a release sequence and the codename already recorded for it: the
// name stamped into its version.json or shown for it. Name is "" when none was
// recorded, as for releases reserved before codenames.
type Named struct {
	Sequence int
	Name     string
}

// Guard fails when the newest lists would rename a release that already has a
// sequence; see Lists.Guard.
func Guard(named []Named) error { return defaultLists.Guard(named) }

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
	cycle   []int // letter indexes in cycle order
	letters [26]letterLists
	// deny maps an adjective and noun index pair of a letter to the first
	// sequence it is denied from.
	deny [26]map[[2]int]int
	// orders holds, per letter and version, the allowed adjective and noun
	// index pairs in naming order; built once, on first use.
	orders     [26][][][2]int
	ordersOnce sync.Once
}

var wordRE = regexp.MustCompile(`^[A-Z][a-z]{1,11}$`)

// Parse reads a word-list file. See words.txt for the format.
func Parse(src string) (*Lists, error) {
	l := &Lists{}
	for i := range l.deny {
		l.deny[i] = map[[2]int]int{}
	}
	inCycle := [26]bool{}
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
		case f[0] == "cycle":
			// The cycle decides every sequence's letter, so only version 1
			// declares it, once, before any word.
			if l.version != 1 || l.cycle != nil || len(f) < 2 {
				return nil, fail("want one cycle L L … line at the start of version 1")
			}
			for _, c := range f[1:] {
				if len(c) != 1 || c[0] < 'A' || c[0] > 'Z' || inCycle[c[0]-'A'] {
					return nil, fail("cycle: %q is not a new capital letter", c)
				}
				inCycle[c[0]-'A'] = true
				l.cycle = append(l.cycle, int(c[0]-'A'))
			}
		case len(f[0]) == 1 && f[0][0] >= 'A' && f[0][0] <= 'Z':
			if len(f) < 3 || (f[1] != "adj" && f[1] != "noun") {
				return nil, fail("want %s adj|noun Word …", f[0])
			}
			li := int(f[0][0] - 'A')
			if !inCycle[li] {
				return nil, fail("letter %s is not in the cycle", f[0])
			}
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
	if l.cycle == nil {
		return nil, fmt.Errorf("words: no cycle line")
	}
	for _, li := range l.cycle {
		ll := l.letters[li]
		if len(ll.adj) == 0 || len(ll.noun) == 0 {
			return nil, fmt.Errorf("words: letter %c needs at least one adjective and one noun", 'A'+li)
		}
	}
	return l, nil
}

// letterOrder is a letter's naming order in the version in effect at seq.
func (l *Lists) letterOrder(li, seq int) [][2]int {
	l.ordersOnce.Do(func() {
		for _, li := range l.cycle {
			for _, s := range l.froms {
				l.orders[li] = append(l.orders[li], l.order(li, s))
			}
		}
	})
	if len(l.orders[li]) == 0 {
		return nil
	}
	return l.orders[li][l.epoch(seq)]
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

// spread is what one earlier use of a word adds to a name's cost; see order.
const spread = 2

// order is the naming order of a letter from sequence seq, the first of a
// version, over every eligible, allowed combination. Step by step it takes the
// cheapest name not yet in the order. A name costs its letters (both words
// together) plus spread for every time each of its words is already in the
// order, so short names come first while the early rounds still vary their
// words. Ties go by the FNV-1a hash of "Adjective Noun", then alphabetically.
// This is part of the frozen naming: never change it.
func (l *Lists) order(li, seq int) [][2]int {
	ll := l.letters[li]
	// A name's cost is its adjective's cost plus its noun's: a word costs its
	// letters plus spread for each use so far.
	adjCost, nounCost := make([]int, len(ll.adj)), make([]int, len(ll.noun))
	adjLeft, nounLeft := make([]int, len(ll.adj)), make([]int, len(ll.noun))
	open, hash := make([][]bool, len(ll.adj)), make([][]uint32, len(ll.adj))
	left := 0
	for a, aw := range ll.adj {
		adjCost[a] = len(aw.text)
		open[a], hash[a] = make([]bool, len(ll.noun)), make([]uint32, len(ll.noun))
		for n, nw := range ll.noun {
			nounCost[n] = len(nw.text)
			if aw.eligible(seq) && nw.eligible(seq) && !l.denied(li, a, n, seq) {
				open[a][n] = true
				hash[a][n] = fnv32a(aw.text + " " + nw.text)
				adjLeft[a]++
				nounLeft[n]++
				left++
			}
		}
	}
	// adjs and nouns hold the words with open names, cheapest first.
	sorted := func(cost, left []int) []int {
		var s []int
		for i := range cost {
			if left[i] > 0 {
				s = append(s, i)
			}
		}
		slices.SortStableFunc(s, func(x, y int) int { return cost[x] - cost[y] })
		return s
	}
	adjs, nouns := sorted(adjCost, adjLeft), sorted(nounCost, nounLeft)
	// used moves word i one spread up, keeping s sorted, and drops it once it
	// has no open name.
	used := func(s []int, i int, cost, left []int) []int {
		cost[i] += spread
		left[i]--
		k := slices.Index(s, i)
		if left[i] == 0 {
			return slices.Delete(s, k, k+1)
		}
		for ; k+1 < len(s) && cost[s[k+1]] < cost[i]; k++ {
			s[k] = s[k+1]
		}
		s[k] = i
		return s
	}
	name := func(a, n int) string { return ll.adj[a].text + " " + ll.noun[n].text }
	out := make([][2]int, 0, left)
	for ; left > 0; left-- {
		best, bestCost, bestHash := [2]int{-1, -1}, 0, uint32(0)
		for _, a := range adjs {
			if best[0] >= 0 && adjCost[a]+nounCost[nouns[0]] > bestCost {
				break
			}
			for _, n := range nouns {
				c := adjCost[a] + nounCost[n]
				if best[0] >= 0 && c > bestCost {
					break
				}
				if !open[a][n] {
					continue
				}
				h := hash[a][n]
				if best[0] < 0 || c < bestCost || h < bestHash || (h == bestHash && name(a, n) < name(best[0], best[1])) {
					best, bestCost, bestHash = [2]int{a, n}, c, h
				}
			}
		}
		out = append(out, best)
		open[best[0]][best[1]] = false
		adjs = used(adjs, best[0], adjCost, adjLeft)
		nouns = used(nouns, best[1], nounCost, nounLeft)
	}
	return out
}

func fnv32a(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
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

func (l *Lists) denied(li, a, n, seq int) bool {
	s, ok := l.deny[li][[2]int{a, n}]
	return ok && s <= seq
}

// Guard fails when these lists would rename a release that already has a
// sequence. A recorded name must still be its sequence's name. A release
// without one cannot be checked by name, so once there is a version after 1,
// the newest version must start above it: its "from S" must be above every
// sequence reserved or published before it was added. Version 1 is exempt.
func (l *Lists) Guard(named []Named) error {
	for _, r := range named {
		if r.Sequence < 1 {
			continue
		}
		if r.Name != "" {
			if now := l.Name(r.Sequence); now != r.Name {
				return fmt.Errorf("codename: release %d is %q but the lists now name it %q; never change a published name", r.Sequence, r.Name, now)
			}
		} else if l.version > 1 && r.Sequence >= l.latest() {
			return fmt.Errorf("codename: version %d from %d would rename release %d, which has no recorded codename; start it above every reserved sequence", l.version, l.latest(), r.Sequence)
		}
	}
	return nil
}

// Capacity is how many distinct names all letters hold at seq.
func (l *Lists) Capacity(seq int) int {
	total := 0
	for _, li := range l.cycle {
		total += l.letterCapacity(li, seq)
	}
	return total
}

func (l *Lists) letterCapacity(li, seq int) int {
	if li < 0 || li >= 26 {
		return 0
	}
	return len(l.letterOrder(li, seq))
}

// Name is the codename of a release sequence, or "" below 1.
func (l *Lists) Name(sequence int) string {
	if sequence < 1 {
		return ""
	}
	n := len(l.cycle)
	names := l.letterNames(l.cycle[(sequence-1)%n], (sequence-1)/n+1)
	return names[len(names)-1]
}

// Names are the codenames of sequences 1 to n.
func (l *Lists) Names(n int) []string {
	out := make([]string, n)
	c := len(l.cycle)
	for p := 0; p < c && p < n; p++ {
		for k, name := range l.letterNames(l.cycle[p], (n-1-p)/c+1) {
			out[p+c*k] = name
		}
	}
	return out
}

// letterNames are the names of a letter's first rounds, in order. The letter
// must be in the cycle.
func (l *Lists) letterNames(li, rounds int) []string {
	names := make([]string, 0, rounds)
	p := slices.Index(l.cycle, li)
	used := map[[2]int]bool{}
	var order [][2]int
	cursor, epoch := 0, -1
	for k := 0; k < rounds; k++ {
		seq := p + 1 + len(l.cycle)*k
		if e := l.epoch(seq); e != epoch {
			// New words, retirements or denials: rescan the new order from its start.
			epoch, cursor, order = e, 0, l.letterOrder(li, seq)
		}
		pick, ok := [2]int{}, false
		for pass := 0; pass < 2 && !ok; pass++ {
			for ; cursor < len(order); cursor++ {
				if !used[order[cursor]] {
					pick, ok = order[cursor], true
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
