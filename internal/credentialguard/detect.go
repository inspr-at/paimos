// SPDX-License-Identifier: AGPL-3.0-only

// Package credentialguard detects credential-shaped text without returning values.
package credentialguard

import (
	"encoding/base64"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Detection is best effort. Callers decide whether a match requires refusal
// or explicit confirmation; the detector exposes only positions, never values.
//
// A credential word alone never triggers ("The token: refresh it" is prose).
// A label needs a value with a credential's shape after it: quoted, or one
// token with enough length and character mix, or a known key prefix. File
// names and paths are never credentials.
//
// Provider key formats come from the gitleaks default rule set, vendored in
// gitleaks/ and turned into gitleaks_rules.go; they apply in addition to the
// label and value heuristics here.

//go:generate go run ./gitleaks/generate.go -tag v8.30.1

// Range is one suspected credential in a text field, in Unicode
// code points. Only positions are returned, never the text.
type Range struct {
	Field string `json:"field"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// span is a byte range [start, end) in the checked text.
type span struct{ start, end int }

type spanPattern struct {
	re    *regexp.Regexp
	group int
	needs []string // runs only when the lower-case text holds one of these
}

// Shapes that are credentials wherever they appear.
var fixedPatterns = []spanPattern{
	// Provider keys and tokens by prefix.
	{regexp.MustCompile(`(?:^|[^A-Za-z0-9])((?:sk-(?:proj-|ant-|live-|test-)?[A-Za-z0-9_-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{16,}|rk_(?:live|test)_[A-Za-z0-9]{16,}|xai-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{16,}|xox[abeoprs]-[A-Za-z0-9-]{10,}|ya29\.[A-Za-z0-9_-]{16,}|AIza[0-9A-Za-z_-]{30,}|aeon_[A-Za-z0-9_]{16,}|npm_[A-Za-z0-9]{30,}|hf_[A-Za-z0-9]{30,}))`), 1,
		[]string{"sk-", "sk_", "rk_", "xai-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-", "xox", "ya29.", "aiza", "aeon_", "npm_", "hf_"}},
	// AWS access key ids.
	{regexp.MustCompile(`(?:^|[^A-Za-z0-9])((?:AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16})(?:[^A-Za-z0-9]|$)`), 1, []string{"akia", "asia", "abia", "acca"}},
	// JSON web tokens.
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), 0, []string{"eyj"}},
	// PEM blocks: private keys, certificates, anything armoured.
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----(?s:.*?)(?:-----END [A-Z0-9 ]+-----|$)`), 0, []string{"-----begin "}},
	// Credentials in a URL.
	{regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://([^\s/:@]+:[^\s/@]+)@`), 1, []string{"://"}},
	// Authorization values.
	{bearerValue, 1, []string{"bearer"}},
	{basicValue, 1, []string{"basic"}},
}

var (
	bearerValue = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])bearer\s+([A-Za-z0-9._~+/-]{16,}=*)`)
	basicValue  = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])basic\s+([A-Za-z0-9+/]{4,}={0,2})(?:[^A-Za-z0-9+/=]|$)`)
	// A credential label, optionally in Markdown or quotes, and its
	// separator: password=, **Password:**, `password`:, "api_key": .
	credentialLabel = regexp.MustCompile("(?i)(?:^|[^A-Za-z0-9])[*_`\"']{0,3}(passw(?:or)?d|pwd|passphrase|client[_-]?secret|secret[_-]?key|secret|access[_-]?token|refresh[_-]?token|auth[_-]?token|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key)[*_`\"']{0,3}[ \\t]*([:=])[*_`]{0,3}[ \\t]*")

	ticketKey  = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)
	versionish = regexp.MustCompile(`^[vV]?[0-9]+([.:/-][0-9]+)+$`)
	envName    = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)*$`)
	snakeWords = regexp.MustCompile(`^[a-z]+([_-][a-z]+)+$`)
	fileSuffix = regexp.MustCompile(`^[A-Za-z0-9_./-]+\.[a-z][a-z0-9]{0,4}$`)
)

// Contains reports whether text appears to contain a credential.
func Contains(text string) bool {
	return len(sensitiveSpans(text)) > 0
}

// Ranges lists the suspected credentials in one field.
func Ranges(field, text string) []Range {
	spans := sensitiveSpans(text)
	out := make([]Range, 0, len(spans))
	for _, s := range spans {
		start := utf8.RuneCountInString(text[:s.start])
		out = append(out, Range{Field: field, Start: start, End: start + utf8.RuneCountInString(text[s.start:s.end])})
	}
	return out
}

// sensitiveSpans returns the merged byte ranges that look like credentials.
func sensitiveSpans(text string) []span {
	if text == "" {
		return nil
	}
	var spans []span
	add := func(start, end int) {
		if start >= 0 && end > start {
			spans = append(spans, span{start, end})
		}
	}
	lower := strings.ToLower(text)
	for _, p := range fixedPatterns {
		if !slices.ContainsFunc(p.needs, func(n string) bool { return strings.Contains(lower, n) }) {
			continue
		}
		for _, m := range p.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := m[2*p.group], m[2*p.group+1]
			switch value := text[start:end]; p.re {
			case bearerValue:
				if charClasses(value) < 2 || wordLike(value) || identifierLike(value) {
					continue
				}
			case basicValue:
				if !basicCredential(value) {
					continue
				}
			}
			add(start, end)
		}
	}
	for _, m := range credentialLabel.FindAllStringSubmatchIndex(text, -1) {
		label := strings.ToLower(text[m[2]:m[3]])
		if start, end, ok := labelledValue(text, m[1], label, text[m[4]]); ok {
			add(start, end)
		}
	}
	for _, m := range opaqueRuns(text) {
		if run := text[m.start:m.end]; highEntropy(run) && !identifierLike(run) {
			add(m.start, m.end)
		}
	}
	leakSpans(text, lower, func(_ string, start, end int) { add(start, end) })
	return mergeSpans(spans)
}

// opaqueRuns lists the runs of 32 or more token characters, the shape of a
// random key.
func opaqueRuns(text string) []span {
	var runs []span
	start := -1
	for i := 0; i <= len(text); i++ {
		if i < len(text) && tokenChar(text[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start >= 32 {
			runs = append(runs, span{start, i})
		}
		start = -1
	}
	return runs
}

func tokenChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("+/_=-", c) >= 0
}

// leakRuleSpec is one gitleaks rule as generated into gitleaks_rules.go.
type leakRuleSpec struct {
	id          string
	regex       string
	keywords    []string // lower case; the rule runs only when one occurs
	entropy     float64  // the secret must exceed this Shannon entropy
	secretGroup int      // 0: the first non-empty group, else the match
	allow       []leakAllowSpec
}

// leakAllowSpec exempts a finding whose target matches one of regexes or
// whose secret contains one of the stop words.
type leakAllowSpec struct {
	target    string // "" for the secret, "match" or "line"
	regexes   []string
	stopwords []string
}

type leakAllow struct {
	target    string
	re        *regexp.Regexp
	stopwords []string
}

type leakRule struct {
	spec     leakRuleSpec
	re       *regexp.Regexp
	allow    []leakAllow
	keywords []int // into leakSet.keywords
}

type leakSet struct {
	rules    []leakRule
	global   leakAllow
	keywords []string
	// byPair indexes keywords by their first two bytes, for one pass over
	// the text.
	byPair map[[2]byte][]int
}

// leakRules compiles the generated rules once, on first use. The generator
// kept only patterns that compile, and a test compiles them all.
var leakRules = sync.OnceValue(func() *leakSet {
	set := &leakSet{rules: make([]leakRule, len(gitleaksRules)), global: compileAllow(gitleaksGlobalAllow), byPair: map[[2]byte][]int{}}
	index := map[string]int{}
	for i, spec := range gitleaksRules {
		r := leakRule{spec: spec, re: regexp.MustCompile(spec.regex)}
		for _, a := range spec.allow {
			r.allow = append(r.allow, compileAllow(a))
		}
		for _, k := range spec.keywords {
			id, ok := index[k]
			if !ok {
				id = len(set.keywords)
				index[k] = id
				set.keywords = append(set.keywords, k)
				pair := [2]byte{k[0]}
				if len(k) > 1 {
					pair[1] = k[1]
				}
				set.byPair[pair] = append(set.byPair[pair], id)
			}
			r.keywords = append(r.keywords, id)
		}
		set.rules[i] = r
	}
	return set
})

// present reports which keywords occur in the lower-case text.
func (set *leakSet) present(lower string) []bool {
	found := make([]bool, len(set.keywords))
	for i := 0; i < len(lower); i++ {
		pair := [2]byte{lower[i]}
		if i+1 < len(lower) {
			pair[1] = lower[i+1]
		}
		for _, id := range set.byPair[pair] {
			if !found[id] && strings.HasPrefix(lower[i:], set.keywords[id]) {
				found[id] = true
			}
		}
	}
	return found
}

func compileAllow(a leakAllowSpec) leakAllow {
	out := leakAllow{target: a.target, stopwords: a.stopwords}
	if len(a.regexes) > 0 {
		out.re = regexp.MustCompile("(?:" + strings.Join(a.regexes, ")|(?:") + ")")
	}
	return out
}

// leakSpans applies the gitleaks rules as gitleaks does: a keyword
// prefilter, the pattern, the secret group, the entropy floor, then the
// global and rule allowlists. Paths stay exempt, as everywhere here.
func leakSpans(text, lower string, add func(rule string, start, end int)) {
	set := leakRules()
	present := set.present(lower)
	for _, r := range set.rules {
		if len(r.keywords) > 0 && !slices.ContainsFunc(r.keywords, func(id int) bool { return present[id] }) {
			continue
		}
		for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := leakSecret(text, m, r.spec.secretGroup)
			if start < 0 || end <= start {
				continue
			}
			secret := text[start:end]
			if r.spec.entropy != 0 && shannonEntropy(secret) <= r.spec.entropy || pathLike(secret) {
				continue
			}
			allowed := set.global.allows(text, m, secret)
			for _, a := range r.allow {
				allowed = allowed || a.allows(text, m, secret)
			}
			if !allowed {
				add(r.spec.id, start, end)
			}
		}
	}
}

// leakSecret picks the secret of a match: the rule's secret group, else the
// first non-empty group, else the match without surrounding line breaks.
func leakSecret(text string, m []int, group int) (int, int) {
	if group > 0 {
		return m[2*group], m[2*group+1]
	}
	for g := 1; 2*g+1 < len(m); g++ {
		if m[2*g] >= 0 && m[2*g+1] > m[2*g] {
			return m[2*g], m[2*g+1]
		}
	}
	start, end := m[0], m[1]
	for start < end && text[start] == '\n' {
		start++
	}
	for end > start && text[end-1] == '\n' {
		end--
	}
	return start, end
}

func (a leakAllow) allows(text string, m []int, secret string) bool {
	if a.re != nil {
		target := secret
		switch a.target {
		case "match":
			target = text[m[0]:m[1]]
		case "line":
			start := strings.LastIndexByte(text[:m[0]], '\n') + 1
			end := strings.IndexByte(text[m[1]:], '\n')
			if end < 0 {
				end = len(text)
			} else {
				end += m[1]
			}
			target = text[start:end]
		}
		if a.re.MatchString(target) {
			return true
		}
	}
	if len(a.stopwords) > 0 {
		lower := strings.ToLower(secret)
		for _, w := range a.stopwords {
			if strings.Contains(lower, w) {
				return true
			}
		}
	}
	return false
}

// shannonEntropy is the entropy of value per byte, counted over code points
// as gitleaks computes it.
func shannonEntropy(value string) float64 {
	counts := map[rune]int{}
	for _, r := range value {
		counts[r]++
	}
	n := float64(len(value))
	entropy := 0.0
	for _, c := range counts {
		p := float64(c) / n
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func mergeSpans(spans []span) []span {
	if len(spans) < 2 {
		return spans
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	out := spans[:1]
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s.start <= last.end {
			last.end = max(last.end, s.end)
			continue
		}
		out = append(out, s)
	}
	return out
}

// basicCredential: a Basic authorization value decodes to user:password.
func basicCredential(value string) bool {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(value, "="))
	}
	if err != nil || !strings.Contains(string(raw), ":") || !utf8.Valid(raw) {
		return false
	}
	for _, r := range string(raw) {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// labelledValue reads the value after a credential label ending at i and
// reports its byte range when it has a credential's shape.
func labelledValue(text string, i int, label string, sep byte) (int, int, bool) {
	password := strings.HasPrefix(label, "pass") || label == "pwd"
	minLen := 8
	if password {
		minLen = 5
	}
	if i >= len(text) {
		return 0, 0, false
	}
	if q := text[i]; q == '"' || q == '\'' || q == '`' {
		if end := strings.IndexByte(text[i+1:], q); end >= 0 && !strings.ContainsAny(text[i+1:i+1+end], "\r\n") {
			value := text[i+1 : i+1+end]
			n := utf8.RuneCountInString(strings.TrimSpace(value))
			switch {
			case placeholder(value) || n < minLen || pathLike(strings.TrimSpace(value)):
				return 0, 0, false
			case password:
				return i + 1, i + 1 + end, true
			case strings.ContainsAny(value, " \t") || wordLike(value):
				return 0, 0, false
			}
			return i + 1, i + 1 + end, true
		}
		i++
	}
	end := i
	for end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if unicode.IsSpace(r) || strings.ContainsRune(",;\"'`", r) {
			break
		}
		end += size
	}
	end = i + len(strings.TrimRight(text[i:end], ".,:;)]}*_"))
	value := text[i:end]
	if value == "" || placeholder(value) || pathLike(value) || ticketKey.MatchString(value) || versionish.MatchString(value) {
		return 0, 0, false
	}
	n := utf8.RuneCountInString(value)
	switch {
	case n < minLen:
		return 0, 0, false
	case sep == '=' && (password || n >= 16):
		return i, end, true
	case sep == ':' && (envName.MatchString(value) || snakeWords.MatchString(value)):
		return 0, 0, false
	case charClasses(value) >= 2 && !wordLike(value):
		return i, end, true
	}
	return 0, 0, false
}

// placeholder: a reference or a mask, not a value: ${API_KEY}, $TOKEN,
// <your token>, {{ secret }}, %s, ****.
func placeholder(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return true
	}
	if strings.HasPrefix(v, "$") || strings.HasPrefix(v, "%") || strings.Contains(v, "${") || strings.Contains(v, "{{") {
		return true
	}
	if strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") {
		return true
	}
	return strings.Trim(v, "*•.") == ""
}

// pathLike: a file name or path, e.g. Foo.vue or internal/x.go. Random keys
// carry no dot, so a name with a file extension is a file.
func pathLike(value string) bool {
	v := strings.TrimLeft(strings.TrimPrefix(value, "~"), "./")
	if fileSuffix.MatchString(v) {
		return true
	}
	return strings.Contains(v, "/") && identifierLike(v)
}

// wordLike: letters only, as a word is written: lower, Title or UPPER case,
// hyphenated parts allowed (short-lived).
func wordLike(value string) bool {
	for _, part := range strings.Split(value, "-") {
		if part == "" {
			return false
		}
		first, size := utf8.DecodeRuneInString(part)
		lower, upper := strings.ToLower(part), strings.ToUpper(part)
		title := string(unicode.ToUpper(first)) + strings.ToLower(part[size:])
		for _, r := range part {
			if !unicode.IsLetter(r) {
				return false
			}
		}
		if part != lower && part != upper && part != title {
			return false
		}
	}
	return true
}

// charClasses counts lower case, upper case, digits and other characters.
func charClasses(value string) int {
	var lower, upper, digit, other int
	for _, r := range value {
		switch {
		case unicode.IsLower(r):
			lower = 1
		case unicode.IsUpper(r):
			upper = 1
		case unicode.IsDigit(r):
			digit = 1
		default:
			other = 1
		}
	}
	return lower + upper + digit + other
}

// identifierLike reports whether run reads as a path or a code identifier.
// Split into word pieces (camelCase, snake_case, kebab-case, path segments),
// at least three quarters of its letters sit in word-shaped pieces, at most a
// third are capitals, and it holds at most two short numbers. A word-shaped
// piece has three or more letters and reads like a word: an acronym of at
// most five capitals, or letters with a vowel in every five. A random key
// switches case every few characters and breaks into short or
// unpronounceable pieces.
func identifierLike(run string) bool {
	letters, worded, upper, numbers := 0, 0, 0, 0
	for _, part := range strings.FieldsFunc(run, func(r rune) bool { return strings.ContainsRune("/_.-+=~", r) }) {
		pieces, ok := wordPieces(part)
		if !ok {
			return false
		}
		for _, piece := range pieces {
			if piece[0] >= '0' && piece[0] <= '9' {
				if numbers++; len(piece) > 4 || numbers > 2 {
					return false
				}
				continue
			}
			letters += len(piece)
			upper += len(piece) - len(strings.TrimLeft(piece, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
			if wordShaped(piece) {
				worded += len(piece)
			}
		}
	}
	return letters > 0 && worded*4 >= letters*3 && upper*3 <= letters
}

func wordShaped(piece string) bool {
	if len(piece) < 3 {
		return false
	}
	if strings.ToUpper(piece) == piece {
		return len(piece) <= 5
	}
	gap := 0
	for _, c := range strings.ToLower(piece) {
		if strings.ContainsRune("aeiouy", c) {
			gap = 0
			continue
		}
		if gap++; gap >= 5 {
			return false
		}
	}
	vowels := len(piece) - len(strings.Map(func(r rune) rune {
		if strings.ContainsRune("aeiouyAEIOUY", r) {
			return -1
		}
		return r
	}, piece))
	return len(piece) <= 4 || vowels*5 >= len(piece)
}

// wordPieces splits an ASCII identifier part at case and digit changes:
// HTTPServerV2 is HTTP, Server, V, 2.
func wordPieces(part string) ([]string, bool) {
	isLower := func(c byte) bool { return c >= 'a' && c <= 'z' }
	isUpper := func(c byte) bool { return c >= 'A' && c <= 'Z' }
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	var pieces []string
	for i := 0; i < len(part); {
		j := i + 1
		switch c := part[i]; {
		case isDigit(c):
			for j < len(part) && isDigit(part[j]) {
				j++
			}
		case isUpper(c):
			for j < len(part) && isUpper(part[j]) {
				j++
			}
			if j < len(part) && isLower(part[j]) {
				if j-i > 1 {
					j-- // HTTPServer: HTTP, then Server
				} else {
					for j < len(part) && isLower(part[j]) {
						j++
					}
				}
			}
		case isLower(c):
			for j < len(part) && isLower(part[j]) {
				j++
			}
		default:
			return nil, false
		}
		pieces = append(pieces, part[i:j])
		i = j
	}
	return pieces, true
}

// highEntropy flags a mixed-case run with random-looking characters. Hex
// digests and UUIDs have no capitals, so commit ids are not flagged; code
// identifiers are exempted by identifierLike.
func highEntropy(run string) bool {
	trimmed := strings.Trim(run, "=/-_+")
	if len(trimmed) < 24 {
		return false
	}
	var upper, lower bool
	counts := map[rune]int{}
	for _, r := range trimmed {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		}
		counts[r]++
	}
	if !upper || !lower {
		return false
	}
	n := float64(len(trimmed))
	entropy := 0.0
	for _, c := range counts {
		p := float64(c) / n
		entropy -= p * math.Log2(p)
	}
	return entropy >= 4.0
}
