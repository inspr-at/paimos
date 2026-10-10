// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentverification"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type preferenceOwner struct {
	Person *string `json:"person"`
	Source string  `json:"source"`
}

// boardDispatchAdopted reports whether dispatch should leave the legacy ladder.
// Opening the board inserts a revision-0 workspace profile and no orders.
// That initializer is not an adoption. A migrated profile keeps revision 1.
func boardDispatchAdopted(s modelprefs.BoardState) bool {
	if len(s.Orders) > 0 || len(s.Rules) > 0 || s.Workspace.Revision > 0 {
		return true
	}
	return s.Person != nil && s.Person.Revision > 0
}

// boardInitialized reports whether any board profile exists, adopted or not.
func boardInitialized(s modelprefs.BoardState) bool {
	return s.Workspace.ID != "" || s.Person != nil && s.Person.ID != ""
}

func resolveBoardWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time, excluded []string) (*WorkResolution, error) {
	if q.PersonID == nil && !q.WorkspaceOnly {
		person, err := currentPreferencePerson(ctx, tx, p)
		if err != nil {
			return nil, err
		}
		q.PersonID = person
	}
	if q.PersonID != nil {
		person, err := modelprefs.CanonicalPerson(ctx, tx, *q.PersonID)
		if err != nil {
			return nil, err
		}
		q.PersonID = person
	}
	s, err := modelprefs.LoadBoardRouting(ctx, tx, q.PersonID, q.ProjectID)
	if err != nil {
		return nil, err
	}
	if q.WorkspaceOnly {
		s.Person = nil
	}
	c, err := loadBoardCatalog(ctx, tx)
	if err != nil {
		return nil, err
	}
	c = modelsCatalog(c)
	s.Lines = c.lines
	requirement, err := boardResidency(ctx, tx, s, q.PersonID, q.ProjectID, q.TicketResidency)
	if err != nil {
		return nil, err
	}
	roleName := q.Role
	review := strings.HasPrefix(roleName, "review-gate")
	if review && q.Area == "security" {
		roleName = "review-gate-security"
	}
	validated, err := validateResolveQuery(resolveQuery{Role: roleName, AuthorFamily: q.AuthorFamily, Harness: q.Harness})
	if err != nil {
		return nil, err
	}
	q.AuthorFamily = validated.AuthorFamily
	role, _ := roleByName(roleName)
	if roleName == "build-hard" {
		q.Situation = "stuck"
	}
	query := modelprefs.BoardQuery{Column: q.Column, Area: q.Area, Labels: q.Labels, Situation: q.Situation, Review: review, Concept: q.Concept, AuthorFamily: q.AuthorFamily, EstimateHours: q.EstimateHours, FixRound: q.FixRound}
	d := modelprefs.ResolveModelsBoard(s, query)
	out := WorkResolution{Resolution: Resolution{Role: roleName, AuthorFamily: q.AuthorFamily, Source: "aeon", Ladder: []Candidate{}, OwnerRequired: true}, Residency: requirement}
	out.Trace = PreferenceTrace{PersonID: q.PersonID, Kind: d.Column, KindSource: "ticket", Complexity: q.Complexity, ComplexitySource: q.ComplexitySource, Bucket: modelprefs.BucketOf(q.Complexity, q.Role), SetBy: "default", Mode: "auto", QualifyingAccountIDs: []string{}}
	health, err := agentaccounts.HarnessHealthAt(ctx, tx, now, q.ProjectID)
	if err != nil {
		return nil, err
	}
	if review && d.Automatic {
		d.Steps = automaticReviewSteps(c, q.AuthorFamily)
		out.Trace.OrderMode = "automatic"
	}
	if roleName == "scout" || roleName == "mechanical" {
		quick, err := quickJobSteps(ctx, tx, c, role.effort)
		if err != nil {
			return nil, err
		}
		def := modelprefs.ResolveModelsBoard(s, modelprefs.BoardQuery{Column: "other", Situation: "first"})
		d = def
		d.Steps = append(quick, def.Steps...)
	}
	seen := map[string]bool{}
	for _, order := range d.Steps {
		if seen[order.Line] || slices.Contains(d.Not, order.Line) {
			continue
		}
		seen[order.Line] = true
		profiles := modelsRouteCandidates(c.profiles[order.Line], order.Effort, order.Level, review)
		if len(profiles) == 0 {
			d.Held = append(d.Held, modelprefs.HeldLine{Line: order.Line, Reason: "no registered route for this line"})
			continue
		}
		firstCause := ""
		for _, profile := range profiles {
			step, err := preferenceStep(ctx, tx, profile, roleName)
			if err != nil {
				return nil, err
			}
			reasons := skipReasons(step, role, resolveQuery{Role: roleName, AuthorFamily: q.AuthorFamily, Harness: q.Harness, OffHarnesses: q.OffHarnesses}, now, health)
			if order.Stage == "quick" && profile.Tier != "fast" {
				reasons = append(reasons, "Quick jobs requires fast tier")
			}
			if !role.readOnly && !q.Concept && profile.Family == "xai" {
				reasons = append(reasons, "No tools in PAIMOS")
			}
			if roleName == "build-hard" && profile.Tier != "strong" && profile.Tier != "frontier" {
				reasons = append(reasons, "build-hard requires strong or frontier")
			}
			if review {
				if !qualifiedReviewProfile(profile) {
					reasons = append(reasons, "review requires strong or frontier at the highest native level")
				}
				for _, policy := range q.ReviewPolicies {
					if allowed, reason := policy.Decision(q.AuthorFamily, profile.Family); !allowed {
						reasons = append(reasons, reason)
					}
				}
			}
			if slices.Contains(excluded, profile.ID) {
				reasons = append(reasons, "already attempted")
			}
			ids := []string{}
			if len(reasons) == 0 {
				if review {
					capability := agentverification.For(profile.Harness, "darwin", "arm64")
					if !capability.Supported {
						reasons = append(reasons, capability.Reason)
					} else {
						ids, err = reviewQualifiedAccountIDs(ctx, tx, profile, q.ProjectID, requirement, now)
					}
				} else {
					ids, err = boardRouteAccounts(ctx, tx, profile, q.ProjectID, requirement, now)
					if err == nil && q.Situation == "stuck" && len(excluded) > 0 {
						ids, err = agentaccounts.EscalationRoomIDs(ctx, tx, ids, now)
					}
				}
				if err != nil {
					return nil, err
				}
			}
			if len(reasons) == 0 && len(ids) == 0 {
				reasons = append(reasons, "no qualified account with available capacity")
			}
			candidate := Candidate{ProfileID: profile.ID, SkipReasons: reasons, Stage: order.Stage}
			if len(reasons) == 0 {
				candidate.Selected, out.OwnerRequired = true, false
				out.Profile, out.Trace.QualifyingAccountIDs = &profile, ids
				d.Selected, d.Lock = order.Line, d.Locks[order.Line]
				if order.Stage == "column" {
					d.CardIndex = slices.Index(d.Rank, order.Line) + 1
				}
				out.Ladder = append(out.Ladder, candidate)
				break
			}
			out.Ladder = append(out.Ladder, candidate)
			if firstCause == "" {
				firstCause = reasons[0]
			}
		}
		if out.Profile != nil {
			break
		}
		d.Held = append(d.Held, modelprefs.HeldLine{Line: order.Line, Reason: firstCause})
	}
	out.Trace.PreferenceOf = &preferenceOwner{d.PreferenceOf.Person, d.PreferenceOf.Source}
	out.Trace.Column, out.Trace.Situation, out.Trace.CardIndex, out.Trace.Lock, out.Trace.Held = d.Column, d.Situation, d.CardIndex, d.Lock, d.Held
	if d.Column == "other" && q.Area != "other" {
		out.Trace.KindSource = "fallback"
	}
	out.Trace.PrefsRevision = s.Workspace.Revision
	if s.Person != nil && d.PreferenceOf.Source == "person" {
		out.Trace.PrefsRevision, out.Trace.SetBy = s.Person.Revision, "person"
	}
	if d.Lock != nil {
		out.Trace.LockedBy = d.Lock.Scope
	}
	chain, err := modelprefs.LoadChain(ctx, tx, q.PersonID, q.ProjectID)
	if err != nil {
		return nil, err
	}
	out.Trace.Residency = modelprefs.ResolveResidency(chain)
	out.Trace.Residency.Value = requirement
	if out.Profile != nil {
		out.CommandTemplate, err = commandTemplate(out.Profile.Harness, out.Profile.Model, out.Profile.Effort, role.readOnly)
		if err != nil {
			return nil, err
		}
	} else {
		out.Trace.Blocked = "no model in the ranked order can run now"
		if len(d.Held) > 0 {
			out.Trace.Blocked = d.Held[0].Reason
		}
	}
	return &out, nil
}

// ModelsReviewHighestNative is the reversible 2026-10-10 review-floor decision.
// The minimum equivalent stays xhigh even when the native catalog has no name.
const ModelsReviewHighestNative = true
const modelsReviewMinLevel = 4

func qualifiedReviewProfile(p Profile) bool {
	return (p.Tier == "strong" || p.Tier == "frontier") && p.EffortLevel != nil && *p.EffortLevel >= modelsReviewMinLevel
}

// Normalize provider identities across today's harness ids. AEON-1145 supplies
// explicit identities later; this does not invent catalog models or routes.
func modelsLine(p Profile) (string, string) {
	identity := p
	if p.Family == "anthropic" {
		identity.Harness = "claude"
		identity.Model = strings.TrimPrefix(p.Model, "anthropic/")
	}
	if p.Family == "openai" && strings.HasPrefix(p.Model, "gpt-") {
		identity.Harness = "codex"
	}
	family, line, version := ProfileLine(identity)
	return family + ":" + line, version
}

func modelsCatalog(c boardCatalog) boardCatalog {
	out := boardCatalog{profiles: map[string][]Profile{}, lines: []modelprefs.BoardLine{}}
	for _, ps := range c.profiles {
		for _, p := range ps {
			line, _ := modelsLine(p)
			out.profiles[line] = append(out.profiles[line], p)
		}
	}
	keys := []string{}
	for line, ps := range out.profiles {
		sort.SliceStable(ps, func(i, j int) bool {
			return ps[i].Slug < ps[j].Slug || ps[i].Slug == ps[j].Slug && (ps[i].Version < ps[j].Version || ps[i].Version == ps[j].Version && ps[i].ID < ps[j].ID)
		})
		keys = append(keys, line)
	}
	sort.Strings(keys)
	for _, line := range keys {
		out.lines = append(out.lines, modelprefs.BoardLine{ID: line, Family: out.profiles[line][0].Family, Tools: true, Review: true})
	}
	return out
}

// Select the nearest level separately on each route, so a route lacking the
// exact name still gets its chance before the next model. Equal distance ties up.
func modelsRouteCandidates(ps []Profile, effort string, level int, review bool) []Profile {
	for _, p := range ps {
		if effort != "" && p.Effort == effort && p.EffortLevel != nil {
			level = *p.EffortLevel
			break
		}
	}
	latest := ""
	for _, p := range ps {
		_, version := modelsLine(p)
		if p.Enabled && !p.Retired && version != "alias" && CompareModelVersions(version, latest) > 0 {
			latest = version
		}
	}
	type route struct{ profiles []Profile }
	routes := []route{}
	keys := map[string]int{}
	for _, p := range ps {
		_, version := modelsLine(p)
		if version != "alias" && version != latest {
			continue
		}
		key := p.Harness + ":" + p.Model
		// Cursor effort suffixes represent one native route.
		if p.Harness == "cursor" && p.Family == "xai" {
			line, _ := modelsLine(p)
			key = p.Harness + ":" + line
		}
		i, ok := keys[key]
		if !ok {
			i = len(routes)
			keys[key] = i
			routes = append(routes, route{})
		}
		routes[i].profiles = append(routes[i].profiles, p)
	}
	out := []Profile{}
	for _, route := range routes {
		levels := []int{}
		exact := false
		for _, p := range route.profiles {
			if p.EffortLevel != nil {
				levels = append(levels, *p.EffortLevel)
			}
			exact = exact || effort != "" && p.Effort == effort
		}
		nearest := modelprefs.NearestEffort(level, levels)
		if review && ModelsReviewHighestNative {
			nearest = -1
			for _, v := range levels {
				nearest = max(nearest, v)
			}
			exact = false
		}
		for _, p := range route.profiles {
			if exact && p.Effort != effort || !exact && nearest >= 0 && (p.EffortLevel == nil || *p.EffortLevel != nearest) {
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

func automaticReviewSteps(c boardCatalog, author string) []modelprefs.BoardStep {
	steps := []modelprefs.BoardStep{}
	for _, line := range c.lines {
		if line.Family == author || line.Family == "unknown" {
			continue
		}
		steps = append(steps, modelprefs.BoardStep{Line: line.ID, Level: modelsReviewMinLevel, Stage: "automatic"})
	}
	strength := func(line string) int {
		best := 0
		for _, p := range c.profiles[line] {
			if qualifiedReviewProfile(p) {
				rank := 1
				if p.Tier == "frontier" {
					rank = 2
				}
				best = max(best, rank)
			}
		}
		return best
	}
	sort.SliceStable(steps, func(i, j int) bool {
		a, _, _ := strings.Cut(steps[i].Line, ":")
		b, _, _ := strings.Cut(steps[j].Line, ":")
		if reviewFamilyRank[a] != reviewFamilyRank[b] {
			return reviewFamilyRank[a] < reviewFamilyRank[b]
		}
		return strength(steps[i].Line) > strength(steps[j].Line)
	})
	return steps
}

func quickJobSteps(ctx context.Context, tx pgx.Tx, c boardCatalog, effort string) ([]modelprefs.BoardStep, error) {
	models := []string{}
	for _, ps := range c.profiles {
		for _, p := range ps {
			if p.Tier == "fast" {
				models = append(models, p.Model)
			}
		}
	}
	// Prices are catalog facts. Unknown prices sort after reported ones.
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (model) model,(input_usd_per_million+output_usd_per_million)::float8 FROM model_prices WHERE model=ANY($1::text[]) ORDER BY model,version DESC LIMIT 513`, models)
	if err != nil {
		return nil, err
	}
	prices := map[string]float64{}
	for rows.Next() {
		var model string
		var price float64
		if err := rows.Scan(&model, &price); err != nil {
			rows.Close()
			return nil, err
		}
		prices[model] = price
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	type quick struct {
		step  modelprefs.BoardStep
		price float64
		slug  string
	}
	picks := []quick{}
	for line, ps := range c.profiles {
		best := math.Inf(1)
		slug := ""
		for _, p := range ps {
			if p.Tier != "fast" {
				continue
			}
			if price, ok := prices[p.Model]; ok {
				best = min(best, price)
			}
			if slug == "" || p.Slug < slug {
				slug = p.Slug
			}
		}
		if slug != "" {
			picks = append(picks, quick{modelprefs.BoardStep{Line: line, Effort: effort, Level: 3, Stage: "quick"}, best, slug})
		}
	}
	sort.SliceStable(picks, func(i, j int) bool {
		if picks[i].price != picks[j].price {
			return picks[i].price < picks[j].price
		}
		return picks[i].slug < picks[j].slug
	})
	out := []modelprefs.BoardStep{}
	for _, pick := range picks {
		out = append(out, pick.step)
	}
	return out, nil
}

// PinnedBottomUsed is called only when the planned profile actually starts.
func PinnedBottomUsed(raw []byte, profile string) (map[string]any, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	if len(raw) > 1<<20 {
		return nil, false, fmt.Errorf("placement exceeds evidence bound")
	}
	var placement struct {
		PlannedProfileID *string               `json:"planned_profile_id"`
		Lock             *modelprefs.BoardLock `json:"lock"`
		Column           string                `json:"column"`
		Situation        string                `json:"situation"`
		PreferenceOf     *preferenceOwner      `json:"preference_of"`
	}
	if err := json.Unmarshal(raw, &placement); err != nil {
		return nil, false, fmt.Errorf("pinned-bottom evidence: %w", err)
	}
	if placement.Lock == nil || placement.Lock.Value != "bottom" || placement.PlannedProfileID == nil || *placement.PlannedProfileID != profile {
		return nil, false, nil
	}
	return map[string]any{"column": placement.Column, "situation": placement.Situation, "preference_of": placement.PreferenceOf, "lock": placement.Lock, "profile_id": profile}, true, nil
}

// joinReasons keeps one sentence per cause across efforts and fallback stages.
func joinReasons(reasons []string) string {
	out := []string{}
	for _, reason := range reasons {
		for _, part := range strings.Split(reason, ";") {
			part = strings.TrimSpace(part)
			for _, stage := range []string{"column: ", "default: ", "role: "} {
				part = strings.TrimPrefix(part, stage)
			}
			if part != "" && !slices.Contains(out, part) {
				out = append(out, part)
			}
		}
	}
	return strings.Join(out, "; ")
}
