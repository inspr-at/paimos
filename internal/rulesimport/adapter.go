// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/client"
)

// These wire types map the frozen AR1 contract (AEON-248/249, ar1-api.json).
// Kept local so this isolated branch compiles before AR1 is integrated.
type DraftScope struct {
	Layer     Layer  `json:"layer"`
	ProjectID string `json:"project_id,omitempty"`
	OwnerID   string `json:"owner_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	Role      string `json:"role,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
}
type DraftSource struct {
	Reference  string `json:"reference"`
	Revision   string `json:"revision,omitempty"`
	Identity   string `json:"identity,omitempty"`
	EditedHere bool   `json:"edited_here"`
}
type DraftRule struct {
	Identity  string      `json:"identity"`
	Text      string      `json:"text"`
	Why       string      `json:"why"`
	Details   string      `json:"details,omitempty"`
	Strength  string      `json:"strength"`
	Enabled   bool        `json:"enabled"`
	ExpiresAt *time.Time  `json:"expires_at,omitempty"`
	Roles     []string    `json:"roles,omitempty"`
	Harnesses []string    `json:"harnesses,omitempty"`
	Source    DraftSource `json:"source"`
}
type DraftSet struct {
	ID               string      `json:"id"`
	LayerID          string      `json:"layer_id"`
	Scope            DraftScope  `json:"scope"`
	Name             string      `json:"name"`
	Revision         int64       `json:"revision"`
	Rules            []DraftRule `json:"rules"`
	PublishedVersion string      `json:"published_version"`
}
type DraftBody struct {
	ExpectedRevision int64       `json:"expected_revision"`
	Name             string      `json:"name"`
	Rules            []DraftRule `json:"rules"`
}

// Target names an existing set and the revision explicitly reviewed by the
// caller. It grants no rights; the server checks rules.write and scope ownership.
type Target struct {
	SetID    string
	Revision int64
}
type DraftResult struct {
	Mode      string `json:"mode"`
	PlanID    string `json:"plan_id"`
	SetID     string `json:"set_id"`
	Revision  int64  `json:"revision"`
	Added     int    `json:"added"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
}

// Lineage is appended as JSON in details, the only AR1 on-demand field capable
// of retaining every source range. No content is shortened to fit wire limits.
type draftLineage struct {
	Schema        string      `json:"schema"`
	Identity      string      `json:"identity"`
	ExplicitID    string      `json:"explicit_id,omitempty"`
	Layer         Layer       `json:"layer"`
	Set           string      `json:"set"`
	SetTitle      string      `json:"set_title"`
	Placement     string      `json:"placement"`
	Source        string      `json:"source,omitempty"`
	Coordinates   string      `json:"coordinates"`
	Sources       []SourceRef `json:"sources"`
	ContentSHA256 string      `json:"content_sha256"`
}

func digest(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }

func importedIdentity(identity string) string { return "import-" + digest([]byte(identity)) }

// MapDraft is pure: one local group maps to one AR1 draft. AR1 cannot encode
// unresolved choices or mixed groups. On-demand packs are attached as draft
// details; their bodies are not session-file text. Refusal retains the complete
// Proposal. Apply never selects an alternative or invents a rationale.
func MapDraft(p Proposal) ([]DraftRule, error) {
	if !validContext(p.Context) || len(p.Rules) == 0 || len(p.Rules) > 100 {
		return nil, draftRefusal("one explicit context and 1..100 rules are required")
	}
	if len(p.Contradictions) != 0 || len(p.Unresolved) != 0 {
		return nil, draftRefusal("resolve contradictions and unresolved choices in the local proposal first")
	}
	if !p.AlwaysOn.Insert {
		return nil, draftRefusal("always-on byte budget exceeded; proposal is untrimmed")
	}
	first := p.Rules[0]
	var out []DraftRule
	seen := map[string]bool{}
	for _, r := range p.Rules {
		if r.Layer != first.Layer || r.Set != first.Set {
			return nil, draftRefusal("AR1 replaces one set at a time; split this proposal into explicit layer/set groups")
		}
		if r.Identity == "" || seen[r.Identity] {
			return nil, draftRefusal("rule identities must be unique")
		}
		seen[r.Identity] = true
		if len(r.Sources) == 0 {
			return nil, draftRefusal("source lineage is required")
		}
		// A date has no instant/timezone. Require an explicit RFC3339 source value,
		// rather than silently inventing midnight or changing the rule's lifetime.
		var expires *time.Time
		if r.Expires != "" {
			at, err := time.Parse(time.RFC3339, r.Expires)
			if err != nil {
				return nil, draftRefusal("date-only expiry needs an explicit RFC3339 instant in the source")
			}
			expires = &at
		}
		id := importedIdentity(r.Identity)
		reference := r.Source
		if reference == "" {
			reference = "doctrine:" + id
		}
		sourceID := r.ExplicitID
		if sourceID == "" {
			sourceID = id
		}
		rule := DraftRule{
			Identity: id, Text: r.Text, Why: r.Why, Details: r.Details,
			Strength: r.Strength, Enabled: r.Enabled, ExpiresAt: expires,
			Roles: slices.Clone(r.Roles), Harnesses: slices.Clone(r.Harnesses),
			Source: DraftSource{Reference: reference, Identity: sourceID, EditedHere: false},
		}
		lineage, err := json.Marshal(draftLineage{
			Schema: "aeon.doctrine-lineage.v1", Identity: r.Identity, ExplicitID: r.ExplicitID,
			Layer: r.Layer, Set: r.Set, SetTitle: r.SetTitle, Placement: r.Placement, Source: r.Source,
			Coordinates:   "1-based normalized lines: BOM removed, CR/CRLF to LF; file_sha256 hashes raw bytes",
			Sources:       r.Sources,
			ContentSHA256: contentFingerprint(rule),
		})
		if err != nil {
			return nil, draftRefusal("cannot encode source lineage")
		}
		revision := r.Sources[0].FileSHA256
		if len(r.Sources) > 1 {
			revision = digest(lineage)
		}
		rule.Source.Revision = revision
		rule.Details = r.Details + "\n\n[aeon doctrine lineage]\n" + string(lineage)
		if err := validateDraftRule(rule); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}

func draftRefusal(reason string) error { return fmt.Errorf("%w: %s", ErrDraftUnavailable, reason) }

var draftIdentity = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,95}$`)
var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func boundedLine(s string, max int, required bool) bool {
	if !utf8.ValidString(s) || len(s) > max || (required && strings.TrimSpace(s) == "") {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' })
}
func validateDraftRule(r DraftRule) error {
	if !draftIdentity.MatchString(r.Identity) || !boundedLine(r.Text, 512, true) || !boundedLine(r.Why, 1024, true) {
		return draftRefusal("AR1 requires a stable slug, text (1..512 bytes) and explicit why (1..1024 bytes), each on one line")
	}
	if !utf8.ValidString(r.Details) || len(r.Details) > 16384 || strings.ContainsRune(r.Details, 0) {
		return draftRefusal("details plus full lineage exceed AR1 bounds or contain invalid text")
	}
	if (r.Strength != StrengthNormal && r.Strength != StrengthLocked) || (r.Strength == StrengthLocked && (!r.Enabled || r.ExpiresAt != nil)) {
		return draftRefusal("AR1 locked rules must remain enabled and non-expiring")
	}
	if !boundedLine(r.Source.Reference, 512, true) || !boundedLine(r.Source.Revision, 128, false) || !boundedLine(r.Source.Identity, 96, false) {
		return draftRefusal("source exceeds AR1 bounds")
	}
	for _, role := range r.Roles {
		if !slices.Contains([]string{"coordinator", "builder", "reviewer", "operator"}, role) {
			return draftRefusal("unknown AR1 role; choose an explicit supported role in the source")
		}
	}
	for _, harness := range r.Harnesses {
		if !slices.Contains([]string{"claude-code", "codex", "grok", "pi", "cursor"}, harness) {
			return draftRefusal("unknown AR1 harness; choose an explicit supported harness in the source")
		}
	}
	if len(r.Roles) > 4 || len(r.Harnesses) > 5 {
		return draftRefusal("too many AR1 selectors")
	}
	return nil
}

func validateTarget(target Target) error {
	if !canonicalUUID.MatchString(target.SetID) || target.Revision < 1 {
		return draftRefusal("apply requires an existing set UUID and explicit positive expected revision")
	}
	return nil
}

// ApplyDraft uses the caller's configured client. Only GET set + PUT draft are
// possible; no creation, publish, restore, retry, privilege fallback or redirect.
func ApplyDraft(ctx context.Context, c *client.Client, p Proposal, target Target) (DraftResult, error) {
	imported, err := MapDraft(p)
	if err != nil {
		return DraftResult{}, err
	}
	if err := validateTarget(target); err != nil {
		return DraftResult{}, err
	}
	if c == nil {
		return DraftResult{}, draftRefusal("configured API client required")
	}
	// Keep the configured transport/auth; prohibit redirects to other operations.
	api := *c
	hc := *http.DefaultClient
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	api.HTTP = &hc
	path := "/api/rules/sets/" + target.SetID
	var current DraftSet
	if err := api.Do(ctx, http.MethodGet, path, nil, &current); err != nil {
		return DraftResult{}, safeDraftError(err)
	}
	if current.ID != target.SetID || current.Scope.Layer != p.Rules[0].Layer || !boundedLine(current.Name, 128, true) {
		return DraftResult{}, draftRefusal("target set does not match the proposed layer or AR1 contract")
	}
	if current.Revision != target.Revision {
		return DraftResult{}, ErrDraftConflict
	}
	merged := slices.Clone(current.Rules)
	index := map[string]int{}
	for i, r := range merged {
		if err := validateDraftRule(r); err != nil {
			return DraftResult{}, draftRefusal("existing draft is outside the frozen AR1 contract; refusing lossy replacement")
		}
		if _, ok := index[r.Identity]; ok {
			return DraftResult{}, ErrDraftConflict
		}
		index[r.Identity] = i
	}
	merged, added, updated, unchanged, err := mergeImported(merged, imported)
	if err != nil {
		return DraftResult{}, err
	}
	result := DraftResult{Mode: "unchanged", PlanID: p.PlanID, SetID: current.ID, Revision: current.Revision, Added: added, Updated: updated, Unchanged: unchanged}
	if added == 0 && updated == 0 {
		return result, nil
	}
	if len(merged) > 100 {
		return DraftResult{}, draftRefusal("combined draft exceeds 100 rules; existing rules were retained")
	}
	body := DraftBody{ExpectedRevision: target.Revision, Name: current.Name, Rules: merged}
	var saved DraftSet
	if err := api.Do(ctx, http.MethodPut, path+"/draft", body, &saved); err != nil {
		return DraftResult{}, safeDraftError(err)
	}
	if saved.ID != current.ID || saved.Revision != current.Revision+1 || saved.Scope != current.Scope || saved.Name != current.Name || saved.PublishedVersion != current.PublishedVersion || !sameDraftRules(saved.Rules, merged) {
		return DraftResult{}, draftRefusal("unexpected draft response; write outcome uncertain, inspect the set before retrying")
	}
	result.Mode, result.Revision = "draft", saved.Revision
	return result, nil
}

func sameDraftRule(a, b DraftRule) bool {
	a.Roles, b.Roles = uniqueSorted(a.Roles), uniqueSorted(b.Roles)
	a.Harnesses, b.Harnesses = uniqueSorted(a.Harnesses), uniqueSorted(b.Harnesses)
	if a.ExpiresAt != nil {
		at := a.ExpiresAt.UTC()
		a.ExpiresAt = &at
	}
	if b.ExpiresAt != nil {
		at := b.ExpiresAt.UTC()
		b.ExpiresAt = &at
	}
	return reflect.DeepEqual(a, b)
}
func sameDraftRules(a, b []DraftRule) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[string]DraftRule, len(a))
	for _, r := range a {
		if _, found := byID[r.Identity]; found {
			return false
		}
		byID[r.Identity] = r
	}
	for _, r := range b {
		prior, found := byID[r.Identity]
		if !found || !sameDraftRule(prior, r) {
			return false
		}
	}
	return true
}
func safeDraftError(err error) error {
	var status *client.StatusError
	if errors.As(err, &status) {
		// Do not echo a server body that could contain submitted private doctrine.
		return &client.StatusError{Status: status.Status, Message: "draft request refused; local proposal retained, no automatic retry"}
	}
	return draftRefusal("API request failed; local proposal retained, inspect the set before retrying")
}
