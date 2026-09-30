// SPDX-License-Identifier: AGPL-3.0-only

package codename

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
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
const version1SHA256 = "104bcf0a5581c7b7938b724695e596bb8797418aa541c7fa00e347ed0426ce15"

func TestLetterCycling(t *testing.T) {
	names := defaultLists.Names(26 * 30)
	for i, name := range names {
		seq := i + 1
		letter := byte('A' + (seq-1)%26)
		words := strings.Fields(name)
		if len(words) != 2 || words[0][0] != letter || words[1][0] != letter {
			t.Fatalf("sequence %d: %q, want two words starting with %c", seq, name, letter)
		}
	}
	if Codename(0) != "" || Codename(-3) != "" {
		t.Fatal("sequences below 1 have no codename")
	}
	if Codename(1)[0] != 'A' || Codename(26)[0] != 'Z' || Codename(27)[0] != 'A' {
		t.Fatal("release 1 is A, 26 is Z, 27 starts the next A round")
	}
}

// Within a letter nothing repeats until its capacity is used up; then the
// next cycle starts with the letter's first name again. Denied combinations
// never come up.
func TestNoRepeatsWithinCapacity(t *testing.T) {
	total := 0
	for li := range 26 {
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
	if Capacity() < 20000 {
		t.Fatalf("capacity %d: the lists should hold well over 20,000 names", Capacity())
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
)

func TestWordQuality(t *testing.T) {
	for li, ll := range defaultLists.letters {
		for _, w := range append(append([]word{}, ll.adj...), ll.noun...) {
			lower := strings.ToLower(w.text)
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
		}
	}
}

// Every combination is scanned: a strong rude fragment where the words run
// together, a known unlucky phrase, or a repeated stem ("Planetary Planet":
// six shared letters, or one word starting with the other) must be on the
// deny list, so the name can never be picked.
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
				if sharedPrefix(la, ln) >= 6 || strings.HasPrefix(ln, la) || strings.HasPrefix(la, ln) {
					why = "repeated stem"
				}
				if why != "" && !denied(li, a.text, n.text) {
					t.Errorf("%s %s: %s; add \"deny %s %s\" in a new list version", a.text, n.text, why, a.text, n.text)
				}
			}
		}
	}
	if scanned < 25000 {
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
// deterministically, before a letter repeats.
func TestUnevenLists(t *testing.T) {
	l := mustParse(minimal("A adj Amber Astral Atomic\nA noun Aurora Array\n"))
	got := l.letterNames(0, 7)
	want := []string{"Amber Aurora", "Astral Array", "Atomic Aurora", "Amber Array", "Astral Aurora", "Atomic Array", "Amber Aurora"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("uneven lists: %v, want %v", got, want)
	}
	l = mustParse(minimal("A adj Amber\nA noun Aurora Array Axis Apogee\n"))
	if got := l.letterNames(0, 5); strings.Join(got, ",") != "Amber Aurora,Amber Array,Amber Axis,Amber Apogee,Amber Aurora" {
		t.Fatalf("one adjective, four nouns: %v", got)
	}
	if got := l.Name(1 + 26*2); got != "Amber Axis" {
		t.Fatalf("Name(53) = %q, want the third A name", got)
	}
}

// Appending a version with new words, a retirement and a denial leaves every
// earlier sequence's name as it was, and changes only later ones.
func TestAppendingKeepsOldNames(t *testing.T) {
	base := minimal("A adj Amber Astral Atomic\nA noun Aurora Array Axis\n")
	before := mustParse(base)
	after := mustParse(base + "version 2 from 80\nA adj Azure\nA noun Apogee\nretire Array\ndeny Atomic Axis\n")
	for seq := 1; seq < 80; seq++ {
		if before.Name(seq) != after.Name(seq) {
			t.Fatalf("sequence %d changed from %q to %q", seq, before.Name(seq), after.Name(seq))
		}
	}
	// A rounds 0-3 (sequences 1, 27, 53, 79) come before the new version. Their
	// names that are still allowed count against the letter's capacity.
	seen := map[string]bool{}
	for k := range 4 {
		if name := after.Name(1 + 26*k); !strings.Contains(name, "Array") && name != "Atomic Axis" {
			seen[name] = true
		}
	}
	capacity := after.letterCapacity(0, 105)
	seq := 105
	for ; len(seen) < capacity; seq += 26 {
		name := after.Name(seq)
		if strings.Contains(name, "Array") || name == "Atomic Axis" {
			t.Fatalf("sequence %d: %q uses a word or pair retired at 80", seq, name)
		}
		if seen[name] {
			t.Fatalf("sequence %d: %q repeats before the letter's capacity is used", seq, name)
		}
		seen[name] = true
	}
	if !seen[after.Name(seq)] {
		t.Fatalf("sequence %d: %q is new after the capacity was used", seq, after.Name(seq))
	}
	if after.letterCapacity(0, 80) != 4*3-1 {
		t.Fatalf("capacity at 80 = %d, want 4 adjectives × 3 active nouns − 1 denied", after.letterCapacity(0, 80))
	}
}

func minimal(a string) string {
	var b strings.Builder
	b.WriteString("version 1 from 1\n" + a)
	for c := 'B'; c <= 'Z'; c++ {
		fmt.Fprintf(&b, "%c adj %cx\n%c noun %cy\n", c, c, c, c)
	}
	return b.String()
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
		"empty letter":       strings.Replace(minimal("A adj Amber\nA noun Aurora\n"), "Z noun Zy\n", "", 1),
		"unknown line":       minimal("A adj Amber\nA noun Aurora\nrename Amber Azure\n"),
		"deny across letter": minimal("A adj Amber\nA noun Aurora\ndeny Amber Bx\n"),
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
	wrong := strings.Replace(string(out), Codename(111), "Amber Aurora", 1)
	if _, _, err := StampVersionFile([]byte(wrong)); err == nil {
		t.Fatal("a codename that differs from the sequence's name must be refused")
	}
	if _, _, err := StampVersionFile([]byte(`{"version":"260930074921.0.0"}`)); err == nil {
		t.Fatal("a file without release_sequence must be refused")
	}
	last, _, err := StampVersionFile([]byte(`{"version": "x", "release_sequence": 1}`))
	if err != nil || string(last) != "{\n  \"version\": \"x\",\n  \"release_sequence\": 1,\n  \"codename\": \"Amber Aurora\"\n}\n" {
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
	if err != nil || !changed || name != "Brisk Binary" || string(raw) != "{\n  \"release_sequence\": 2,\n  \"codename\": \"Brisk Binary\"\n}\n" {
		t.Fatalf("stamp: %q %v %v %q", name, changed, err, raw)
	}
	if _, changed, err := StampFile(path); changed || err != nil {
		t.Fatalf("restamp changed %v, %v", changed, err)
	}
}
