// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// Build reads the requested files and returns a local proposal.
// It never publishes, and it never walks directories.
func Build(ctx context.Context, in Request) (Proposal, error) {
	if err := ctx.Err(); err != nil {
		return Proposal{}, err
	}
	section := in.Section
	if section == "" {
		section = SectionAll
	}
	if section != SectionAll && section != SectionPersonal && section != SectionKernel {
		return Proposal{}, fmt.Errorf("unknown section %q", section)
	}
	if !validContext(in.Context) {
		return Proposal{}, fmt.Errorf("unknown trust context %q", in.Context)
	}
	if len(in.Files) == 0 {
		return Proposal{}, fmt.Errorf("at least one explicit file is required")
	}
	overrides, err := layerOverrides(in.Layers)
	if err != nil {
		return Proposal{}, err
	}
	seenPath := map[string]bool{}
	var files []SourceFile
	var raws []built
	var unresolved []Unresolved
	for _, path := range in.Files {
		if err := ctx.Err(); err != nil {
			return Proposal{}, err
		}
		clean, err := cleanPath(path)
		if err != nil {
			return Proposal{}, err
		}
		if seenPath[clean] {
			return Proposal{}, fmt.Errorf("duplicate path %s", filepathBase(clean))
		}
		seenPath[clean] = true
		body, sum, size, err := readDoctrine(clean)
		if err != nil {
			return Proposal{}, err
		}
		file, err := classify(clean, section, size)
		if err != nil {
			return Proposal{}, err
		}
		file.SHA256 = sum
		if err := classifyContent(&file, body, section, in.Context); err != nil {
			return Proposal{}, err
		}
		if !contextAllowed(in.Context, file.Trust) {
			return Proposal{}, fmt.Errorf("%w: %s is %s, plan context is %s", ErrMixedContext, file.Base, file.Trust, in.Context)
		}
		if layer, ok := overrides[clean]; ok {
			file.Layer = layer
			delete(overrides, clean)
		}
		parsed, err := parseDocument(file, body, section)
		if err != nil {
			return Proposal{}, err
		}
		files = append(files, file)
		unresolved = append(unresolved, parsed.unresolved...)
		for _, rule := range parsed.rules {
			raws = append(raws, built{file: file, rule: rule, lines: parsed.lines})
		}
	}
	if len(overrides) > 0 {
		return Proposal{}, fmt.Errorf("layer mapping does not match an explicit file")
	}
	proposal := assemble(in.Context, section, files, raws, unresolved)
	proposal.PlanID = planID(proposal)
	proposal.Adapter = AdapterReport{Ready: true, Reason: "AR1 draft mapping available; API authorization and explicit set/revision required"}
	if _, err := MapDraft(proposal); err != nil {
		proposal.Adapter = AdapterReport{Reason: err.Error()}
	}
	return proposal, nil
}

func layerOverrides(in map[string]Layer) (map[string]Layer, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]Layer, len(in))
	for raw, layer := range in {
		switch layer {
		case LayerCompany, LayerProject, LayerPerson, LayerAgent:
		default:
			return nil, fmt.Errorf("unknown layer %q", layer)
		}
		clean, err := cleanPath(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := out[clean]; ok {
			return nil, fmt.Errorf("duplicate layer mapping")
		}
		out[clean] = layer
	}
	return out, nil
}

func validContext(c TrustContext) bool {
	switch c {
	case ContextTemplate, ContextPrivate, ContextProject, ContextPerson:
		return true
	default:
		return false
	}
}

func filepathBase(path string) string {
	slash := strings.ReplaceAll(path, "\\", "/")
	if i := strings.LastIndex(slash, "/"); i >= 0 {
		return slash[i+1:]
	}
	return slash
}

type built struct {
	file  SourceFile
	rule  rawRule
	lines []string
}

func assemble(plan TrustContext, section string, files []SourceFile, raws []built, unresolved []Unresolved) Proposal {
	slices.SortFunc(files, func(a, b SourceFile) int {
		return strings.Compare(a.Path, b.Path)
	})
	type fpGroup struct {
		rule Rule
	}
	type identityGroup struct {
		fps  []string
		byFP map[string]*fpGroup
	}
	groups := map[string]*identityGroup{}
	var order []string
	for _, item := range raws {
		rule := toRule(item)
		ig := groups[rule.Identity]
		if ig == nil {
			ig = &identityGroup{byFP: map[string]*fpGroup{}}
			groups[rule.Identity] = ig
			order = append(order, rule.Identity)
		}
		fp := fingerprint(rule)
		existing := ig.byFP[fp]
		if existing == nil {
			rule.Sources = []SourceRef{rule.Sources[0]}
			ig.byFP[fp] = &fpGroup{rule: rule}
			ig.fps = append(ig.fps, fp)
			continue
		}
		existing.rule.Sources = append(existing.rule.Sources, rule.Sources[0])
	}
	var rules []Rule
	var contradictions []Contradiction
	var duplicates []Duplicate
	for _, identity := range order {
		ig := groups[identity]
		if len(ig.fps) > 1 {
			var ids []string
			fields := map[string]bool{}
			var sample []Rule
			for _, fp := range ig.fps {
				sample = append(sample, ig.byFP[fp].rule)
				ids = append(ids, ig.byFP[fp].rule.ID)
			}
			for i := 0; i < len(sample); i++ {
				for j := i + 1; j < len(sample); j++ {
					for _, field := range differingFields(sample[i], sample[j]) {
						fields[field] = true
					}
				}
			}
			slices.Sort(ids)
			contradictions = append(contradictions, Contradiction{
				Identity: identity,
				Kind:     "same_identity_difference",
				RuleIDs:  ids,
				Fields:   mapKeys(fields),
				Note:     "same identity differs; both rules are retained and layer precedence is not applied",
			})
		}
		for _, fp := range ig.fps {
			rule := ig.byFP[fp].rule
			slices.SortFunc(rule.Sources, func(a, b SourceRef) int {
				if c := strings.Compare(a.Path, b.Path); c != 0 {
					return c
				}
				if a.StartLine != b.StartLine {
					return a.StartLine - b.StartLine
				}
				return a.EndLine - b.EndLine
			})
			if len(rule.Sources) > 1 {
				duplicates = append(duplicates, Duplicate{
					Identity: rule.Identity,
					RuleID:   rule.ID,
					Sources:  len(rule.Sources),
					Note:     "identical content replayed; every source lineage is retained",
				})
			}
			rules = append(rules, rule)
		}
	}
	contradictions = append(contradictions, crossLayerContradictions(rules)...)
	slices.SortFunc(rules, func(a, b Rule) int {
		if layerRank(a.Layer) != layerRank(b.Layer) {
			return layerRank(a.Layer) - layerRank(b.Layer)
		}
		if c := strings.Compare(a.Set, b.Set); c != 0 {
			return c
		}
		if c := strings.Compare(a.Identity, b.Identity); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(contradictions, func(a, b Contradiction) int {
		return strings.Compare(a.Identity, b.Identity)
	})
	slices.SortFunc(duplicates, func(a, b Duplicate) int {
		return strings.Compare(a.Identity, b.Identity)
	})
	heuristics := heuristicsFor(rules)
	slices.SortFunc(unresolved, func(a, b Unresolved) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	doc := alwaysOnDocument(rules)
	report := AlwaysOnReport{Bytes: len(doc), Budget: AlwaysOnBudget}
	if report.Bytes <= AlwaysOnBudget {
		report.Insert = true
		report.Explanation = fmt.Sprintf("always-on projection is %d bytes, within the %d byte budget", report.Bytes, AlwaysOnBudget)
	} else {
		report.Insert = false
		report.Explanation = fmt.Sprintf("always-on projection is %d bytes, budget is %d; rules were not inserted and were not truncated", report.Bytes, AlwaysOnBudget)
	}
	for i := range rules {
		rules[i].AlwaysOnEligible = rules[i].Placement == PlacementAlwaysOn && report.Insert
	}
	if rules == nil {
		rules = []Rule{}
	}
	if contradictions == nil {
		contradictions = []Contradiction{}
	}
	if duplicates == nil {
		duplicates = []Duplicate{}
	}
	if heuristics == nil {
		heuristics = []HeuristicMatch{}
	}
	if unresolved == nil {
		unresolved = []Unresolved{}
	}
	if files == nil {
		files = []SourceFile{}
	}
	return Proposal{
		Context:        plan,
		Section:        section,
		Mode:           "preview",
		Files:          files,
		Rules:          rules,
		Contradictions: contradictions,
		Duplicates:     duplicates,
		Heuristics:     heuristics,
		Unresolved:     unresolved,
		AlwaysOn:       report,
	}
}

func toRule(item built) Rule {
	raw := item.rule
	roles := uniqueSorted(raw.roles)
	harnesses := uniqueSorted(raw.harnesses)
	roleKey := "-"
	if len(roles) > 0 {
		roleKey = strings.Join(roles, "+")
	}
	part := raw.explicitID
	if part == "" {
		sum := sha256.Sum256([]byte(normalizeText(raw.text)))
		part = "t-" + hex.EncodeToString(sum[:10])
	}
	identity := strings.Join([]string{string(raw.layer), raw.setSlug, roleKey, part}, "/")
	strength := StrengthNormal
	if raw.locked {
		strength = StrengthLocked
	}
	rule := Rule{
		Identity:   identity,
		Layer:      raw.layer,
		Set:        raw.setSlug,
		SetTitle:   raw.setTitle,
		Text:       raw.text,
		Why:        raw.why,
		Details:    strings.Join(raw.details, "\n"),
		Strength:   strength,
		Enabled:    raw.enabled,
		Expires:    raw.expires,
		Source:     raw.source,
		Roles:      roles,
		Harnesses:  harnesses,
		Placement:  raw.placement,
		ExplicitID: raw.explicitID,
		Sources: []SourceRef{{
			Path:        item.file.Path,
			HeadingPath: raw.headingPath,
			StartLine:   raw.start,
			EndLine:     raw.end,
			SHA256:      hashLines(item.lines, raw.start, raw.end),
			FileSHA256:  item.file.SHA256,
		}},
	}
	rule.ID = ruleID(identity, fingerprint(rule))
	return rule
}

func ruleID(identity, fp string) string {
	sum := sha256.Sum256([]byte(identity + "\n" + fp))
	return "rule_" + hex.EncodeToString(sum[:8])
}

func fingerprint(rule Rule) string {
	h := sha256.New()
	fmt.Fprintf(h, "text=%s\nwhy=%s\ndetails=%s\nstrength=%s\nenabled=%t\nexpires=%s\nsource=%s\nroles=%s\nharnesses=%s\nplacement=%s\nlayer=%s\n",
		rule.Text, rule.Why, rule.Details, rule.Strength, rule.Enabled, rule.Expires, rule.Source,
		strings.Join(rule.Roles, ","), strings.Join(rule.Harnesses, ","), rule.Placement, rule.Layer)
	return hex.EncodeToString(h.Sum(nil))
}

func differingFields(a, b Rule) []string {
	var fields []string
	if a.Text != b.Text {
		fields = append(fields, "text")
	}
	if a.Why != b.Why {
		fields = append(fields, "why")
	}
	if a.Details != b.Details {
		fields = append(fields, "details")
	}
	if a.Strength != b.Strength {
		fields = append(fields, "strength")
	}
	if a.Enabled != b.Enabled {
		fields = append(fields, "enabled")
	}
	if a.Expires != b.Expires {
		fields = append(fields, "expires")
	}
	if a.Source != b.Source {
		fields = append(fields, "source")
	}
	if strings.Join(a.Roles, ",") != strings.Join(b.Roles, ",") {
		fields = append(fields, "roles")
	}
	if strings.Join(a.Harnesses, ",") != strings.Join(b.Harnesses, ",") {
		fields = append(fields, "harnesses")
	}
	slices.Sort(fields)
	return fields
}

// directivePhrases are matched leftmost, and a longer phrase wins a tie, so
// "must not" stays a prohibition rather than a requirement.
var directivePhrases = []struct {
	polarity string
	phrase   string
}{
	{"prohibit", "must not"},
	{"prohibit", "do not"},
	{"prohibit", "don't"},
	{"prohibit", "never"},
	{"prohibit", "forbidden"},
	{"require", "always"},
	{"require", "required"},
	{"require", "must"},
	{"permit", "allowed"},
	{"permit", "may"},
}

// crossLayerContradictions reports the same action with opposing directives
// (must/always versus never/must-not) in different layers. Tightening
// (may versus must) is not a conflict. Rules whose roles or harnesses cannot
// apply to the same audience are not a conflict. Heading paths are provenance,
// not part of the action key. Precedence is not applied; both rules stay.
func crossLayerContradictions(rules []Rule) []Contradiction {
	type member struct {
		rule     Rule
		polarity string
	}
	groups := map[string][]member{}
	var topics []string
	for _, rule := range rules {
		topic, polarity, ok := directiveTopic(rule)
		if !ok {
			continue
		}
		if _, exists := groups[topic]; !exists {
			topics = append(topics, topic)
		}
		groups[topic] = append(groups[topic], member{rule, polarity})
	}
	var out []Contradiction
	for _, topic := range topics {
		members := groups[topic]
		involved := map[string]Rule{}
		var order []string
		for i := range members {
			for j := i + 1; j < len(members); j++ {
				a, b := members[i], members[j]
				if a.rule.Layer == b.rule.Layer || !opposed(a.polarity, b.polarity) || !audiencesOverlap(a.rule, b.rule) {
					continue
				}
				for _, rule := range []Rule{a.rule, b.rule} {
					if _, seen := involved[rule.ID]; seen {
						continue
					}
					involved[rule.ID] = rule
					order = append(order, rule.ID)
				}
			}
		}
		if len(involved) == 0 {
			continue
		}
		ids := append([]string(nil), order...)
		slices.Sort(ids)
		layers := map[Layer]bool{}
		var layerOrder []Layer
		headings := map[string]bool{}
		var headingList []string
		for _, id := range order {
			rule := involved[id]
			if !layers[rule.Layer] {
				layers[rule.Layer] = true
				layerOrder = append(layerOrder, rule.Layer)
			}
			heading := rule.SetTitle
			if heading == "" && len(rule.Sources) > 0 {
				heading = rule.Sources[0].HeadingPath
			}
			if heading != "" && !headings[heading] {
				headings[heading] = true
				headingList = append(headingList, heading)
			}
		}
		slices.SortFunc(layerOrder, func(a, b Layer) int {
			if d := layerRank(a) - layerRank(b); d != 0 {
				return d
			}
			return strings.Compare(string(a), string(b))
		})
		names := make([]string, len(layerOrder))
		for i, layer := range layerOrder {
			names[i] = string(layer)
		}
		slices.Sort(headingList)
		out = append(out, Contradiction{
			Identity: topic,
			Kind:     "cross_layer_directive",
			RuleIDs:  ids,
			Fields:   []string{"directive"},
			Note:     "same action has conflicting directives across layers; headings are provenance and layer precedence is not applied",
			Topic:    topic,
			Layers:   names,
			Headings: headingList,
		})
	}
	return out
}

func opposed(a, b string) bool {
	return (a == "require" && b == "prohibit") || (a == "prohibit" && b == "require")
}

func audiencesOverlap(a, b Rule) bool {
	return selectorsOverlap(a.Roles, b.Roles) && selectorsOverlap(a.Harnesses, b.Harnesses)
}

func selectorsOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, item := range a {
		if slices.Contains(b, item) {
			return true
		}
	}
	return false
}

func directiveTopic(rule Rule) (topic, polarity string, ok bool) {
	text := directiveSurface(rule.Text)
	at, n, polarity := -1, 0, ""
	for _, phrase := range directivePhrases {
		i := strings.Index(text, phrase.phrase)
		if i < 0 || !phraseBounded(text, i, len(phrase.phrase)) {
			continue
		}
		if at < 0 || i < at || (i == at && len(phrase.phrase) > n) {
			at, n, polarity = i, len(phrase.phrase), phrase.polarity
		}
	}
	if at < 0 {
		return "", "", false
	}
	residue := collapseSpace(text[:at] + " " + text[at+n:])
	residue = strings.Trim(residue, ".,;:!?\"'`*-_")
	residue = collapseSpace(residue)
	if residue == "" {
		return "", "", false
	}
	return residue, polarity, true
}

func directiveSurface(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "🔴", "")
	s = strings.ReplaceAll(s, "🟡", "")
	return collapseSpace(stripEmphasis(s))
}

// stripEmphasis removes markdown emphasis markers and keeps code spans
// verbatim, aside from the backticks that delimit them. An underscore between
// identifier characters is part of the identifier, not emphasis.
func stripEmphasis(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inCode := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '`' {
			inCode = !inCode
			continue
		}
		if inCode {
			b.WriteByte(c)
			continue
		}
		if c == '*' || (c == '_' && !identifierUnderscore(s, i)) {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func identifierUnderscore(s string, i int) bool {
	return i > 0 && i+1 < len(s) && isTopicWord(s[i-1]) && isTopicWord(s[i+1])
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func phraseBounded(s string, at, n int) bool {
	if at > 0 && isTopicWord(s[at-1]) {
		return false
	}
	end := at + n
	if end < len(s) && isTopicWord(s[end]) {
		return false
	}
	return true
}

func isTopicWord(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func heuristicsFor(rules []Rule) []HeuristicMatch {
	type bucket struct {
		ids        []string
		identities map[string]struct{}
	}
	buckets := map[string]*bucket{}
	var keys []string
	for _, rule := range rules {
		key := normalizeText(rule.Text)
		if key == "" {
			continue
		}
		b := buckets[key]
		if b == nil {
			b = &bucket{identities: map[string]struct{}{}}
			buckets[key] = b
			keys = append(keys, key)
		}
		b.ids = append(b.ids, rule.ID)
		b.identities[rule.Identity] = struct{}{}
	}
	var out []HeuristicMatch
	for _, key := range keys {
		b := buckets[key]
		if len(b.identities) < 2 {
			continue
		}
		ids := append([]string(nil), b.ids...)
		slices.Sort(ids)
		out = append(out, HeuristicMatch{
			Authoritative: false,
			Kind:          "normalized_text",
			RuleIDs:       ids,
			Note:          "normalized text match is a heuristic suggestion, not authoritative precedence",
		})
	}
	slices.SortFunc(out, func(a, b HeuristicMatch) int {
		return strings.Compare(strings.Join(a.RuleIDs, ","), strings.Join(b.RuleIDs, ","))
	})
	return out
}

// sessionHeader and sessionRuleLine match rules.SessionHeader and rules.ruleLine.
// import_budget_test locks this projection to rules.RenderedBody.
const sessionHeader = "# Aeon session rules\n\n"

func sessionRuleLine(identity, text string) string {
	return fmt.Sprintf("- [%s] %s\n", identity, text)
}

// alwaysOnDocument counts the session file Merge would write: header plus
// identity and text for every enabled rule. Pack bodies stay in details and
// are not part of this projection. Disabled rules are omitted.
func alwaysOnDocument(in []Rule) string {
	type row struct{ id, text string }
	rows := make([]row, 0, len(in))
	for _, rule := range in {
		if !rule.Enabled {
			continue
		}
		if rule.Placement != PlacementAlwaysOn && rule.Placement != PlacementOnDemand {
			continue
		}
		rows = append(rows, row{importedIdentity(rule.Identity), rule.Text})
	}
	if len(rows) == 0 {
		return ""
	}
	slices.SortFunc(rows, func(a, b row) int { return strings.Compare(a.id, b.id) })
	var b strings.Builder
	b.WriteString(sessionHeader)
	for _, row := range rows {
		b.WriteString(sessionRuleLine(row.id, row.text))
	}
	return b.String()
}

func planID(p Proposal) string {
	h := sha256.New()
	fmt.Fprintf(h, "context=%s\nsection=%s\n", p.Context, p.Section)
	for _, file := range p.Files {
		fmt.Fprintf(h, "file=%s %s %s %s %s %s\n", file.SHA256, file.Layer, file.Kind, file.Trust, file.Placement, file.Role)
	}
	for _, rule := range p.Rules {
		fmt.Fprintf(h, "rule=%s\n", rule.ID)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func layerRank(layer Layer) int {
	switch layer {
	case LayerCompany:
		return 0
	case LayerProject:
		return 1
	case LayerPerson:
		return 2
	case LayerAgent:
		return 3
	default:
		return 9
	}
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func mapKeys(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
