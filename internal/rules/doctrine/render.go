// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"errors"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/inspr-at/paimos/internal/rulesimport"
)

// MaxTLDRBytes bounds one explanation line in one language, as for Aeon's own
// rules (AEON-314).
const MaxTLDRBytes = 300

// TLDR is an explanation for people, read from git. Aeon only displays it;
// agents never receive it. Check is set when the sidecar names the text it
// was written for (basis) and the rule's text has changed since.
type TLDR struct {
	EN    string `json:"en"`
	DE    string `json:"de,omitempty"`
	Check bool   `json:"check,omitempty"`
}

// RuleView is one git-backed rule. Its identity is the repository, the file
// and the heading anchor (plus Key within that heading); Source is the exact
// bytes of its lines at the pinned commit.
type RuleView struct {
	Key         string `json:"key"`
	Identity    string `json:"identity"`
	Set         string `json:"set"`
	HeadingPath string `json:"heading_path"`
	Anchor      string `json:"anchor"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	Source      string `json:"source"`
	SHA256      string `json:"sha256"`
	Text        string `json:"text"`
	Strength    string `json:"strength"`
	URL         string `json:"url"`
	TLDR        *TLDR  `json:"tldr,omitempty"`
}

// SetView is one heading section of a file, in document order.
type SetView struct {
	Set    string `json:"set"`
	Title  string `json:"title"`
	Anchor string `json:"anchor"`
	TLDR   *TLDR  `json:"tldr,omitempty"`
}

// SidecarView reports the TL;DR file next to a doctrine file. Problem is set
// when it could not be used; Unmatched lists keys that name no rule or set at
// this commit (their text changed or they moved).
type SidecarView struct {
	Path      string   `json:"path"`
	URL       string   `json:"url"`
	Problem   string   `json:"problem,omitempty"`
	Unmatched []string `json:"unmatched,omitempty"`
}

// FileView is one indexed doctrine file at the pinned commit.
type FileView struct {
	Path    string       `json:"path"`
	BlobSHA string       `json:"blob_sha"`
	SHA256  string       `json:"sha256"`
	Bytes   int          `json:"bytes"`
	Kind    string       `json:"kind"`
	Layer   string       `json:"layer"`
	URL     string       `json:"url"`
	TLDR    *TLDR        `json:"tldr,omitempty"`
	Sets    []SetView    `json:"sets"`
	Rules   []RuleView   `json:"rules"`
	Sidecar *SidecarView `json:"sidecar,omitempty"`
	Problem string       `json:"problem,omitempty"`
}

// Render indexes the cached files of one source at commit. It reads nothing:
// every byte comes from files, which hold the exact blobs at that commit.
func Render(repository, commit string, private bool, files []File) []FileView {
	byPath := make(map[string]File, len(files))
	for _, f := range files {
		byPath[f.Path] = f
	}
	out := []FileView{}
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".md") {
			continue
		}
		view := FileView{Path: f.Path, BlobSHA: f.BlobSHA, Bytes: len(f.Content), URL: blobURL(repository, commit, f.Path), Sets: []SetView{}, Rules: []RuleView{}}
		sum := sha256.Sum256(f.Content)
		view.SHA256 = hex.EncodeToString(sum[:])
		indexed, err := rulesimport.IndexBytes(f.Path, f.Content, private)
		if err != nil {
			view.Problem = indexProblem(err)
			out = append(out, view)
			continue
		}
		view.Kind = indexed.File.Kind
		view.Layer = string(indexed.File.Layer)
		lines := rawLines(f.Content)
		anchors := headingAnchors(lines)
		seenSet := map[string]bool{}
		for _, rule := range indexed.Rules {
			src := rule.Sources[0]
			exact := strings.Join(lines[src.StartLine-1:src.EndLine], "")
			digest := sha256.Sum256([]byte(exact))
			anchor := anchorAt(anchors, src.StartLine)
			view.Rules = append(view.Rules, RuleView{
				Key: rulesimport.RuleKey(rule), Identity: repository + "/" + f.Path + "#" + anchor,
				Set: rule.Set, HeadingPath: src.HeadingPath, Anchor: anchor,
				StartLine: src.StartLine, EndLine: src.EndLine, Source: exact, SHA256: hex.EncodeToString(digest[:]),
				Text: rule.Text, Strength: rule.Strength, URL: lineURL(repository, commit, f.Path, src.StartLine, src.EndLine),
			})
			if !seenSet[rule.Set] {
				seenSet[rule.Set] = true
				view.Sets = append(view.Sets, SetView{Set: rule.Set, Title: rule.SetTitle, Anchor: anchor})
			}
		}
		if side, ok := byPath[SidecarPath(f.Path)]; ok {
			view.Sidecar = &SidecarView{Path: side.Path, URL: blobURL(repository, commit, side.Path)}
			applySidecar(&view, side.Content)
		}
		out = append(out, view)
	}
	return out
}

func indexProblem(err error) string {
	switch {
	case errors.Is(err, rulesimport.ErrNotText):
		return "not UTF-8 text"
	case errors.Is(err, rulesimport.ErrByteBound):
		return "larger than 256 KiB"
	case errors.Is(err, rulesimport.ErrMixedContext):
		return "mixes personal and other rules in a public repository"
	case errors.Is(err, rulesimport.ErrUnrecognizedFile):
		return "not a doctrine rule file"
	default:
		return "could not be indexed"
	}
}

// ---------- TL;DR sidecar ----------

// The sidecar next to docs/AGENTS-KERNEL.md is docs/AGENTS-KERNEL.tldr.yaml:
//
//	file: {en: One line for the whole file, de: Eine Zeile}
//	sets:
//	  hard-safety-irreversibles/secrets: {en: …}
//	rules:
//	  no-force: {en: …, basis: 0123456789abcdef}
//	  t-3f2a9c0d1e4b5a6c7d8e: {en: …}
//
// Rule keys are the rule's explicit aeon-rule id, or the text key Aeon shows
// for a rule without one (it changes with the text, so a stale explanation
// drops out on its own). basis is optional: the first 16 hex digits of the
// SHA-256 of the rule's text line (for a set: of its rules' keys and text
// lines, see setBasis). When it no longer matches, the explanation is shown
// marked for a check.
type sidecarText struct {
	EN    string `yaml:"en"`
	DE    string `yaml:"de"`
	Basis string `yaml:"basis"`
}

type sidecarFile struct {
	File  *sidecarText           `yaml:"file"`
	Sets  map[string]sidecarText `yaml:"sets"`
	Rules map[string]sidecarText `yaml:"rules"`
}

func applySidecar(view *FileView, raw []byte) {
	if len(raw) > MaxSidecarBytes || !utf8.Valid(raw) {
		view.Sidecar.Problem = "not UTF-8 text of at most 64 KiB"
		return
	}
	var side sidecarFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&side); err != nil {
		view.Sidecar.Problem = "not valid: expected file, sets and rules, each with en and optional de and basis"
		return
	}
	if side.File != nil {
		if problem := side.File.problem(); problem != "" {
			view.Sidecar.Problem = "file: " + problem
			return
		}
	}
	for _, group := range []map[string]sidecarText{side.Sets, side.Rules} {
		keys := make([]string, 0, len(group))
		for key := range group {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			text := group[key]
			if problem := text.problem(); problem != "" {
				view.Sidecar.Problem = fmt.Sprintf("%.80s: %s", key, problem)
				return
			}
		}
	}
	if side.File != nil {
		view.TLDR = side.File.tldr("")
	}
	used := map[string]bool{}
	for i := range view.Rules {
		rule := &view.Rules[i]
		if text, ok := side.Rules[rule.Key]; ok {
			rule.TLDR = text.tldr(textBasis(rule.Text))
			used["rules."+rule.Key] = true
		}
	}
	for i := range view.Sets {
		set := &view.Sets[i]
		if text, ok := side.Sets[set.Set]; ok {
			set.TLDR = text.tldr(setBasis(view.Rules, set.Set))
			used["sets."+set.Set] = true
		}
	}
	var unmatched []string
	for key := range side.Rules {
		if !used["rules."+key] {
			unmatched = append(unmatched, "rules."+key)
		}
	}
	for key := range side.Sets {
		if !used["sets."+key] {
			unmatched = append(unmatched, "sets."+key)
		}
	}
	sort.Strings(unmatched)
	if len(unmatched) > 50 {
		unmatched = unmatched[:50]
	}
	view.Sidecar.Unmatched = unmatched
}

func (t sidecarText) problem() string {
	for _, v := range []string{t.EN, t.DE} {
		if len(v) > MaxTLDRBytes || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) || r == ' ' || r == ' ' }) {
			return fmt.Sprintf("each language is one line of at most %d bytes", MaxTLDRBytes)
		}
	}
	if strings.TrimSpace(t.EN) == "" {
		return "en is required"
	}
	if t.Basis != "" && (len(t.Basis) != 16 || strings.Trim(t.Basis, "0123456789abcdef") != "") {
		return "basis is 16 lowercase hex digits"
	}
	return ""
}

func (t sidecarText) tldr(basis string) *TLDR {
	return &TLDR{EN: strings.TrimSpace(t.EN), DE: strings.TrimSpace(t.DE), Check: t.Basis != "" && basis != "" && t.Basis != basis}
}

// textBasis fingerprints the one line agents read for a rule, as Aeon's own
// rule explanations do (AEON-314).
func textBasis(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

// setBasis fingerprints a set: its rules' keys and text lines, in key order.
func setBasis(rules []RuleView, set string) string {
	var members []RuleView
	for _, r := range rules {
		if r.Set == set {
			members = append(members, r)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Key < members[j].Key })
	var b strings.Builder
	for _, r := range members {
		b.WriteString(r.Key)
		b.WriteByte(0)
		b.WriteString(r.Text)
		b.WriteByte('\n')
	}
	return textBasis(b.String())
}

// ---------- Lines, anchors and links ----------

// rawLines splits the exact bytes into lines that keep their terminators. A
// line ends at LF, CRLF or a lone CR, as the importer's line numbers count.
func rawLines(raw []byte) []string {
	var lines []string
	start := 0
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\n':
			lines = append(lines, string(raw[start:i+1]))
			start = i + 1
		case '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
			lines = append(lines, string(raw[start:i+1]))
			start = i + 1
		}
	}
	if start < len(raw) {
		lines = append(lines, string(raw[start:]))
	}
	return lines
}

type anchorLine struct {
	line   int
	anchor string
}

// headingAnchors lists the ATX headings outside code fences with the anchor
// GitHub renders for each, duplicates numbered as GitHub numbers them.
func headingAnchors(lines []string) []anchorLine {
	var out []anchorLine
	seen := map[string]int{}
	fence := ""
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		trim := strings.TrimLeft(line, " ")
		if len(line)-len(trim) > 3 {
			continue
		}
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			marker := trim[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		level := len(trim) - len(strings.TrimLeft(trim, "#"))
		if level < 1 || level > 6 || (len(trim) > level && trim[level] != ' ' && trim[level] != '\t') {
			continue
		}
		title := strings.TrimSpace(trim[level:])
		title = strings.TrimSpace(strings.TrimRight(title, "#"))
		slug := githubSlug(title)
		anchor := slug
		if n := seen[slug]; n > 0 {
			anchor = fmt.Sprintf("%s-%d", slug, n)
		}
		seen[slug]++
		out = append(out, anchorLine{line: i + 1, anchor: anchor})
	}
	return out
}

// githubSlug is GitHub's heading anchor: lower case, letters, digits, spaces,
// hyphens and underscores kept, every space a hyphen.
func githubSlug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

func anchorAt(anchors []anchorLine, line int) string {
	anchor := ""
	for _, a := range anchors {
		if a.line >= line {
			break
		}
		anchor = a.anchor
	}
	return anchor
}

func escapedPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func blobURL(repository, commit, file string) string {
	return "https://github.com/" + repository + "/blob/" + commit + "/" + escapedPath(file)
}

// lineURL opens the file's source view at the rule's exact lines.
func lineURL(repository, commit, file string, start, end int) string {
	u := blobURL(repository, commit, file) + "?plain=1#L" + fmt.Sprint(start)
	if end > start {
		u += fmt.Sprintf("-L%d", end)
	}
	return u
}

func treeURL(repository, commit string) string {
	return "https://github.com/" + repository + "/tree/" + commit
}
