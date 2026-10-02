// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/inspr-at/paimos/internal/markdownsource"
)

var (
	listRE          = regexp.MustCompile(`^(\s*)([-*]|\d+\.)\s+(.*)$`)
	explicitIDRE    = regexp.MustCompile(`^<!--\s*aeon-rule:\s*([a-z0-9][a-z0-9._-]{0,63})\s*-->$`)
	looseIDRE       = regexp.MustCompile(`^<!--\s*aeon-rule:`)
	personalHeading = regexp.MustCompile(`(?i)^personal(?:\s+section)?\b`)
	whyRE           = regexp.MustCompile(`(?i)^why:\s*(.*)$`)
	detailsRE       = regexp.MustCompile(`(?i)^details:\s*(.*)$`)
	rolesRE         = regexp.MustCompile(`(?i)^roles:\s*(.*)$`)
	harnessRE       = regexp.MustCompile(`(?i)^harness(?:es)?:\s*(.*)$`)
	expiresLooseRE  = regexp.MustCompile(`(?i)^expires:`)
	sourceRE        = regexp.MustCompile(`(?i)^source:\s*([A-Z][A-Z0-9]{1,9}-[1-9][0-9]{0,6})\s*$`)
	sourceLooseRE   = regexp.MustCompile(`(?i)^source:`)
)

type rawRule struct {
	explicitID          string
	text                string
	why                 string
	details             []string
	roles               []string
	harnesses           []string
	expires             string
	source              string
	enabled             bool
	locked              bool
	unspecifiedStrength bool
	start               int
	end                 int
	setSlug             string
	setTitle            string
	headingPath         string
	layer               Layer
	placement           string
	extraWhy            bool
	stale               []taggedLine
	badExpiry           []taggedLine
	badSource           []taggedLine
}

type taggedLine struct {
	n    int
	text string
}

type fileParse struct {
	rules      []rawRule
	unresolved []Unresolved
	lines      []string
}

type headingFrame struct {
	level    int
	title    string
	slug     string
	personal bool
	locked   bool
}

type parser struct {
	lines   []string
	file    SourceFile
	section string

	headings   map[int]mdHeading
	stack      []headingFrame
	h1         string
	setSlug    string
	setTitle   string
	personal   bool
	lockedHead bool

	current   *rawRule
	pendingID string
	pendingLn int
	blanks    int
	codeLines []bool

	sawPersonal bool
	sawOther    bool
	prose       int
	tables      int
	fences      int
	skipped     int

	rules      []rawRule
	unresolved []Unresolved
}

func parseDocument(file SourceFile, text, section string) (fileParse, error) {
	p := &parser{
		lines:     splitLines(text),
		headings:  indexATXHeadings(text),
		codeLines: markdownsource.CodeLines(text),
		file:      file,
		section:   section,
		setSlug:   "preamble",
		setTitle:  "Preamble",
		personal:  file.Kind == "profile",
	}
	p.scan()
	return p.finish()
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func (p *parser) scan() {
	for i, line := range p.lines {
		n := i + 1
		trim := strings.TrimSpace(line)
		if p.codeLines[i] {
			if p.current != nil {
				p.appendDetail(n, line)
			} else if i == 0 || !p.codeLines[i-1] {
				p.fences++
			}
			continue
		}
		if id, ok := explicitID(trim); ok {
			p.noteID(n, id, trim)
			continue
		}
		if h, ok := p.headings[n]; ok && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			p.heading(n, h.level, h.title)
			continue
		}
		if m := listRE.FindStringSubmatch(strings.ReplaceAll(line, "\t", "  ")); m != nil {
			p.item(n, len(m[1]), m[3])
			continue
		}
		if trim == "" {
			if p.current != nil {
				p.blanks++
			}
			continue
		}
		if p.current != nil {
			if body, ok := continuation(line); ok {
				p.continuation(n, body)
				continue
			}
		}
		if strings.HasPrefix(trim, "|") {
			p.tables++
			continue
		}
		if trim != "<!-- aeon-context: template -->" {
			p.prose++
		}
	}
	p.flush(len(p.lines))
}

func (p *parser) noteID(n int, id, raw string) {
	if p.current != nil {
		p.appendDetail(n, raw)
		return
	}
	if id == "" {
		p.unresolved = append(p.unresolved, Unresolved{
			Kind: "explicit_id_invalid",
			Path: p.file.Path,
			Line: n,
			Note: "aeon-rule marker ignored because the id is not a stable token",
			Text: excerpt(raw),
		})
		p.pendingID = ""
		p.pendingLn = 0
		return
	}
	if p.pendingID != "" {
		p.unresolved = append(p.unresolved, Unresolved{
			Kind: "explicit_id_unused",
			Path: p.file.Path,
			Line: p.pendingLn,
			Note: "aeon-rule marker was replaced before a rule",
			Text: p.pendingID,
		})
	}
	p.pendingID = id
	p.pendingLn = n
}

func explicitID(trim string) (string, bool) {
	if m := explicitIDRE.FindStringSubmatch(trim); m != nil {
		return m[1], true
	}
	if looseIDRE.MatchString(trim) {
		return "", true
	}
	return "", false
}

func (p *parser) heading(n, level int, title string) {
	p.flush(n - 1)
	p.dropPending(n)
	title = strings.TrimSpace(title)
	if level == 1 {
		p.stack = nil
		p.h1 = title
		p.setSlug = "preamble"
		p.setTitle = "Preamble"
		p.personal = p.file.Kind == "profile" || p.headingPersonal(title)
		p.lockedHead = headingMarksLocked(title)
		return
	}
	for len(p.stack) > 0 && p.stack[len(p.stack)-1].level >= level {
		p.stack = p.stack[:len(p.stack)-1]
	}
	parentPersonal := len(p.stack) > 0 && p.stack[len(p.stack)-1].personal
	parentLocked := len(p.stack) > 0 && p.stack[len(p.stack)-1].locked
	frame := headingFrame{
		level:    level,
		title:    title,
		slug:     slug(title),
		personal: parentPersonal || p.headingPersonal(title),
		locked:   parentLocked || headingMarksLocked(title),
	}
	p.stack = append(p.stack, frame)
	var slugs, titles []string
	for _, frame := range p.stack {
		slugs = append(slugs, frame.slug)
		titles = append(titles, frame.title)
	}
	p.setSlug = strings.Join(slugs, "/")
	p.setTitle = strings.Join(titles, " / ")
	p.personal = frame.personal
	p.lockedHead = frame.locked
}

func (p *parser) headingPersonal(title string) bool {
	if p.file.Kind != "claude" && p.file.Kind != "repo" {
		return false
	}
	return personalHeading.MatchString(strings.TrimSpace(title))
}

func (p *parser) dropPending(line int) {
	if p.pendingID == "" {
		return
	}
	p.unresolved = append(p.unresolved, Unresolved{
		Kind: "explicit_id_unused",
		Path: p.file.Path,
		Line: p.pendingLn,
		Note: "aeon-rule marker was not followed by a rule",
		Text: p.pendingID,
	})
	p.pendingID = ""
	p.pendingLn = 0
	_ = line
}

func (p *parser) item(n, indent int, text string) {
	text = strings.TrimSpace(text)
	nested := indent >= 2 && p.current != nil
	own := !nested || explicitLocked(text) || p.pendingID != ""
	if !own {
		p.appendDetail(n, "- "+text)
		return
	}
	p.flush(n - 1)
	start := n
	if p.pendingLn != 0 {
		start = p.pendingLn
	}
	rule := &rawRule{
		explicitID:  p.pendingID,
		text:        text,
		enabled:     true,
		locked:      explicitLocked(text),
		start:       start,
		end:         n,
		setSlug:     p.setSlug,
		setTitle:    p.setTitle,
		headingPath: p.headingPath(),
		layer:       p.file.Layer,
		placement:   p.file.Placement,
	}
	p.pendingID = ""
	p.pendingLn = 0
	if p.file.Role != "" {
		rule.roles = append(rule.roles, p.file.Role)
	}
	// 🟡 is an explicit normal-strength mark. The shipped model has only normal
	// and locked, so caution does not become a third strength and does not lock.
	if p.lockedHead && !rule.locked && !explicitCaution(text) {
		rule.unspecifiedStrength = true
	}
	if strings.Contains(strings.ToLower(text), "[off]") || strings.Contains(strings.ToLower(text), "enabled: false") {
		rule.enabled = false
	}
	p.current = rule
	p.blanks = 0
}

func (p *parser) continuation(n int, body string) {
	if p.blanks > 0 {
		for i := 0; i < p.blanks; i++ {
			p.current.details = append(p.current.details, "")
		}
		p.blanks = 0
	}
	p.current.end = n
	trim := strings.TrimSpace(body)
	if m := whyRE.FindStringSubmatch(trim); m != nil {
		if p.current.why == "" {
			p.current.why = strings.TrimSpace(m[1])
		} else {
			p.current.extraWhy = true
			p.current.details = append(p.current.details, trim)
		}
		return
	}
	if m := detailsRE.FindStringSubmatch(trim); m != nil {
		if strings.TrimSpace(m[1]) != "" {
			p.current.details = append(p.current.details, strings.TrimSpace(m[1]))
		}
		return
	}
	if m := rolesRE.FindStringSubmatch(trim); m != nil {
		p.current.roles = append(p.current.roles, splitList(m[1])...)
		return
	}
	if m := harnessRE.FindStringSubmatch(trim); m != nil {
		p.current.harnesses = append(p.current.harnesses, splitList(m[1])...)
		return
	}
	if expiresLooseRE.MatchString(trim) {
		value := strings.TrimSpace(strings.SplitN(trim, ":", 2)[1])
		_, instantErr := time.Parse(time.RFC3339, value)
		if p.current.expires != "" {
			p.current.badExpiry = append(p.current.badExpiry, taggedLine{n: n, text: trim})
			p.current.details = append(p.current.details, trim)
		} else if instantErr == nil || validDate(value) {
			p.current.expires = value
		} else {
			p.current.badExpiry = append(p.current.badExpiry, taggedLine{n: n, text: trim})
			p.current.details = append(p.current.details, trim)
		}
		return
	}
	if sourceLooseRE.MatchString(trim) {
		if m := sourceRE.FindStringSubmatch(trim); m != nil && p.current.source == "" {
			p.current.source = m[1]
		} else {
			p.current.badSource = append(p.current.badSource, taggedLine{n: n, text: trim})
			p.current.details = append(p.current.details, trim)
		}
		return
	}
	if staleModelRoute(trim) {
		p.current.stale = append(p.current.stale, taggedLine{n: n, text: trim})
	}
	p.current.details = append(p.current.details, body)
}

func (p *parser) appendDetail(n int, line string) {
	if p.blanks > 0 {
		for i := 0; i < p.blanks; i++ {
			p.current.details = append(p.current.details, "")
		}
		p.blanks = 0
	}
	p.current.details = append(p.current.details, line)
	if n > p.current.end {
		p.current.end = n
	}
}

func continuation(line string) (string, bool) {
	if strings.HasPrefix(line, "\t") {
		return strings.TrimPrefix(line, "\t"), true
	}
	if strings.HasPrefix(line, "  ") {
		return strings.TrimPrefix(line, "  "), true
	}
	return "", false
}

func (p *parser) flush(endHint int) {
	if p.current == nil {
		return
	}
	rule := *p.current
	p.current = nil
	p.blanks = 0
	if rule.end == 0 {
		rule.end = endHint
	}
	if rule.end < rule.start {
		rule.end = rule.start
	}
	personal := p.file.Kind == "profile" || p.personal
	if personal {
		p.sawPersonal = true
	} else {
		p.sawOther = true
	}
	if !p.keep(personal) {
		p.skipped++
		return
	}
	if staleModelRoute(rule.text) {
		rule.stale = append(rule.stale, taggedLine{n: rule.start, text: rule.text})
	}
	p.rules = append(p.rules, rule)
}

func (p *parser) keep(personal bool) bool {
	switch p.section {
	case SectionPersonal:
		return personal
	case SectionKernel:
		return !personal
	default:
		return true
	}
}

func (p *parser) finish() (fileParse, error) {
	if p.section == SectionAll && p.sawPersonal && p.sawOther && p.file.Trust != ContextPrivate {
		return fileParse{}, fmt.Errorf("%w: %s contains person and other instructions", ErrMixedContext, p.file.Base)
	}
	if p.prose > 0 {
		p.unresolved = append(p.unresolved, Unresolved{
			Kind: "prose_not_imported",
			Path: p.file.Path,
			Note: fmt.Sprintf("%d non-list lines were not imported as rules", p.prose),
		})
	}
	if p.tables > 0 {
		p.unresolved = append(p.unresolved, Unresolved{
			Kind: "table_not_a_rule",
			Path: p.file.Path,
			Note: fmt.Sprintf("%d table lines were not imported as rules", p.tables),
		})
	}
	if p.fences > 0 {
		p.unresolved = append(p.unresolved, Unresolved{
			Kind: "code_not_imported",
			Path: p.file.Path,
			Note: fmt.Sprintf("%d code fences outside rules were not imported", p.fences),
		})
	}
	if p.section == SectionAll && p.sawPersonal && p.sawOther {
		p.unresolved = append(p.unresolved, Unresolved{Kind: "mixed_personal_context", Path: p.file.Path, Note: "personal and other rules retained locally; split scopes before applying"})
	}
	kept := 0
	for _, rule := range p.rules {
		kept++
		if rule.unspecifiedStrength {
			p.unresolved = append(p.unresolved, Unresolved{
				Kind: "strength_unspecified",
				Path: p.file.Path,
				Line: rule.start,
				Note: "section is marked locked or hard safety, and this line has no explicit strength",
				Text: excerpt(rule.text),
			})
		}
		if rule.extraWhy {
			p.unresolved = append(p.unresolved, Unresolved{
				Kind: "extra_why",
				Path: p.file.Path,
				Line: rule.start,
				Note: "additional why line kept in details",
			})
		}
		for _, line := range rule.stale {
			p.unresolved = append(p.unresolved, Unresolved{
				Kind: "stale_model_or_review_name",
				Path: p.file.Path,
				Line: line.n,
				Note: "model/review routing requires human resolution; source text is retained without applying precedence",
				Text: excerpt(line.text),
			})
		}
		for _, line := range rule.badExpiry {
			p.unresolved = append(p.unresolved, Unresolved{
				Kind: "expiry_unparsed",
				Path: p.file.Path,
				Line: line.n,
				Note: "expiry is repeated or invalid; choose one explicit RFC3339 instant",
				Text: excerpt(line.text),
			})
		}
		for _, line := range rule.badSource {
			p.unresolved = append(p.unresolved, Unresolved{
				Kind: "source_unparsed",
				Path: p.file.Path,
				Line: line.n,
				Note: "source is repeated or not an explicit ticket key; source text retained",
				Text: excerpt(line.text),
			})
		}
	}
	if kept == 0 && p.skipped > 0 {
		p.unresolved = append(p.unresolved, Unresolved{
			Kind: "section_not_found",
			Path: p.file.Path,
			Note: "the requested section contained no rules",
		})
	}
	if kept == 0 && p.skipped == 0 && len(p.rules) == 0 {
		p.unresolved = append(p.unresolved, Unresolved{
			Kind: "no_list_rules",
			Path: p.file.Path,
			Note: "no list rules were found",
		})
	}
	return fileParse{rules: p.rules, unresolved: p.unresolved, lines: p.lines}, nil
}

func (p *parser) headingPath() string {
	if p.h1 == "" {
		return p.setTitle
	}
	if len(p.stack) == 0 {
		// The separator stays so the empty section below the title is visible
		// to lineage matching after the title itself changes.
		return p.h1 + " / "
	}
	return p.h1 + " / " + p.setTitle
}

func explicitCaution(text string) bool {
	return strings.Contains(text, "🟡")
}

func explicitLocked(text string) bool {
	if strings.Contains(text, "🔴") {
		return true
	}
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "[locked]"), strings.Contains(low, "(locked)"), strings.Contains(low, "**locked**"):
		return true
	case strings.Contains(low, "strength: locked"), strings.HasPrefix(low, "locked:"):
		return true
	default:
		return false
	}
}

func headingMarksLocked(title string) bool {
	if strings.Contains(title, "🔴") {
		return true
	}
	low := strings.ToLower(title)
	return strings.Contains(low, "locked") || strings.Contains(low, "hard safety")
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func validDate(value string) bool {
	if len(value) != 10 {
		return false
	}
	var y, m, d int
	if _, err := fmt.Sscanf(value, "%4d-%2d-%2d", &y, &m, &d); err != nil {
		return false
	}
	if m < 1 || m > 12 || d < 1 || y < 2000 || y > 2099 {
		return false
	}
	mdays := []int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if m == 2 && leap(y) {
		mdays[2] = 29
	}
	return d <= mdays[m]
}

func leap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r == '🔴' {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	if len(out) > 80 {
		out = strings.TrimRight(out[:80], "-")
	}
	return out
}

func normalizeText(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "🔴", "")
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space && b.Len() > 0 {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func hashLines(lines []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if end < start {
		end = start
	}
	chunk := strings.Join(lines[start-1:end], "\n") + "\n"
	sum := sha256.Sum256([]byte(chunk))
	return hex.EncodeToString(sum[:])
}

func excerpt(s string) string {
	r := []rune(s)
	if len(r) <= 500 {
		return s
	}
	return string(r[:500]) + "…"
}
