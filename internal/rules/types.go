// SPDX-License-Identifier: AGPL-3.0-only

// Package rules implements ADR-004 nodes, immutable snapshots and deterministic merging.
package rules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/workorders"
)

// MaxBytes is the default always-on budget of one merged session file. A
// workspace may configure its own budget between MinBudgetBytes and
// CeilingBytes (AEON-314); clients that only check a received file's size use
// CeilingBytes, the largest file any workspace can be served.
const MaxBytes = 12000
const MinBudgetBytes = 2000
const CeilingBytes = 64000
const MaxRules = 100

// MaxTLDRBytes bounds one explanation line in one language.
const MaxTLDRBytes = 300

var identityPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,95}$`)
var Roles = []string{"coordinator", "builder", "reviewer", "operator"}
var Harnesses = []string{"claude-code", "codex", "grok", "pi", "cursor"}

type Scope struct {
	Layer     string `json:"layer"`
	ProjectID string `json:"project_id,omitempty"`
	OwnerID   string `json:"owner_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	Role      string `json:"role,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
}
type Source struct {
	Reference  string `json:"reference"`
	Revision   string `json:"revision,omitempty"`
	Identity   string `json:"identity,omitempty"`
	EditedHere bool   `json:"edited_here"`
}
type Rule struct {
	Identity  string     `json:"identity"`
	Text      string     `json:"text"`
	Why       string     `json:"why"`
	Details   string     `json:"details,omitempty"`
	Strength  string     `json:"strength"`
	Enabled   bool       `json:"enabled"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Roles     []string   `json:"roles,omitempty"`
	Harnesses []string   `json:"harnesses,omitempty"`
	Source    Source     `json:"source"`
	// TLDR is for people only: Merge drops it, so agents never receive it.
	TLDR *TLDR `json:"tldr,omitempty"`
}

// TLDR is a short, technical explanation of a rule or a set for people
// (AEON-314). It lives in the draft and in every published snapshot, so it is
// versioned and approved with the normal publication, and it is never part of
// what agents receive: Merge removes it before anything is rendered.
type TLDR struct {
	EN string `json:"en"`
	DE string `json:"de,omitempty"`
	// Basis fingerprints the text the explanation was written for (see
	// RuleBasis and SetBasis). The server stamps it when it is empty.
	Basis string `json:"basis,omitempty"`
	// Check is derived on every read and never stored: the explained text
	// changed after the explanation was written, so a person should check it.
	Check bool `json:"check,omitempty"`
}

// RuleBasis fingerprints the one line agents read for a rule.
func RuleBasis(r Rule) string { return digest([]byte(r.Text))[:16] }

// SetBasis fingerprints what a set tells agents: every rule's identity and
// text, in identity order. Switching a rule on or off does not change it.
func SetBasis(rules []Rule) string {
	sorted := slices.Clone(rules)
	slices.SortFunc(sorted, func(a, b Rule) int { return strings.Compare(a.Identity, b.Identity) })
	var b strings.Builder
	for _, r := range sorted {
		b.WriteString(r.Identity)
		b.WriteByte(0)
		b.WriteString(r.Text)
		b.WriteByte('\n')
	}
	return digest([]byte(b.String()))[:16]
}

// storedTLDR is the form written to a draft: trimmed, never marked for a
// check, and stamped with basis when the caller did not name one (written now).
func storedTLDR(t *TLDR, basis string) *TLDR {
	if t == nil {
		return nil
	}
	out := TLDR{EN: strings.TrimSpace(t.EN), DE: strings.TrimSpace(t.DE), Basis: t.Basis}
	if out.Basis == "" {
		out.Basis = basis
	}
	return &out
}

// checked returns a copy marked for a check when its basis no longer matches.
func checked(t *TLDR, basis string) *TLDR {
	if t == nil {
		return nil
	}
	out := *t
	out.Check = out.Basis != basis
	return &out
}

func validTLDR(t *TLDR) bool {
	if t == nil {
		return true
	}
	if !line(t.EN, MaxTLDRBytes, true) || !line(t.DE, MaxTLDRBytes, false) {
		return false
	}
	if t.Basis == "" {
		return true
	}
	if len(t.Basis) != 16 {
		return false
	}
	for _, r := range t.Basis {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// ValidateTLDR checks one explanation as a request states it (before stamping).
func ValidateTLDR(t *TLDR) error {
	if t != nil {
		trimmed := TLDR{EN: strings.TrimSpace(t.EN), DE: strings.TrimSpace(t.DE), Basis: t.Basis}
		if !validTLDR(&trimmed) {
			return fail(400, "invalid_tldr", fmt.Sprintf("an explanation needs English text; each language is one line of at most %d UTF-8 bytes", MaxTLDRBytes))
		}
	}
	return nil
}

type Layer struct {
	ID    string `json:"id"`
	Scope Scope  `json:"scope"`
}
type Set struct {
	ID               string `json:"id"`
	LayerID          string `json:"layer_id"`
	Scope            Scope  `json:"scope"`
	Name             string `json:"name"`
	Revision         int64  `json:"revision"`
	Rules            []Rule `json:"rules"`
	PublishedVersion string `json:"published_version"`
	// TLDR explains the whole set to people; never sent to agents.
	TLDR *TLDR `json:"tldr,omitempty"`
}
type Snapshot struct {
	SetID       string    `json:"set_id"`
	Scope       Scope     `json:"scope"`
	Name        string    `json:"name"`
	Revision    int64     `json:"revision"`
	Version     string    `json:"version"`
	SHA256      string    `json:"sha256"`
	Rules       []Rule    `json:"rules"`
	PublishedAt time.Time `json:"published_at"`
	// Note is omitted when empty so historical snapshots keep their digest.
	Note string `json:"note,omitempty"`
	// TLDR is omitted when empty so historical snapshots keep their digest.
	TLDR *TLDR `json:"tldr,omitempty"`
}
type Context struct {
	TenantID  string `json:"tenant_id"`
	ProjectID string `json:"project_id"`
	PersonID  string `json:"person_id"`
	AgentID   string `json:"agent_id,omitempty"`
	Role      string `json:"role"`
	Harness   string `json:"harness"`
	TaskID    string `json:"task_id,omitempty"`
}
type VersionRef struct {
	SetID   string `json:"set_id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
type Merged struct {
	Context    Context      `json:"context"`
	Versions   []VersionRef `json:"versions"`
	Version    string       `json:"version"`
	SHA256     string       `json:"sha256"`
	Body       string       `json:"body"`
	ByteSize   int          `json:"byte_size"`
	Rules      []Rule       `json:"rules"`
	Floor      string       `json:"floor"`
	ValidUntil *time.Time   `json:"valid_until"`
}
type Error struct {
	Status      int    `json:"-"`
	Code        string `json:"code"`
	Message     string `json:"error"`
	ActualBytes int    `json:"actual_bytes,omitempty"`
	MaxBytes    int    `json:"max_bytes,omitempty"`
	// Layer names the layer whose own cap was crossed; empty for the total.
	Layer string `json:"layer,omitempty"`
}

func (e *Error) Error() string { return e.Message }
func fail(status int, code, message string) error {
	return &Error{Status: status, Code: code, Message: message}
}
func digest(b []byte) string  { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func jsonDigest(v any) string { b, _ := json.Marshal(v); return digest(b) }

func ValidateScope(s Scope) error {
	for _, id := range []string{s.ProjectID, s.OwnerID, s.AgentID, s.TaskID} {
		if id != "" && (!workorders.UUID(id) || strings.ToLower(id) != id) {
			return fail(400, "invalid_scope", "scope IDs must be canonical UUIDs")
		}
	}
	valid := false
	switch s.Layer {
	case "company":
		valid = s.ProjectID == "" && s.OwnerID == "" && s.AgentID == "" && s.Role == "" && s.TaskID == ""
	case "project":
		valid = s.ProjectID != "" && s.OwnerID == "" && s.AgentID == "" && s.Role == "" && s.TaskID == ""
	case "person":
		valid = s.OwnerID != "" && s.ProjectID == "" && s.AgentID == "" && s.Role == "" && s.TaskID == ""
	case "agent":
		valid = (s.Role != "" && slices.Contains(Roles, s.Role) && s.AgentID == "" && s.OwnerID == "" && s.ProjectID == "" && s.TaskID == "") ||
			(s.Role == "" && s.OwnerID != "" && s.AgentID != "" && ((s.ProjectID == "" && s.TaskID == "") || (s.ProjectID != "" && s.TaskID != "")))
	}
	if !valid {
		return fail(400, "invalid_scope", "scope must unambiguously name company, project, person, agent role, owned named agent, or owned agent task")
	}
	return nil
}
func ValidateContext(c Context) error {
	for _, id := range []string{c.TenantID, c.ProjectID, c.PersonID, c.AgentID, c.TaskID} {
		if strings.ToLower(id) != id {
			return fail(400, "invalid_scope", "context IDs must be canonical lowercase UUIDs")
		}
	}
	if !workorders.UUID(c.TenantID) || !workorders.UUID(c.ProjectID) || !workorders.UUID(c.PersonID) || !slices.Contains(Roles, c.Role) || !slices.Contains(Harnesses, c.Harness) || (c.AgentID != "" && !workorders.UUID(c.AgentID)) || (c.TaskID != "" && (!workorders.UUID(c.TaskID) || c.AgentID == "")) {
		return fail(400, "invalid_scope", "exact tenant, project, person, role and harness are required; task also requires agent")
	}
	return nil
}
func line(s string, max int, required bool) bool {
	if !utf8.ValidString(s) || len(s) > max || (required && strings.TrimSpace(s) == "") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}
func ValidateRules(rules []Rule) error {
	if len(rules) > MaxRules {
		return fail(400, "invalid_rule", "at most 100 rules per set")
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if !identityPattern.MatchString(r.Identity) || seen[r.Identity] || !line(r.Text, 512, true) || !line(r.Why, 1024, true) || !utf8.ValidString(r.Details) || len(r.Details) > 16384 || strings.ContainsRune(r.Details, 0) || (r.Strength != "normal" && r.Strength != "locked") || !line(r.Source.Reference, 512, true) || !line(r.Source.Revision, 128, false) || !line(r.Source.Identity, 96, false) {
			return fail(400, "invalid_rule", "unique stable identity, bounded text/why/details/source and normal|locked strength required")
		}
		// A locked safety floor must not disappear offline or depend on a selector.
		if !validTLDR(r.TLDR) {
			return fail(400, "invalid_tldr", fmt.Sprintf("an explanation needs English text; each language is one line of at most %d UTF-8 bytes", MaxTLDRBytes))
		}
		if r.Strength == "locked" && (!r.Enabled || r.ExpiresAt != nil) {
			return fail(400, "invalid_rule", "locked rules must be enabled and non-expiring; change them through a new higher-authority publication")
		}
		for _, v := range r.Roles {
			if !slices.Contains(Roles, v) {
				return fail(400, "invalid_rule", "unknown role")
			}
		}
		for _, v := range r.Harnesses {
			if !slices.Contains(Harnesses, v) {
				return fail(400, "invalid_rule", "unknown harness")
			}
		}
		if len(r.Roles) > len(Roles) || len(r.Harnesses) > len(Harnesses) {
			return fail(400, "invalid_rule", "too many selectors")
		}
		seen[r.Identity] = true
	}
	return nil
}
func validateName(s string) error {
	if !line(s, 128, true) {
		return fail(400, "invalid_rule", "set name must be one line, 1..128 UTF-8 bytes")
	}
	return nil
}

const maxPublishNote = 500

// normalizeNote trims an optional publish note. Empty stays empty so it is
// omitted from the snapshot and does not change an existing digest.
func normalizeNote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !utf8.ValidString(s) || len(s) > maxPublishNote {
		return "", fail(400, "invalid_rule", "publish note must be at most 500 UTF-8 bytes")
	}
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", fail(400, "invalid_rule", "publish note must be plain text")
		}
	}
	return s, nil
}
func (s Scope) rank() int {
	switch s.Layer {
	case "company":
		return 0
	case "project":
		return 1
	case "person":
		return 2
	default:
		if s.Role != "" {
			return 3
		}
		if s.TaskID == "" {
			return 4
		}
		return 5
	}
}
func (s Scope) matches(c Context) bool {
	return (s.ProjectID == "" || s.ProjectID == c.ProjectID) && (s.OwnerID == "" || s.OwnerID == c.PersonID) && (s.AgentID == "" || s.AgentID == c.AgentID) && (s.Role == "" || s.Role == c.Role) && (s.TaskID == "" || s.TaskID == c.TaskID)
}

// SessionHeader is the prefix Merge writes on every session file.
const SessionHeader = "# Aeon session rules\n\n"

func ruleLine(r Rule) string { return fmt.Sprintf("- [%s] %s\n", r.Identity, r.Text) }

// RenderedBody is the session file Merge writes for these rules: the header
// plus one identity line per enabled rule, sorted by identity. Details are
// omitted. Disabled rules are omitted. An empty enabled set renders nothing.
func RenderedBody(rules []Rule) string {
	enabled := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	if len(enabled) == 0 {
		return ""
	}
	slices.SortFunc(enabled, func(a, b Rule) int { return strings.Compare(a.Identity, b.Identity) })
	var body strings.Builder
	body.WriteString(SessionHeader)
	for _, r := range enabled {
		body.WriteString(ruleLine(r))
	}
	return body.String()
}

// UnmarshalJSON requires an explicit on/off decision. An omitted enabled field
// must never silently become an off rule that suppresses a lower identity.
func (r *Rule) UnmarshalJSON(raw []byte) error {
	type alias Rule
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"identity", "text", "why", "strength", "enabled", "source"} {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return fmt.Errorf("rule requires %s", key)
		}
	}
	var value alias
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	*r = Rule(value)
	return nil
}
