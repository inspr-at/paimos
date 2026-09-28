// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/releasehistory"
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
		body, sum, err := readDoctrine(clean)
		if err != nil {
			return Proposal{}, err
		}
		file, err := classify(clean, section, len(body))
		if err != nil {
			return Proposal{}, err
		}
		file.SHA256 = sum
		if !contextAllowed(in.Context, file.Trust) {
			return Proposal{}, fmt.Errorf("%w: %s is %s, plan context is %s", ErrMixedContext, file.Base, file.Trust, in.Context)
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
	proposal := assemble(in.Context, section, files, raws, unresolved)
	proposal.Adapter = adapterFromPath(in.AR1Draft)
	proposal.PlanID = planID(proposal)
	return proposal, nil
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
		rule  Rule
		order int
	}
	type identityGroup struct {
		order int
		fps   []string
		byFP  map[string]*fpGroup
	}
	groups := map[string]*identityGroup{}
	var order []string
	seq := 0
	for _, item := range raws {
		rule := toRule(item)
		seq++
		ig := groups[rule.Identity]
		if ig == nil {
			ig = &identityGroup{order: seq, byFP: map[string]*fpGroup{}}
			groups[rule.Identity] = ig
			order = append(order, rule.Identity)
		}
		fp := fingerprint(rule)
		existing := ig.byFP[fp]
		if existing == nil {
			rule.Sources = []SourceRef{rule.Sources[0]}
			ig.byFP[fp] = &fpGroup{rule: rule, order: seq}
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
	doc := alwaysOnDocument(rules, files)
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
		OwnerPolicy:    OwnerPolicy,
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
			Path:       item.file.Path,
			StartLine:  raw.start,
			EndLine:    raw.end,
			SHA256:     hashLines(item.lines, raw.start, raw.end),
			FileSHA256: item.file.SHA256,
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

func alwaysOnDocument(rules []Rule, files []SourceFile) string {
	var b strings.Builder
	for _, file := range files {
		if file.Placement != PlacementOnDemand {
			continue
		}
		n := 0
		for _, rule := range rules {
			for _, source := range rule.Sources {
				if source.Path == file.Path {
					n++
					break
				}
			}
		}
		fmt.Fprintf(&b, "on-demand: %s (%d rules)\n", file.Base, n)
	}
	var sets []string
	grouped := map[string][]Rule{}
	for _, rule := range rules {
		if rule.Placement != PlacementAlwaysOn {
			continue
		}
		if _, ok := grouped[rule.Set]; !ok {
			sets = append(sets, rule.Set)
		}
		grouped[rule.Set] = append(grouped[rule.Set], rule)
	}
	for _, set := range sets {
		fmt.Fprintf(&b, "## %s\n", set)
		for _, rule := range grouped[set] {
			fmt.Fprintf(&b, "- %s\n", rule.Text)
		}
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

// ImportDraft asks an injected AR1 port for one tenant draft.
// A false Authorized flag, a missing port, or any publish request fails closed.
// The CLI never sets Authorized.
func ImportDraft(ctx context.Context, req DraftRequest, w DraftWriter) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.Publish {
		return fmt.Errorf("%w: importer does not publish rules", ErrPublishRefused)
	}
	if req.Version != "" {
		if !releasehistory.ValidVersion(req.Version) {
			return fmt.Errorf("%w: publication coordinate is not inspr-calendar-v2", ErrPublishRefused)
		}
		return fmt.Errorf("%w: importer does not publish ruleset versions", ErrPublishRefused)
	}
	if !req.Authorized {
		return fmt.Errorf("%w: caller is not authorized to write a draft", ErrDraftUnavailable)
	}
	if !validTenant(req.Tenant) {
		return fmt.Errorf("%w: one tenant slug is required", ErrDraftUnavailable)
	}
	if !validContext(req.Context) {
		return fmt.Errorf("%w: one trust context is required", ErrDraftUnavailable)
	}
	if w == nil {
		return fmt.Errorf("%w: AR1 draft client is not registered", ErrDraftUnavailable)
	}
	req.Proposal.Mode = "draft"
	req.Publish = false
	return w.WriteDraft(ctx, req)
}

func validTenant(slug string) bool {
	if slug == "" || len(slug) > 63 {
		return false
	}
	for i, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}
