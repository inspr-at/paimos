// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	// Six contiguous words is the run that blocks a quotation. Whole entries
	// shorter than that still block once they reach quoteWholeMin, so a
	// two-word TL;DR cannot refuse every public proposal that mentions it.
	quoteRunWords       = 6
	quoteWholeMin       = 5
	quoteShingleWords   = 8
	quoteShingleKeep    = 5
	quoteShinglePercent = 60
	quoteEntryMax       = 2000
	maxGuardFile        = 1 << 20
	maxGuardTotal       = 8 << 20
	maxCorpusBytes      = 8 << 20
)

// guardCorpus is the private quotation index. It stores hashes of normalised
// runs, whole entries and shingles, never the private words themselves.
type guardCorpus struct {
	runs    map[[16]byte]struct{}
	wholes  map[uint16]map[[16]byte]struct{}
	entries []map[[16]byte]struct{}
	seen    map[[16]byte]struct{}
}

func newGuardCorpus() *guardCorpus {
	return &guardCorpus{
		runs:   map[[16]byte]struct{}{},
		wholes: map[uint16]map[[16]byte]struct{}{},
		seen:   map[[16]byte]struct{}{},
	}
}

func (c *guardCorpus) empty() bool {
	return c == nil || len(c.runs) == 0 && len(c.wholes) == 0 && len(c.entries) == 0
}

func hashWords(words []string) [16]byte {
	h := sha256.New()
	for i, w := range words {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(w))
	}
	var out [16]byte
	copy(out[:], h.Sum(nil))
	return out
}

func (c *guardCorpus) add(path, text string) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	c.addRuns(proposalWords(text))
	for _, line := range strings.Split(text, "\n") {
		c.addEntry(line)
	}
	for _, para := range strings.Split(text, "\n\n") {
		c.addEntry(para)
	}
	if strings.HasSuffix(strings.ToLower(path), ".yaml") || strings.HasSuffix(strings.ToLower(path), ".yml") {
		c.addYAML(text)
	}
}

func (c *guardCorpus) addRuns(words []string) {
	for i := 0; i+quoteRunWords <= len(words); i++ {
		c.runs[hashWords(words[i:i+quoteRunWords])] = struct{}{}
	}
}

func (c *guardCorpus) addEntry(text string) {
	words := proposalWords(text)
	if len(words) < quoteWholeMin || len(words) > quoteEntryMax {
		return
	}
	c.addRuns(words)
	h := hashWords(words)
	if _, ok := c.seen[h]; ok {
		return
	}
	c.seen[h] = struct{}{}
	n := uint16(len(words))
	if c.wholes[n] == nil {
		c.wholes[n] = map[[16]byte]struct{}{}
	}
	c.wholes[n][h] = struct{}{}
	if len(words) < quoteShingleWords {
		return
	}
	set := make(map[[16]byte]struct{}, len(words))
	for i := 0; i+4 <= len(words); i++ {
		set[hashWords(words[i:i+4])] = struct{}{}
	}
	c.entries = append(c.entries, set)
}

func (c *guardCorpus) addYAML(text string) {
	var node yaml.Node
	if yaml.Unmarshal([]byte(text), &node) != nil {
		return
	}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || n.Kind == yaml.AliasNode {
			return
		}
		if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
			c.addEntry(n.Value)
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(&node)
}

func corpusFrom(docs map[string]string) *guardCorpus {
	c := newGuardCorpus()
	paths := make([]string, 0, len(docs))
	for path := range docs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		c.add(path, docs[path])
	}
	return c
}

// publicAllow is the indexed public text. A private span that already occurs
// here is not a quotation of something unpublished.
type publicAllow struct {
	runs     map[[16]byte]struct{}
	shingles map[[16]byte]struct{}
	streams  [][]string
	windows  map[uint16]map[[16]byte]struct{}
}

func allowFromFiles(files []File) publicAllow {
	a := publicAllow{
		runs:     map[[16]byte]struct{}{},
		shingles: map[[16]byte]struct{}{},
		windows:  map[uint16]map[[16]byte]struct{}{},
	}
	for _, f := range files {
		if len(f.Content) == 0 || bytes.Contains(f.Content, []byte{0}) || !utf8.Valid(f.Content) {
			continue
		}
		words := proposalWords(string(f.Content))
		if len(words) == 0 {
			continue
		}
		a.streams = append(a.streams, words)
		for i := 0; i+quoteRunWords <= len(words); i++ {
			a.runs[hashWords(words[i:i+quoteRunWords])] = struct{}{}
		}
		for i := 0; i+4 <= len(words); i++ {
			a.shingles[hashWords(words[i:i+4])] = struct{}{}
		}
	}
	return a
}

func (a *publicAllow) hasWindow(n uint16, h [16]byte) bool {
	if a == nil || int(n) < quoteWholeMin {
		return false
	}
	if a.windows[n] == nil {
		set := map[[16]byte]struct{}{}
		width := int(n)
		for _, stream := range a.streams {
			for i := 0; i+width <= len(stream); i++ {
				set[hashWords(stream[i:i+width])] = struct{}{}
			}
		}
		if a.windows == nil {
			a.windows = map[uint16]map[[16]byte]struct{}{}
		}
		a.windows[n] = set
	}
	_, ok := a.windows[n][h]
	return ok
}

func (c *guardCorpus) quotes(allow publicAllow, texts ...string) bool {
	for _, text := range texts {
		words := proposalWords(text)
		if c.runHit(allow, words) || c.wholeHit(allow, words) || c.shingleHit(allow, words) {
			return true
		}
	}
	return false
}

func (c *guardCorpus) runHit(allow publicAllow, words []string) bool {
	for i := 0; i+quoteRunWords <= len(words); i++ {
		h := hashWords(words[i : i+quoteRunWords])
		if _, ok := c.runs[h]; !ok {
			continue
		}
		if _, pub := allow.runs[h]; pub {
			continue
		}
		return true
	}
	return false
}

func (c *guardCorpus) wholeHit(allow publicAllow, words []string) bool {
	for n, set := range c.wholes {
		if int(n) > len(words) {
			continue
		}
		width := int(n)
		for i := 0; i+width <= len(words); i++ {
			h := hashWords(words[i : i+width])
			if _, ok := set[h]; !ok || allow.hasWindow(n, h) {
				continue
			}
			return true
		}
	}
	return false
}

func (c *guardCorpus) shingleHit(allow publicAllow, words []string) bool {
	if len(words) < 4 || len(c.entries) == 0 {
		return false
	}
	proposed := make(map[[16]byte]struct{}, len(words))
	for i := 0; i+4 <= len(words); i++ {
		proposed[hashWords(words[i:i+4])] = struct{}{}
	}
	for _, entry := range c.entries {
		matches, denom := 0, 0
		for h := range entry {
			if _, pub := allow.shingles[h]; pub {
				continue
			}
			denom++
			if _, ok := proposed[h]; ok {
				matches++
			}
		}
		if matches >= quoteShingleKeep && denom > 0 && matches*100 >= denom*quoteShinglePercent {
			return true
		}
	}
	return false
}

// guardPrivateQuotes refuses a public proposal that quotes the private corpus.
// Spans that already occur in the public source's indexed files are ignored.
// A nil corpus means this proposal is not public. An empty one cannot prove
// the proposal safe. The error never contains the private match.
func guardPrivateQuotes(c *guardCorpus, public []File, texts ...string) error {
	if c == nil {
		return nil
	}
	if c.empty() {
		return fail(422, "private_index_unavailable", "Public proposals require an authorized, successfully indexed private doctrine source. Restore its index before proposing.")
	}
	if c.quotes(allowFromFiles(public), texts...) {
		return fail(422, "private_doctrine", "This public proposal quotes private doctrine. Generalise the changed text or propose it in the private repository. Nothing was published.")
	}
	return nil
}

func (c *guardCorpus) marshal() ([]byte, error) {
	runs := hashesOf(c.runs)
	lengths := make([]int, 0, len(c.wholes))
	for n := range c.wholes {
		lengths = append(lengths, int(n))
	}
	slices.Sort(lengths)
	var b []byte
	b = append(b, 'P', 'G', 1)
	b = appendU32(b, len(runs))
	for _, h := range runs {
		b = append(b, h[:]...)
	}
	b = appendU32(b, len(lengths))
	for _, n := range lengths {
		var width [2]byte
		binary.BigEndian.PutUint16(width[:], uint16(n))
		b = append(b, width[:]...)
		hashes := hashesOf(c.wholes[uint16(n)])
		b = appendU32(b, len(hashes))
		for _, h := range hashes {
			b = append(b, h[:]...)
		}
	}
	b = appendU32(b, len(c.entries))
	for _, entry := range c.entries {
		hashes := hashesOf(entry)
		b = appendU32(b, len(hashes))
		for _, h := range hashes {
			b = append(b, h[:]...)
		}
	}
	if len(b) > maxCorpusBytes {
		return nil, gitFail("the private doctrine guard is larger than Aeon stores")
	}
	return b, nil
}

func hashesOf(set map[[16]byte]struct{}) [][16]byte {
	out := make([][16]byte, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b [16]byte) int { return bytes.Compare(a[:], b[:]) })
	return out
}

func appendU32(b []byte, n int) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(n))
	return append(b, buf[:]...)
}

func unmarshalGuard(raw []byte) (*guardCorpus, error) {
	if len(raw) < 3 || raw[0] != 'P' || raw[1] != 'G' || raw[2] != 1 {
		return nil, gitFail("the private doctrine guard could not be read")
	}
	c := &guardCorpus{runs: map[[16]byte]struct{}{}, wholes: map[uint16]map[[16]byte]struct{}{}}
	rest := raw[3:]
	var n int
	var err error
	if n, rest, err = takeU32(rest, 2_000_000); err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		var h [16]byte
		if len(rest) < len(h) {
			return nil, gitFail("the private doctrine guard could not be read")
		}
		copy(h[:], rest[:len(h)])
		rest = rest[len(h):]
		c.runs[h] = struct{}{}
	}
	var lengths int
	if lengths, rest, err = takeU32(rest, 4000); err != nil {
		return nil, err
	}
	for i := 0; i < lengths; i++ {
		if len(rest) < 2 {
			return nil, gitFail("the private doctrine guard could not be read")
		}
		width := binary.BigEndian.Uint16(rest[:2])
		rest = rest[2:]
		var count int
		if count, rest, err = takeU32(rest, 2_000_000); err != nil {
			return nil, err
		}
		set := make(map[[16]byte]struct{}, count)
		for j := 0; j < count; j++ {
			var h [16]byte
			if len(rest) < len(h) {
				return nil, gitFail("the private doctrine guard could not be read")
			}
			copy(h[:], rest[:len(h)])
			rest = rest[len(h):]
			set[h] = struct{}{}
		}
		c.wholes[width] = set
	}
	var entries int
	if entries, rest, err = takeU32(rest, 200_000); err != nil {
		return nil, err
	}
	c.entries = make([]map[[16]byte]struct{}, 0, entries)
	for i := 0; i < entries; i++ {
		var count int
		if count, rest, err = takeU32(rest, quoteEntryMax); err != nil {
			return nil, err
		}
		set := make(map[[16]byte]struct{}, count)
		for j := 0; j < count; j++ {
			var h [16]byte
			if len(rest) < len(h) {
				return nil, gitFail("the private doctrine guard could not be read")
			}
			copy(h[:], rest[:len(h)])
			rest = rest[len(h):]
			set[h] = struct{}{}
		}
		c.entries = append(c.entries, set)
	}
	if len(rest) != 0 {
		return nil, gitFail("the private doctrine guard could not be read")
	}
	return c, nil
}

func takeU32(b []byte, limit int) (int, []byte, error) {
	if len(b) < 4 {
		return 0, nil, gitFail("the private doctrine guard could not be read")
	}
	n := int(binary.BigEndian.Uint32(b[:4]))
	if n < 0 || n > limit {
		return 0, nil, gitFail("the private doctrine guard could not be read")
	}
	return n, b[4:], nil
}

// readPrivateCorpus hashes every text blob at commit. Paths are not consulted:
// narrowing what the index shows must not narrow what a public proposal is
// checked against. Binary blobs have no doctrine text; anything unreadable
// fails the build. The returned bytes contain hashes only.
func readPrivateCorpus(ctx context.Context, r Reader, repository, commit string) ([]byte, error) {
	entries, err := r.Tree(ctx, repository, commit)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, gitFail("the private doctrine tree has no files")
	}
	c := newGuardCorpus()
	total, texts := 0, 0
	for _, e := range entries {
		if e.Size < 0 || e.Size > maxGuardFile {
			return nil, gitFail("a private doctrine file is larger than the guard can cover")
		}
		raw, err := r.Blob(ctx, repository, e.SHA, e.Size)
		if err != nil {
			return nil, err
		}
		if len(raw) > maxGuardFile || total > maxGuardTotal-len(raw) {
			return nil, gitFail("the private doctrine tree is larger than the guard can cover")
		}
		total += len(raw)
		if bytes.Contains(raw, []byte{0}) {
			continue
		}
		if !utf8.Valid(raw) {
			return nil, gitFail("a private doctrine file is not UTF-8 text")
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		texts++
		c.add(e.Path, string(raw))
	}
	if texts == 0 || c.empty() {
		return nil, gitFail("the private doctrine tree has no text to guard")
	}
	return c.marshal()
}
