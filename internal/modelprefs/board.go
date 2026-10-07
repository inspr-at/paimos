// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// BoardProfile is sparse: null personal values inherit the workspace.
type BoardProfile struct {
	ID             string   `json:"-"`
	Scope          string   `json:"scope"`
	PersonID       *string  `json:"person_id"`
	Template       *string  `json:"template"`
	Thinking       *string  `json:"thinking"`
	Usage          *string  `json:"usage"`
	Residency      *string  `json:"residency"`
	HiddenKinds    []string `json:"hidden_kinds"`
	DismissedLines []string `json:"dismissed_lines"`
	Revision       int64    `json:"revision"`
}
type BoardOrder struct {
	ProfileID string   `json:"profile_id"`
	Column    string   `json:"column"`
	Situation string   `json:"situation"`
	Rank      []string `json:"rank"`
	Not       []string `json:"not"`
	Thinking  *string  `json:"thinking"`
}
type Rule struct {
	Scope     string    `json:"scope"`
	ProjectID *string   `json:"project_id"`
	Column    string    `json:"column"`
	Line      string    `json:"line"`
	Lock      string    `json:"lock"`
	Position  int       `json:"position"`
	Why       string    `json:"why"`
	SetBy     *string   `json:"set_by"`
	SetAt     time.Time `json:"set_at"`
}
type BoardLock struct {
	Kind  string     `json:"kind"`
	Value string     `json:"value"`
	Who   *string    `json:"who"`
	Why   string     `json:"why"`
	At    *time.Time `json:"at"`
	Scope string     `json:"scope"`
}

func (r Rule) BoardLock() *BoardLock {
	return &BoardLock{Kind: "rule", Value: r.Lock, Who: r.SetBy, Why: r.Why, At: &r.SetAt, Scope: r.Scope}
}

type BoardLine struct {
	ID     string
	Family string
	Tools  bool
	Review bool
}
type BoardState struct {
	Workspace  BoardProfile
	Person     *BoardProfile
	Orders     []BoardOrder
	Rules      []Rule
	Kinds      []Kind
	Lines      []BoardLine
	SmallHours int
	FixRounds  int
}
type BoardQuery struct {
	Column         string
	Area           string
	Labels         []string
	Situation      string
	AuthorFamily   string
	PreviousFamily string
	Review         bool
	Concept        bool
	EstimateHours  float64
	FixRound       int
}
type HeldLine struct {
	Line   string `json:"line"`
	Reason string `json:"reason"`
}
type BoardDecision struct {
	PreferenceOf struct {
		Person *string `json:"person"`
		Source string  `json:"source"`
	} `json:"preference_of"`
	Column         string                `json:"column"`
	Situation      string                `json:"situation"`
	CardIndex      int                   `json:"card_index,omitempty"`
	Lock           *BoardLock            `json:"lock,omitempty"`
	Held           []HeldLine            `json:"held"`
	Rank           []string              `json:"-"`
	Top            []string              `json:"-"`
	Bottom         []string              `json:"-"`
	Not            []string              `json:"-"`
	Cant           []HeldLine            `json:"-"`
	Locks          map[string]*BoardLock `json:"-"`
	Thinking       string                `json:"-"`
	EffortLevel    int                   `json:"-"`
	ThinkingSource string                `json:"-"`
	Source         string                `json:"-"`
	Selected       string                `json:"-"`
}

// Templates name a closed set of lines. Catalog discoveries never join implicitly.
func TemplateRank(template, column string) []string {
	build := []string{"openai:sol", "anthropic:sonnet", "anthropic:opus", "openai:astra", "anthropic:fable"}
	design := []string{"anthropic:opus", "anthropic:sonnet", "openai:sol", "anthropic:fable", "openai:astra"}
	review := []string{"openai:sol", "xai:grok", "anthropic:opus", "anthropic:fable"}
	concept := []string{"anthropic:opus", "anthropic:fable", "openai:astra", "anthropic:sonnet", "openai:sol", "xai:grok"}
	if template == "best" {
		build = []string{"openai:astra", "anthropic:opus", "openai:sol", "anthropic:sonnet", "anthropic:fable"}
		design = []string{"anthropic:opus", "anthropic:fable", "anthropic:sonnet", "openai:sol", "openai:astra"}
		concept = []string{"anthropic:opus", "openai:astra", "anthropic:fable", "anthropic:sonnet", "openai:sol", "xai:grok"}
	}
	if template == "save" {
		build = []string{"anthropic:sonnet", "openai:sol", "anthropic:opus", "openai:astra", "anthropic:fable"}
		design = []string{"anthropic:sonnet", "anthropic:opus", "openai:sol", "anthropic:fable", "openai:astra"}
		review = []string{"openai:sol", "xai:grok", "anthropic:fable", "anthropic:opus"}
		concept = []string{"anthropic:sonnet", "anthropic:opus", "openai:astra", "anthropic:fable", "openai:sol", "xai:grok"}
	}
	switch {
	case column == "design":
		return design
	case strings.HasPrefix(column, "review:"):
		return review
	case column == "concept":
		return concept
	default:
		return build
	}
}
func ThinkingLevel(word string) int {
	switch word {
	case "lean":
		return 2
	case "deep":
		return 4
	case "max":
		return 5
	default:
		return 3
	}
}
func ThinkingWord(level int) string {
	switch {
	case level <= 2:
		return "lean"
	case level == 4:
		return "deep"
	case level >= 5:
		return "max"
	default:
		return "standard"
	}
}

// NearestEffort chooses a registered level and resolves equidistant ties upward.
func NearestEffort(level int, registered []int) int {
	best, dist := -1, 100
	for _, v := range registered {
		d := v - level
		if d < 0 {
			d = -d
		}
		if d < dist || d == dist && v > best {
			best, dist = v, d
		}
	}
	return best
}
func boardOrder(s BoardState, p BoardProfile, column, situation string) (BoardOrder, bool) {
	for _, o := range s.Orders {
		if o.ProfileID == p.ID && o.Column == column && o.Situation == situation {
			return o, true
		}
	}
	return BoardOrder{}, false
}
func profileOrder(s BoardState, p BoardProfile, column, situation string) (BoardOrder, string, bool) {
	keys := [][2]string{{column, situation}}
	if situation != "first" {
		keys = append(keys, [2]string{column, "first"})
	}
	if column != "other" && !strings.HasPrefix(column, "review:") && column != "concept" {
		keys = append(keys, [2]string{"other", situation})
		if situation != "first" {
			keys = append(keys, [2]string{"other", "first"})
		}
	}
	for _, key := range keys {
		if o, ok := boardOrder(s, p, key[0], key[1]); ok && o.Rank != nil {
			source := "own"
			if key[0] != column || key[1] != situation {
				source = "follows"
			}
			return o, source, true
		}
	}
	return BoardOrder{}, "", false
}

// ResolveBoard implements the six steps for both dispatch and board reads.
// available checks the current account/harness policy, without mutating it.
func ResolveBoard(s BoardState, q BoardQuery, available func(string, int) (bool, string)) BoardDecision {
	d := BoardDecision{Held: []HeldLine{}, Rank: []string{}, Top: []string{}, Bottom: []string{}, Not: []string{}, Cant: []HeldLine{}, Locks: map[string]*BoardLock{}, Source: "template", ThinkingSource: "default", Thinking: "standard"}
	d.PreferenceOf.Source = "default"
	if s.Person != nil {
		d.PreferenceOf.Person = s.Person.PersonID
		d.PreferenceOf.Source = "person"
	}
	d.Column = q.Column
	if d.Column == "" {
		d.Column = "other"
		for _, k := range s.Kinds {
			if k.Slug == q.Area || slices.Contains(k.Labels, q.Area) {
				d.Column = k.Slug
				break
			}
			for _, label := range q.Labels {
				if slices.Contains(k.Labels, label) {
					d.Column = k.Slug
					break
				}
			}
			if d.Column != "other" {
				break
			}
		}
	}
	if q.Review {
		d.Column = "review:" + q.AuthorFamily
	}
	if q.Concept {
		d.Column = "concept"
	}
	d.Situation = q.Situation
	if d.Situation == "" {
		d.Situation = "first"
		if q.FixRound > 0 {
			d.Situation = "fix"
		}
		limit := s.FixRounds
		if limit == 0 {
			limit = 3
		}
		if q.FixRound > limit {
			d.Situation = "stuck"
		}
	}
	chosen := s.Workspace
	order, source, found := BoardOrder{}, "", false
	if s.Person != nil {
		chosen = *s.Person
		order, source, found = profileOrder(s, chosen, d.Column, d.Situation)
	}
	if !found && chosen.Template == nil {
		chosen = s.Workspace
		order, source, found = profileOrder(s, chosen, d.Column, d.Situation)
		if source == "own" {
			source = "default"
		}
	}
	if found {
		d.Rank = slices.Clone(order.Rank)
		d.Source = source
	} else {
		template := "balanced"
		if chosen.Template != nil {
			template = *chosen.Template
		}
		d.Rank = TemplateRank(template, d.Column)
	}
	// An order containing only thinking still inherits its rank.
	if s.Workspace.Thinking != nil {
		d.Thinking = *s.Workspace.Thinking
	}
	if s.Person != nil && s.Person.Thinking != nil {
		d.Thinking = *s.Person.Thinking
		d.ThinkingSource = "own"
	}
	profiles := []BoardProfile{s.Workspace}
	if s.Person != nil {
		profiles = append(profiles, *s.Person)
	}
	for _, p := range profiles {
		o, ok := boardOrder(s, p, d.Column, d.Situation)
		if !ok && d.Situation != "first" {
			o, ok = boardOrder(s, p, d.Column, "first")
		}
		if ok && o.Thinking != nil {
			d.Thinking = *o.Thinking
			d.ThinkingSource = "default"
			if p.Scope == "person" {
				d.ThinkingSource = "own"
			}
		}
		if o, _, ok := profileOrder(s, p, d.Column, d.Situation); ok {
			for _, id := range o.Not {
				if !slices.Contains(d.Not, id) {
					d.Not = append(d.Not, id)
				}
			}
		}
	}
	level := ThinkingLevel(d.Thinking)
	explicitSituation := found && order.Situation == d.Situation
	if d.Situation == "fix" && !explicitSituation {
		level--
	}
	small := s.SmallHours
	if small == 0 {
		small = 2
	}
	if d.Situation == "first" && q.EstimateHours > 0 && q.EstimateHours <= float64(small) {
		level--
	}
	if d.Situation == "stuck" {
		if level < 4 {
			level = 4
		}
		if !explicitSituation && q.PreviousFamily != "" {
			sort.SliceStable(d.Rank, func(i, j int) bool {
				family := func(id string) string { f, _, _ := strings.Cut(id, ":"); return f }
				return family(d.Rank[i]) != q.PreviousFamily && family(d.Rank[j]) == q.PreviousFamily
			})
		}
	}
	if strings.HasPrefix(d.Column, "review:") {
		level = 4
	}
	d.Thinking = ThinkingWord(level)
	d.EffortLevel = level
	rules := slices.Clone(s.Rules)
	sort.SliceStable(rules, func(i, j int) bool {
		a, b := rules[i], rules[j]
		if a.Scope != b.Scope {
			return a.Scope == "project"
		}
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return a.Line < b.Line
	})
	for _, r := range rules {
		if r.Column != d.Column {
			continue
		}
		if r.Lock == "not" {
			if !slices.Contains(d.Not, r.Line) {
				d.Not = append(d.Not, r.Line)
			}
			d.Locks[r.Line] = r.BoardLock()
		}
	}
	for _, r := range rules {
		if r.Column != d.Column || slices.Contains(d.Not, r.Line) {
			continue
		}
		if _, ok := d.Locks[r.Line]; ok {
			continue
		}
		d.Locks[r.Line] = r.BoardLock()
		if r.Lock == "top" {
			d.Top = append(d.Top, r.Line)
		} else if r.Lock == "bottom" {
			d.Bottom = append(d.Bottom, r.Line)
		}
	}
	rank := append(slices.Clone(d.Top), d.Rank...)
	rank = append(rank, d.Bottom...)
	filtered := []string{}
	seen := map[string]bool{}
	for _, id := range rank {
		if seen[id] {
			continue
		}
		seen[id] = true
		if slices.Contains(d.Not, id) {
			continue
		}
		if slices.Contains(d.Bottom, id) {
			continue
		}
		filtered = append(filtered, id)
	}
	filtered = append(filtered, d.Bottom...)
	lines := map[string]BoardLine{}
	for _, l := range s.Lines {
		lines[l.ID] = l
	}
	d.Rank = []string{}
	for _, id := range filtered {
		l, ok := lines[id]
		if !ok {
			d.Held = append(d.Held, HeldLine{id, "line is not in the catalog"})
			continue
		}
		if strings.HasPrefix(d.Column, "review:") && l.Family == strings.TrimPrefix(d.Column, "review:") {
			d.Not = append(d.Not, id)
			d.Locks[id] = &BoardLock{Kind: "cross_family", Value: "not", Why: "A model never reviews its own family", Scope: "workspace"}
			continue
		}
		if (!l.Tools && d.Column != "concept" && !strings.HasPrefix(d.Column, "review:")) || strings.HasPrefix(d.Column, "review:") && !l.Review {
			reason := "No tools in PAIMOS"
			if strings.HasPrefix(d.Column, "review:") {
				reason = "Review requires a qualified strong or frontier model at xhigh"
			}
			d.Cant = append(d.Cant, HeldLine{id, reason})
			continue
		}
		d.Rank = append(d.Rank, id)
	}
	// The same-family rule is visible even when a template omitted that line.
	if strings.HasPrefix(d.Column, "review:") {
		for _, l := range s.Lines {
			if l.Family == strings.TrimPrefix(d.Column, "review:") && !slices.Contains(d.Not, l.ID) {
				d.Not = append(d.Not, l.ID)
				d.Locks[l.ID] = &BoardLock{Kind: "cross_family", Value: "not", Why: "A model never reviews its own family", Scope: "workspace"}
			}
		}
	}
	for i, id := range d.Rank {
		if available == nil {
			break
		}
		ok, reason := available(id, level)
		if ok {
			d.Selected = id
			d.CardIndex = i + 1
			d.Lock = d.Locks[id]
			break
		}
		d.Held = append(d.Held, HeldLine{id, reason})
	}
	return d
}
