// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// spec is one knowledge type: the wire (CLI) type, the node kind slug and the
// defaults used when a tenant does not have the kind yet.
type spec struct {
	Type   string
	Kind   string
	Label  string
	Prefix string
}

// specs is also the display order: procedures first, then rules, then memory.
var specs = []spec{
	{Type: "runbook", Kind: "runbook", Label: "Runbook", Prefix: "RUN"},
	{Type: "guideline", Kind: "guideline", Label: "Guideline", Prefix: "GUI"},
	{Type: "memory", Kind: "memory", Label: "Memory", Prefix: "MEM"},
	{Type: "external-system", Kind: "external_system", Label: "External system", Prefix: "EXT"},
	{Type: "related-project", Kind: "related_project", Label: "Related project", Prefix: "RPR"},
	{Type: "decision", Kind: "decision", Label: "Decision", Prefix: "DCS"},
}

func kindSlugs() []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Kind
	}
	return out
}

// specFor accepts the wire type or the kind slug ("external-system" or "external_system").
func specFor(value string) (spec, bool) {
	v := strings.TrimSpace(strings.ToLower(value))
	for _, s := range specs {
		if v == s.Type || v == s.Kind {
			return s, true
		}
	}
	return spec{}, false
}

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// keyPrefix is a node key prefix (the project's key, so entries number like its tickets).
var keyPrefix = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

const maxSlug = 64

// Classic memory sub-routes; a memory slug with these names would shadow them.
var reservedMemory = map[string]bool{"references": true, "stale": true, "proposed": true, "needs-review": true}

func slugProblem(s spec, slug string) string {
	switch {
	case slug == "":
		return "slug is required"
	case len(slug) > maxSlug:
		return "slug is longer than 64 characters"
	case !slugPattern.MatchString(slug):
		return "slug must start with a letter and use only a-z, 0-9, - and _"
	case s.Kind == "memory" && reservedMemory[slug]:
		return "slug " + slug + " is reserved for memory"
	}
	return ""
}

// Status classes. Stored states follow classic Paimos: active entries are
// backlog, archived entries cancelled.
const (
	statusActive   = "active"
	statusProposed = "proposed"
	statusArchived = "archived"
)

func statusOf(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "cancelled", "canceled", "archived":
		return statusArchived
	case "proposed":
		return statusProposed
	}
	return statusActive
}

func stateFor(status string) (string, bool) {
	switch status {
	case statusActive:
		return "backlog", true
	case statusProposed:
		return "proposed", true
	case statusArchived:
		return "cancelled", true
	}
	return "", false
}

// Person is a principal as shown to people (linked principals resolve to their target).
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectRef is the nearest project above an entry.
type ProjectRef struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

// Item is one row of a knowledge list.
type Item struct {
	ID        string      `json:"id"`
	Key       string      `json:"key"`
	Type      string      `json:"type"`
	Kind      string      `json:"kind"`
	Slug      string      `json:"slug"`
	Title     string      `json:"title"`
	Status    string      `json:"status"`
	State     string      `json:"state"`
	Project   *ProjectRef `json:"project"`
	Excerpt   string      `json:"excerpt"`
	LinkCount int         `json:"link_count"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	UpdatedBy *Person     `json:"updated_by"`
	// Imported: the last write came from the classic import, not a person here.
	Imported bool `json:"imported"`
}

// LinkedNode is the other end of a relation.
type LinkedNode struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	Title     string  `json:"title"`
	State     string  `json:"state"`
	Kind      string  `json:"kind"`
	ProjectID *string `json:"project_id"`
	// Slug and Type are set when the other end is a knowledge entry.
	Slug string `json:"slug,omitempty"`
	Type string `json:"type,omitempty"`
}

// Link is one relation of an entry, seen from the entry.
type Link struct {
	RelationID string     `json:"relation_id"`
	Type       string     `json:"type"`
	Direction  string     `json:"direction"`
	Node       LinkedNode `json:"node"`
}

// Entry is one knowledge entry with everything the entry page shows.
type Entry struct {
	Item
	Body        string         `json:"body"`
	Metadata    map[string]any `json:"metadata"`
	Author      *Person        `json:"author"`
	Links       []Link         `json:"links"`
	RenamedFrom string         `json:"renamed_from,omitempty"`
	// EventID is the event a write appended (for Undo); absent on reads and no-op writes.
	EventID *int64 `json:"event_id,omitempty"`
}

// nodeSnap mirrors the nodes package's node JSON so event snapshots have one shape.
type nodeSnap struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	KindID    string          `json:"kind_id"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Fields    json.RawMessage `json:"fields"`
	State     string          `json:"state"`
	ParentID  *string         `json:"parent_id"`
	Position  string          `json:"position"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *time.Time      `json:"deleted_at"`
}

func (n nodeSnap) fieldMap() map[string]any {
	out := map[string]any{}
	if len(n.Fields) > 0 {
		_ = json.Unmarshal(n.Fields, &out)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func (n nodeSnap) slug() string {
	s, _ := n.fieldMap()["slug"].(string)
	return s
}

// metadataProblem checks the values the entry page edits; unknown keys pass.
func metadataProblem(meta map[string]any) string {
	for _, key := range []string{"url", "instance_url"} {
		raw, ok := meta[key]
		if !ok || raw == nil {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return key + " must be text"
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(s))
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return key + " must be an absolute http or https URL"
		}
	}
	return ""
}

// ---------- Plain-text excerpts ----------

var (
	fence       = regexp.MustCompile("(?s)```.*?```")
	image       = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	linkText    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	headingMark = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	listMark    = regexp.MustCompile(`(?m)^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?`)
	quoteMark   = regexp.MustCompile(`(?m)^\s*>\s?`)
	tableRule   = regexp.MustCompile(`(?m)^\s*\|?\s*:?-{2,}.*$`)
	emphasis    = regexp.MustCompile("\\*\\*|__|~~|[*`]")
	spaces      = regexp.MustCompile(`\s+`)
)

// plainText strips Markdown to readable prose for list excerpts. A heading
// reads as a lead-in ("Steps: ...") and a list item without its own ending
// is separated from the next one by a comma.
func plainText(body string) string {
	s := fence.ReplaceAllString(body, "\n")
	s = image.ReplaceAllString(s, "$1")
	s = linkText.ReplaceAllString(s, "$1")
	s = tableRule.ReplaceAllString(s, "")
	var parts []string
	for _, line := range strings.Split(s, "\n") {
		heading := headingMark.MatchString(line)
		item := listMark.MatchString(line)
		line = headingMark.ReplaceAllString(line, "")
		line = listMark.ReplaceAllString(line, "")
		line = quoteMark.ReplaceAllString(line, "")
		line = strings.ReplaceAll(line, "|", " ")
		line = emphasis.ReplaceAllString(line, "")
		line = strings.TrimSpace(spaces.ReplaceAllString(line, " "))
		if line == "" {
			continue
		}
		if ended := strings.ContainsAny(line[len(line)-1:], ".:!?;,"); !ended && heading {
			line += ":"
		} else if !ended && item {
			line += ","
		}
		parts = append(parts, line)
	}
	out := strings.Join(parts, " ")
	return strings.TrimSuffix(out, ",")
}

const excerptRunes = 200

// excerpt returns about 200 characters of body text: the start, or a window
// around the first query word found. A leading copy of the title is dropped.
// cut says body is a window that starts inside the text.
func excerpt(body, title string, words []string, cut bool) string {
	if !cut {
		body = withoutTitle(body, title)
	}
	text := plainText(body)
	if cut {
		if i := strings.IndexFunc(text, unicode.IsSpace); i > 0 {
			text = "…" + strings.TrimSpace(text[i:])
		}
	}
	runes := []rune(text)
	if len(runes) <= excerptRunes {
		return text
	}
	start := 0
	lower := strings.ToLower(text)
	for _, w := range words {
		if i := strings.Index(lower, strings.ToLower(w)); i >= 0 {
			at := utf8.RuneCountInString(text[:i])
			if at > excerptRunes/3 {
				start = at - excerptRunes/3
			}
			break
		}
	}
	if start+excerptRunes > len(runes) {
		start = max(0, len(runes)-excerptRunes)
	}
	// Start and end on word boundaries.
	if start > 0 {
		for start < len(runes) && !unicode.IsSpace(runes[start-1]) {
			start++
		}
	}
	end := min(len(runes), start+excerptRunes)
	if end < len(runes) {
		for end > start && !unicode.IsSpace(runes[end]) {
			end--
		}
	}
	out := strings.TrimSpace(string(runes[start:end]))
	if start > 0 && !strings.HasPrefix(out, "…") {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

// withoutTitle drops a first heading that only repeats the title.
func withoutTitle(body, title string) string {
	trimmed := strings.TrimLeft(body, " \t\r\n")
	line, rest, _ := strings.Cut(trimmed, "\n")
	if !headingMark.MatchString(line) {
		return body
	}
	// Words only, so "ADR-001 · Foundation" is the start of "ADR-001 · Foundation (accepted)".
	norm := func(s string) string {
		return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
	}
	heading, name := norm(headingMark.ReplaceAllString(line, "")), norm(title)
	if heading != name && (len(heading) < 8 || !strings.HasPrefix(name, heading+" ")) {
		return body
	}
	return rest
}

// queryWords splits a search into words for excerpts and body probes.
func queryWords(q string) []string {
	var out []string
	for _, w := range strings.Fields(q) {
		w = strings.Trim(w, `"'()`)
		if utf8.RuneCountInString(w) >= 2 {
			out = append(out, w)
		}
	}
	return out
}

func longest(words []string) string {
	best := ""
	for _, w := range words {
		if len(w) > len(best) {
			best = w
		}
	}
	return best
}

// likePattern escapes a value for ILIKE with backslash as the escape character.
func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}
