// SPDX-License-Identifier: AGPL-3.0-only

package codename

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/golden.txt (only for a new, intentional list version)")

// The first 200 names are frozen: appending to the lists must never move them.
func TestGoldenFirst200(t *testing.T) {
	var b strings.Builder
	for i, name := range defaultLists.Names(200) {
		fmt.Fprintf(&b, "%d %s\n", i+1, name)
	}
	if *update {
		if err := os.WriteFile("testdata/golden.txt", []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Fatalf("codenames for sequences 1-200 changed; names are append-only:\n%s", diff(string(want), b.String()))
	}
	// One sequence at a time gives the same names as the whole run.
	for i, line := range strings.Split(strings.TrimSpace(string(want)), "\n") {
		if got := fmt.Sprintf("%d %s", i+1, Codename(i+1)); got != line {
			t.Fatalf("Codename(%d): %q, golden %q", i+1, got, line)
		}
	}
}

func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var out []string
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			out = append(out, fmt.Sprintf("- %s\n+ %s", a, b))
		}
		if len(out) == 10 {
			break
		}
	}
	return strings.Join(out, "\n")
}

// The version 1 block is frozen byte for byte; later versions only append.
func TestVersionBlocksAreAppendOnly(t *testing.T) {
	frozen := map[int]string{1: version1SHA256}
	blocks := map[int]string{}
	current := 0
	var b strings.Builder
	for _, line := range strings.Split(wordsFile, "\n") {
		var v, from int
		if _, err := fmt.Sscanf(line, "version %d from %d", &v, &from); err == nil {
			if current > 0 {
				blocks[current] = b.String()
			}
			current = v
			b.Reset()
		}
		if current > 0 {
			b.WriteString(line + "\n")
		}
	}
	blocks[current] = b.String()
	for v, want := range frozen {
		sum := sha256.Sum256([]byte(blocks[v]))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("word list version %d changed (sha256 %s, frozen %s): append a new version instead", v, got, want)
		}
	}
}

// version1SHA256 freezes the "version 1 from 1" block of words.txt.
const version1SHA256 = "5ef3eb48b372c5a72824a7fdc062a7a92f36d0d1c3352ddee094aa6d2970ac0a"

func TestLetterCycling(t *testing.T) {
	cycle := defaultLists.cycle
	names := defaultLists.Names(len(cycle) * 30)
	for i, name := range names {
		seq := i + 1
		letter := byte('A' + cycle[(seq-1)%len(cycle)])
		words := strings.Fields(name)
		if len(words) != 2 || words[0][0] != letter || words[1][0] != letter {
			t.Fatalf("sequence %d: %q, want two words starting with %c", seq, name, letter)
		}
		if i > 0 && names[i-1][0] == name[0] {
			t.Fatalf("sequences %d and %d both start with %c", seq-1, seq, name[0])
		}
	}
	if Codename(0) != "" || Codename(-3) != "" {
		t.Fatal("sequences below 1 have no codename")
	}
	if Codename(1)[0] != 'A' || Codename(15)[0] != 'T' || Codename(16)[0] != 'A' {
		t.Fatal("release 1 is A, 15 is T, 16 starts the next A round")
	}
}

// Within a letter nothing repeats until its capacity is used up; then the
// next cycle starts with the letter's first name again. Denied combinations
// never come up.
func TestNoRepeatsWithinCapacity(t *testing.T) {
	total := 0
	for _, li := range defaultLists.cycle {
		capacity := defaultLists.letterCapacity(li, 1)
		total += capacity
		names := defaultLists.letterNames(li, capacity+1)
		seen := map[string]bool{}
		for k, name := range names[:capacity] {
			if name == "" || seen[name] {
				t.Fatalf("letter %c round %d: %q is empty or repeated within capacity %d", 'A'+li, k, name, capacity)
			}
			seen[name] = true
			words := strings.Fields(name)
			if denied(li, words[0], words[1]) {
				t.Fatalf("letter %c round %d: denied %q was picked", 'A'+li, k, name)
			}
		}
		if names[capacity] != names[0] {
			t.Fatalf("letter %c: after %d names the cycle restarts with %q, got %q", 'A'+li, capacity, names[0], names[capacity])
		}
	}
	if total != Capacity() {
		t.Fatalf("letter capacities sum to %d, Capacity() = %d", total, Capacity())
	}
}

// Only letters with at least 45 adjectives, 45 nouns and 2,000 allowed names
// are in the cycle, and the smallest of them lasts at least 40,000 releases:
// four to five years at 20 to 25 releases a day before any name repeats.
func TestCycleCapacity(t *testing.T) {
	l := defaultLists
	smallest, firstRepeat := 0, 0
	for p, li := range l.cycle {
		ll, capacity := l.letters[li], l.letterCapacity(li, l.latest())
		if len(ll.adj) < 45 || len(ll.noun) < 45 || capacity < 2000 {
			t.Errorf("letter %c: %d adjectives, %d nouns, %d names; the cycle needs 45, 45 and 2,000", 'A'+li, len(ll.adj), len(ll.noun), capacity)
		}
		if smallest == 0 || capacity < smallest {
			smallest = capacity
		}
		// The letter's first repeat is its round number capacity.
		if repeat := p + 1 + len(l.cycle)*capacity; firstRepeat == 0 || repeat < firstRepeat {
			firstRepeat = repeat
		}
	}
	if len(l.cycle)*smallest < 40000 || firstRepeat < 40000 {
		t.Fatalf("%d letters × smallest capacity %d = %d, first repeat at %d; want at least 40,000", len(l.cycle), smallest, len(l.cycle)*smallest, firstRepeat)
	}
	for li := range 26 {
		if !slices.Contains(l.cycle, li) && (len(l.letters[li].adj) > 0 || len(l.letters[li].noun) > 0 || LetterCapacity(byte('A'+li)) != 0) {
			t.Errorf("letter %c is outside the cycle but has words", 'A'+li)
		}
	}
	t.Logf("%d letters, smallest capacity %d, capacity %d, first repeat at sequence %d", len(l.cycle), smallest, Capacity(), firstRepeat)
}

// Short names come first: the first 200 releases and every letter's first
// 20 rounds are much shorter than the average name, and those rounds still
// vary their words.
func TestShortNamesFirst(t *testing.T) {
	l := defaultLists
	letters := func(name string) int { return len(name) - 1 }
	early := 0
	for _, name := range l.Names(200) {
		early += letters(name)
	}
	all, count := 0, 0
	for _, li := range l.cycle {
		order := l.letterOrder(li, 1)
		letterAll := 0
		for _, p := range order {
			letterAll += len(l.letters[li].adj[p[0]].text) + len(l.letters[li].noun[p[1]].text)
		}
		all, count = all+letterAll, count+len(order)
		first, uses := 0, map[string]int{}
		names := l.letterNames(li, 20)
		for _, name := range names {
			first += letters(name)
			for _, w := range strings.Fields(name) {
				if uses[w]++; uses[w] > 3 {
					t.Errorf("letter %c: %q is in more than 3 of the first 20 names: %v", 'A'+li, w, names)
				}
			}
		}
		if avg, firstAvg := float64(letterAll)/float64(len(order)), float64(first)/20; firstAvg > avg-3 {
			t.Errorf("letter %c: the first 20 names average %.1f letters, all %.1f", 'A'+li, firstAvg, avg)
		}
	}
	earlyAvg, allAvg := float64(early)/200, float64(all)/float64(count)
	if earlyAvg > allAvg-3 {
		t.Fatalf("sequences 1-200 average %.2f letters, all names %.2f: short names must come first", earlyAvg, allAvg)
	}
	t.Logf("sequences 1-200 average %.2f letters, all names %.2f", earlyAvg, allAvg)
}

// The naming order is deterministic and cheapest first: each name costs its
// letters plus spread per earlier use of each word.
func TestOrderCosts(t *testing.T) {
	l := defaultLists
	for _, li := range l.cycle {
		order := l.letterOrder(li, 1)
		if again := l.order(li, 1); !slices.Equal(order, again) {
			t.Fatalf("letter %c: the order is not deterministic", 'A'+li)
		}
		adjUses, nounUses := map[int]int{}, map[int]int{}
		cost := func(p [2]int) int {
			return len(l.letters[li].adj[p[0]].text) + len(l.letters[li].noun[p[1]].text) + spread*(adjUses[p[0]]+nounUses[p[1]])
		}
		for i, p := range order {
			// No later name is cheaper at this step.
			for _, q := range order[i+1 : min(len(order), i+40)] {
				if cost(q) < cost(p) {
					t.Fatalf("letter %c step %d: %v costs %d, later %v only %d", 'A'+li, i, p, cost(p), q, cost(q))
				}
			}
			adjUses[p[0]]++
			nounUses[p[1]]++
		}
	}
}

func denied(li int, adj, noun string) bool {
	for p := range defaultLists.deny[li] {
		if defaultLists.letters[li].adj[p[0]].text == adj && defaultLists.letters[li].noun[p[1]].text == noun {
			return true
		}
	}
	return false
}

// Words carry no rude fragment, no negative word and no proper noun we know of.
var (
	rudeFragments = []string{"anal", "anus", "bitch", "boob", "butt", "cock", "crap", "cum", "cunt", "dick", "dildo", "fag", "fuck", "homo", "jizz", "kkk", "nazi", "nigg", "nude", "orgasm", "penis", "piss", "porn", "prick", "pube", "puss", "rape", "semen", "sex", "shit", "slut", "spunk", "tits", "turd", "twat", "vagina", "wank", "whore"}
	// strongFragments are also caught where the two words run together.
	strongFragments = []string{"anal", "cock", "cunt", "dick", "fag", "fuck", "kkk", "nazi", "nigg", "piss", "porn", "shit", "slut", "twat", "wank", "whore"}
	negativeWords   = []string{"abort", "bleak", "blight", "bomb", "broken", "bug", "collapse", "corona", "crash", "crater", "dark", "dead", "death", "decay", "demon", "die", "doom", "dread", "fail", "fatal", "final", "gloom", "grave", "grim", "hell", "kill", "last", "lost", "missile", "nuclear", "plague", "poison", "rot", "ruin", "sad", "sick", "sink", "toxic", "venom", "virus", "void", "war", "weapon", "wreck"}
	properNouns     = []string{"Apollo", "Cassini", "Einstein", "Galileo", "Gemini", "Hubble", "Jupiter", "Kepler", "Mars", "Mercury", "Newton", "Orion", "Saturn", "Sirius", "Sputnik", "Tesla", "Titan", "Uranus", "Vega", "Venus", "Voyager"}
	// offTheme words are brands or products, fantasy or medieval, or pastoral:
	// the lists are pure science fiction.
	offTheme = []string{
		"Android", "Avatar", "Codex", "Copilot", "Cursor", "Droid", "Firefly", "Sprite", "Tailwind",
		"Argent", "Ark", "Astrolabe", "Dwarf", "Exalted", "Firmament", "Forge", "Gallant", "Genesis", "Gnomon",
		"Heavenly", "Hourglass", "Lantern", "Realm", "Regal", "Revered", "Royal", "Sovereign",
		"Almanac", "Alpine", "Balmy", "Bloom", "Blooming", "Bountiful", "Breezy", "Budding", "Dappled", "Dewy",
		"Evergreen", "Fertile", "Flaxen", "Flourishing", "Fountain", "Garden", "Harbor", "Harvest", "Haven", "Hazel",
		"Homestead", "Idyllic", "Island", "Lavender", "Lilac", "Lush", "Meadow", "Orchard", "Perennial", "Rainbow",
		"Russet", "Saffron", "Sundial", "Sunny", "Tawny", "Tropical",
	}
)

// brands reads brands.txt: the one-word names and the
// two-word names, lower case.
func brands(t *testing.T) (words map[string]bool, pairs []string) {
	t.Helper()
	raw, err := os.ReadFile("brands.txt")
	if err != nil {
		t.Fatal(err)
	}
	words = map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		name := strings.ToLower(strings.TrimSpace(line))
		switch {
		case name == "" || strings.HasPrefix(name, "#"):
		case strings.Contains(name, " "):
			pairs = append(pairs, name)
		default:
			words[name] = true
		}
	}
	if len(words)+len(pairs) < 300 {
		t.Fatalf("brands.txt holds only %d names", len(words)+len(pairs))
	}
	return words, pairs
}

func TestWordQuality(t *testing.T) {
	brandWords, brandPairs := brands(t)
	checked, hits := 0, 0
	for li, ll := range defaultLists.letters {
		for _, w := range append(append([]word{}, ll.adj...), ll.noun...) {
			checked++
			lower := strings.ToLower(w.text)
			if brandWords[lower] {
				hits++
				t.Errorf("letter %c: %q is a brand or product name (brands.txt)", 'A'+li, w.text)
			}
			for _, bad := range rudeFragments {
				if strings.Contains(lower, bad) {
					t.Errorf("letter %c: %q contains %q", 'A'+li, w.text, bad)
				}
			}
			for _, bad := range negativeWords {
				if lower == bad {
					t.Errorf("letter %c: %q is negative (%q)", 'A'+li, w.text, bad)
				}
			}
			for _, name := range properNouns {
				if w.text == name {
					t.Errorf("letter %c: %q is a proper noun", 'A'+li, w.text)
				}
			}
			if slices.ContainsFunc(offTheme, func(o string) bool { return strings.EqualFold(o, w.text) }) {
				hits++
				t.Errorf("letter %c: %q is off theme (a brand, fantasy or pastoral)", 'A'+li, w.text)
			}
		}
	}
	// A two-word brand the lists can form must be denied.
	for _, pair := range brandPairs {
		words := strings.Fields(pair)
		if len(words) != 2 || words[0][0] != words[1][0] {
			continue
		}
		li := int(strings.ToUpper(words[0])[0] - 'A')
		if li < 0 || li >= 26 {
			continue
		}
		adj, noun := findFold(li, words[0], true), findFold(li, words[1], false)
		if adj != "" && noun != "" && !denied(li, adj, noun) {
			hits++
			t.Errorf("%s %s is a brand (brands.txt); add \"deny %s %s\"", adj, noun, adj, noun)
		}
	}
	t.Logf("brand screen: %d words checked against %d names and %d two-word names, %d hits", checked, len(brandWords), len(brandPairs), hits)
}

// findFold is the listed spelling of an adjective or noun of letter li that
// equals w ignoring case, or "".
func findFold(li int, w string, adj bool) string {
	list := defaultLists.letters[li].noun
	if adj {
		list = defaultLists.letters[li].adj
	}
	for _, x := range list {
		if strings.EqualFold(x.text, w) {
			return x.text
		}
	}
	return ""
}

// Every combination is scanned: a strong rude fragment where the words run
// together, a known unlucky phrase, or a repeated stem ("Planetary Planet",
// "Hypersonic Hyperdrive": five shared first letters, or one word starting
// with the other) must be on the deny list, so the name can never be picked.
var unluckyPhrases = []string{"golden gate", "golden gateway", "massive meteor", "twin tower", "eternal eclipse", "pulsing probe", "playful probe", "potent probe"}

func TestCombinationDenyList(t *testing.T) {
	scanned := 0
	for li, ll := range defaultLists.letters {
		for _, a := range ll.adj {
			for _, n := range ll.noun {
				scanned++
				name := strings.ToLower(a.text + " " + n.text)
				joined := strings.ReplaceAll(name, " ", "")
				var why string
				for _, bad := range strongFragments {
					if strings.Contains(joined, bad) {
						why = "rude fragment " + bad
					}
				}
				for _, phrase := range unluckyPhrases {
					if strings.HasPrefix(name, phrase) {
						why = "unlucky phrase"
					}
				}
				la, ln := strings.ToLower(a.text), strings.ToLower(n.text)
				if sharedPrefix(la, ln) >= 5 || strings.HasPrefix(ln, la) || strings.HasPrefix(la, ln) {
					why = "repeated stem"
				}
				if why != "" && !denied(li, a.text, n.text) {
					t.Errorf("%s %s: %s; add \"deny %s %s\" in a new list version", a.text, n.text, why, a.text, n.text)
				}
			}
		}
	}
	if scanned < 45000 {
		t.Fatalf("scanned only %d combinations", scanned)
	}
	for _, phrase := range unluckyPhrases {
		words := strings.Fields(phrase)
		li := int(strings.ToUpper(words[0])[0] - 'A')
		adj := strings.ToUpper(words[0][:1]) + words[0][1:]
		noun := strings.ToUpper(words[1][:1]) + words[1][1:]
		if _, _, ok := defaultLists.find(li, adj); ok {
			if _, _, ok := defaultLists.find(li, noun); ok && !denied(li, adj, noun) {
				t.Errorf("%s %s is listed but not denied", adj, noun)
			}
		}
	}
}

func sharedPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// Lists of different lengths still enumerate every combination exactly once,
// deterministically and shortest first, before a letter repeats.
func TestUnevenLists(t *testing.T) {
	l := mustParse(minimal("A adj Amber Astral Atomic\nA noun Aurora Array\n"))
	got := l.letterNames(0, 7)
	seen := map[string]bool{}
	for _, name := range got[:6] {
		seen[name] = true
	}
	if got[0] != "Amber Array" || len(seen) != 6 || got[6] != got[0] {
		t.Fatalf("uneven lists: %v, want the 6 names once, the shortest first, then a repeat", got)
	}
	// Axis is the shortest noun, then Array; each use of Amber adds 4, so
	// Aurora and Apogee (both 6 letters) follow, and the letter starts over.
	l = mustParse(minimal("A adj Amber\nA noun Aurora Array Axis Apogee\n"))
	got = l.letterNames(0, 5)
	if got[0] != "Amber Axis" || got[1] != "Amber Array" || got[4] != "Amber Axis" || !slices.Contains(got[2:4], "Amber Aurora") || !slices.Contains(got[2:4], "Amber Apogee") {
		t.Fatalf("one adjective, four nouns: %v", got)
	}
	if got := l.Name(1 + 3); got != "Amber Array" {
		t.Fatalf("Name(4) = %q, want the second A name", got)
	}
}

// Appending a version with new words, a retirement and a denial leaves every
// earlier sequence's name as it was, and changes only later ones.
func TestAppendingKeepsOldNames(t *testing.T) {
	base := minimal("A adj Amber Astral Atomic\nA noun Aurora Array Axis\n")
	before := mustParse(base)
	after := mustParse(base + "version 2 from 11\nA adj Azure\nA noun Apogee\nretire Array\ndeny Atomic Axis\n")
	for seq := 1; seq < 11; seq++ {
		if before.Name(seq) != after.Name(seq) {
			t.Fatalf("sequence %d changed from %q to %q", seq, before.Name(seq), after.Name(seq))
		}
	}
	// A rounds 0-3 (sequences 1, 4, 7, 10) come before the new version. Their
	// names that are still allowed count against the letter's capacity.
	seen := map[string]bool{}
	for k := range 4 {
		if name := after.Name(1 + 3*k); !strings.Contains(name, "Array") && name != "Atomic Axis" {
			seen[name] = true
		}
	}
	capacity := after.letterCapacity(0, 13)
	seq := 13
	for ; len(seen) < capacity; seq += 3 {
		name := after.Name(seq)
		if strings.Contains(name, "Array") || name == "Atomic Axis" {
			t.Fatalf("sequence %d: %q uses a word or pair retired at 11", seq, name)
		}
		if seen[name] {
			t.Fatalf("sequence %d: %q repeats before the letter's capacity is used", seq, name)
		}
		seen[name] = true
	}
	if !seen[after.Name(seq)] {
		t.Fatalf("sequence %d: %q is new after the capacity was used", seq, after.Name(seq))
	}
	if after.letterCapacity(0, 11) != 4*3-1 {
		t.Fatalf("capacity at 11 = %d, want 4 adjectives × 3 active nouns − 1 denied", after.letterCapacity(0, 11))
	}
}

// A new version must start above every release without a recorded name, and
// may never change a recorded one; version 1 is exempt.
func TestGuard(t *testing.T) {
	base := minimal("A adj Amber Astral Atomic\nA noun Aurora Array Axis\n")
	v2 := func(from int) *Lists {
		return mustParse(base + fmt.Sprintf("version 2 from %d\nA adj Azure\nA noun Apogee\nretire Aurora\n", from))
	}
	v1 := mustParse(base)
	published := []Named{{Sequence: 1}, {Sequence: 4}, {Sequence: 11}}
	if err := v1.Guard(published); err != nil {
		t.Fatalf("version 1: %v", err)
	}
	for _, from := range []int{2, 10, 11} {
		if err := v2(from).Guard(published); err == nil {
			t.Errorf("version 2 from %d passed with release 11 published", from)
		}
	}
	if err := v2(12).Guard(published); err != nil {
		t.Fatalf("version 2 from 12: %v", err)
	}
	// Releases reserved under version 2 record their names and keep passing.
	l := v2(12)
	later := append(published, Named{Sequence: 13, Name: l.Name(13)}, Named{Sequence: 40, Name: l.Name(40)})
	if err := l.Guard(later); err != nil {
		t.Fatalf("recorded version 2 names: %v", err)
	}
	// A recorded name the lists no longer give fails, even below the new version.
	if err := l.Guard([]Named{{Sequence: 4, Name: v1.Name(4) + "x"}}); err == nil {
		t.Fatal("a changed recorded name passed")
	}
	if err := l.Guard([]Named{{Sequence: 13}}); err == nil {
		t.Fatal("an unrecorded release at the new version's first sequence passed")
	}
	// Abandoned attempts neither own names nor constrain a later word-list
	// version. The same sequence can then be stamped by the next attempt.
	if err := l.Guard([]Named{{Sequence: 13, Reusable: true}, {Sequence: 40, Name: "Old Attempt", Reusable: true}}); err != nil {
		t.Fatal("abandoned attempts constrain names", err)
	}
}

// A later version never hides an earlier one that started too low: with
// version 2 from 100 denying "Fresh Flyby" and version 3 from 200, published
// release 111 (unrecorded, named "Fresh Flyby" under version 1) is renamed,
// although it sits below the newest version's first sequence.
func TestGuardEveryVersion(t *testing.T) {
	v1 := mustParse(wordsFile)
	if v1.Name(111) != "Fresh Flyby" {
		t.Fatalf("Name(111) = %q, want Fresh Flyby under version 1", v1.Name(111))
	}
	v2 := wordsFile + "\nversion 2 from 100\ndeny Fresh Flyby\n"
	l := mustParse(v2 + "version 3 from 200\n")
	if l.Name(111) == "Fresh Flyby" {
		t.Fatal("the overlay does not rename release 111")
	}
	if err := l.Guard([]Named{{Sequence: 111}}); err == nil {
		t.Fatalf("release 111 renamed to %q passed", l.Name(111))
	}
	if err := mustParse(wordsFile + "\nversion 2 from 112\ndeny Fresh Flyby\nversion 3 from 200\n").Guard([]Named{{Sequence: 111}}); err != nil {
		t.Fatalf("version 2 from 112: %v", err)
	}
}

// minimal is a word list with the cycle A B C, a's lines for A and one
// placeholder name for B and C.
func minimal(a string) string {
	return "version 1 from 1\ncycle A B C\n" + a + "B adj Bx\nB noun By\nC adj Cx\nC noun Cy\n"
}

func TestParseRejects(t *testing.T) {
	for name, src := range map[string]string{
		"no version":         "A adj Amber\n",
		"version 2 first":    "version 2 from 5\n",
		"from must grow":     minimal("A adj Amber\nA noun Aurora\n") + "version 2 from 1\n",
		"skipped version":    minimal("A adj Amber\nA noun Aurora\n") + "version 3 from 50\n",
		"wrong letter":       minimal("A adj Bold\nA noun Aurora\n"),
		"two words in one":   minimal("A adj AmberAurora\nA noun Aurora\n"),
		"duplicate":          minimal("A adj Amber\nA noun Amber\n"),
		"deny unknown":       minimal("A adj Amber\nA noun Aurora\ndeny Amber Axis\n"),
		"retire unknown":     minimal("A adj Amber\nA noun Aurora\nretire Axis\n"),
		"retire twice":       minimal("A adj Amber Astral\nA noun Aurora\n") + "version 2 from 9\nretire Amber\nretire Amber\n",
		"empty letter":       strings.Replace(minimal("A adj Amber\nA noun Aurora\n"), "C noun Cy\n", "", 1),
		"no cycle":           strings.Replace(minimal("A adj Amber\nA noun Aurora\n"), "cycle A B C\n", "", 1),
		"letter not cycled":  minimal("A adj Amber\nA noun Aurora\nD adj Dx\n"),
		"cycle twice":        minimal("A adj Amber\nA noun Aurora\ncycle A B\n"),
		"cycle repeats":      strings.Replace(minimal("A adj Amber\nA noun Aurora\n"), "cycle A B C", "cycle A B C A", 1),
		"cycle in version 2": minimal("A adj Amber\nA noun Aurora\n") + "version 2 from 9\ncycle A B C D\n",
		"unknown line":       minimal("A adj Amber\nA noun Aurora\nrename Amber Azure\n"),
		"deny across letter": minimal("A adj Amber\nA noun Aurora\ndeny Amber By\n"),
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestStampVersionFile(t *testing.T) {
	in := `{
  "product": "PAIMOS AEON",
  "version_scheme": "inspr-calver-3",
  "version": "260930074921.0.0",
  "release_channel": "stable",
  "release_sequence": 111,
  "reserved_at": "2026-09-30T07:49:21Z",
  "ticket": "AEON-390",
  "unpublished_reservations": [
    "260923134337.0.0",
    "260925231350.0.0"
  ]
}
`
	out, name, err := StampVersionFile([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(in, `"release_sequence": 111,`, `"release_sequence": 111,`+"\n"+`  "codename": "`+Codename(111)+`",`, 1)
	if name != Codename(111) || string(out) != want {
		t.Fatalf("stamped:\n%s\nwant:\n%s", out, want)
	}
	again, _, err := StampVersionFile(out)
	if err != nil || string(again) != string(out) {
		t.Fatalf("restamping is not idempotent: %v", err)
	}
	wrong := strings.Replace(string(out), Codename(111), Codename(112), 1)
	if _, _, err := StampVersionFile([]byte(wrong)); err == nil {
		t.Fatal("a codename that differs from the sequence's name must be refused")
	}
	if _, _, err := StampVersionFile([]byte(`{"version":"260930074921.0.0"}`)); err == nil {
		t.Fatal("a file without release_sequence must be refused")
	}
	last, _, err := StampVersionFile([]byte(`{"version": "x", "release_sequence": 1}`))
	if err != nil || string(last) != "{\n  \"version\": \"x\",\n  \"release_sequence\": 1,\n  \"codename\": \"Avid Axle\"\n}\n" {
		t.Fatalf("stamp after the last member: %q, %v", last, err)
	}
}

// The reservation's version.json, once stamped, names the same codename as
// its sequence; a hand edit cannot drift from the function.
func TestRepositoryVersionFile(t *testing.T) {
	raw, err := os.ReadFile("../../../version.json")
	if err != nil {
		t.Skip("no version.json in this checkout")
	}
	if _, _, err := StampVersionFile(raw); err != nil {
		t.Fatal(err)
	}
}

func TestStampFile(t *testing.T) {
	path := t.TempDir() + "/version.json"
	if err := os.WriteFile(path, []byte("{\"release_sequence\": 2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, changed, err := StampFile(path)
	raw, _ := os.ReadFile(path)
	if err != nil || !changed || name != "Blue Bot" || string(raw) != "{\n  \"release_sequence\": 2,\n  \"codename\": \"Blue Bot\"\n}\n" {
		t.Fatalf("stamp: %q %v %v %q", name, changed, err, raw)
	}
	if _, changed, err := StampFile(path); changed || err != nil {
		t.Fatalf("restamp changed %v, %v", changed, err)
	}
}
