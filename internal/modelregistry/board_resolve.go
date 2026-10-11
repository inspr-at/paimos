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
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/agentverification"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type preferenceOwner struct {
	Person *string `json:"person"`
	Source string  `json:"source"`
}

// Models is the dispatch authority for migrated and newly-created tenants.
func boardDispatchAdopted(s modelprefs.BoardState) bool { return true }

func boardInitialized(s modelprefs.BoardState) bool {
	return s.Workspace.ID != "" || s.Person != nil && s.Person.ID != ""
}

func resolveBoardWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time, excluded []string, policies ...reviewgate.FamilyPolicy) (*WorkResolution, error) {
	// Routine evaluation supplies its mandatory family policy through this
	// boundary; keep it in the same Models qualification as project policies.
	if len(policies) > 0 {
		q.ReviewPolicies = policies
	}
	// Bound account/window fan-out before any availability projection.
	var large bool
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM (SELECT 1 FROM agent_accounts LIMIT 257) a)>256 OR (SELECT count(*) FROM (SELECT 1 FROM account_allowance_windows WHERE NOT pairing_verification AND NOT capacity_retired AND removed_at IS NULL LIMIT 4097) w)>4096`).Scan(&large); err != nil {
		return nil, err
	}
	if large {
		return nil, modelprefs.ErrBoardBounds
	}
	excludedIdentity := map[string]bool{}
	for _, id := range excluded {
		var harness, model, effort string
		if err := tx.QueryRow(ctx, `SELECT harness,model,effort FROM model_profiles WHERE id=$1::uuid`, id).Scan(&harness, &model, &effort); err != nil {
			return nil, err
		}
		excludedIdentity[harness+":"+model+":"+effort] = true
	}
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
	if !boardDispatchAdopted(s) && !(q.ExplicitBoard && boardInitialized(s)) {
		return nil, nil
	}
	c, err := q.preview.board(ctx, tx)
	if err != nil {
		return nil, err
	}
	s = modelsBoardIdentities(s, c)
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
	query := modelprefs.BoardQuery{Column: q.Column, Area: q.Area, Labels: q.Labels, Situation: q.Situation, Review: review, Concept: q.Concept, AuthorFamily: q.AuthorFamily, PreviousFamily: q.PreviousFamily, EstimateHours: q.EstimateHours, FixRound: q.FixRound}
	if review && !boardDispatchAdopted(s) {
		// Closed-board placement previews retain their public review column.
		query.Column = "review:" + q.AuthorFamily
	}
	d := modelsBoardDecision(s, query)
	chain, err := modelprefs.LoadChain(ctx, tx, q.PersonID, q.ProjectID)
	if err != nil {
		return nil, err
	}
	lockedProfiles := map[string]bool{}
	placementRole := q.TicketRole
	if placementRole == "" {
		placementRole = q.Role
	}
	cell := modelprefs.ResolveCell(chain, d.Column, modelprefs.BucketOf(q.Complexity, placementRole))
	lockedEscalation := q.Escalation && cell.LockedBy != "" && cell.Cell != nil && cell.Cell.Mode != "auto"
	if lockedEscalation {
		profiles := []Profile{}
		for _, ps := range c.profiles {
			profiles = append(profiles, ps...)
		}
		for _, profile := range expandPreference(*cell.Cell, profiles) {
			lockedProfiles[profile.ID] = true
		}
	}
	out := WorkResolution{Resolution: Resolution{Role: roleName, AuthorFamily: q.AuthorFamily, Source: "aeon", Ladder: []Candidate{}, OwnerRequired: true}, Residency: requirement}
	out.Trace = PreferenceTrace{PersonID: q.PersonID, Kind: d.Column, KindSource: "ticket", Complexity: q.Complexity, ComplexitySource: q.ComplexitySource, Bucket: modelprefs.BucketOf(q.Complexity, placementRole), SetBy: "default", Mode: "auto", QualifyingAccountIDs: []string{}}
	health, err := agentaccounts.HarnessHealthAt(ctx, tx, now, q.ProjectID)
	if err != nil {
		return nil, err
	}
	if review {
		out.Trace.OrderMode = reviewOrderSaved
		if d.Automatic {
			if len(d.Steps) == 0 {
				out.Trace.OrderMode = reviewOrderLegacy
			}
			d.Steps = append(d.Steps, automaticReviewSteps(c, q.AuthorFamily)...)
		}
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
	daily := modelsDailyAdmission{principal: p, snapshots: map[string]agentplan.Snapshot{}, unknown: map[string]bool{}}
	seen := map[string]bool{}
	admissionWait := false
	residencyHeld := false
	waitCode := ""
	for _, order := range d.Steps {
		key := order.Line
		if order.Stage == "quick" {
			key = "quick:" + key
		}
		if seen[key] || order.Stage != "quick" && slices.Contains(d.Not, order.Line) {
			continue
		}
		seen[key] = true
		profiles := modelsRouteCandidates(c.profiles[order.Line], order.Effort, order.Level, review)
		if len(profiles) == 0 {
			d.Held = append(d.Held, modelprefs.HeldLine{Line: order.Line, Reason: "no registered route for this line"})
			continue
		}
		if waitCode != "" {
			out.Trace.Blocked = waitCode
			break
		}
		firstCause := ""
		dailyWait := ""
		for _, profile := range profiles {
			step, err := preferenceStep(ctx, tx, profile, roleName)
			if err != nil {
				return nil, err
			}
			reasons := skipReasons(step, role, resolveQuery{Role: roleName, AuthorFamily: q.AuthorFamily, Harness: q.Harness, OffHarnesses: q.OffHarnesses}, now, health)
			if slices.Contains(q.OffHarnesses, profile.Harness) {
				// Models handles admission below; this caller-supplied exclusion
				// is the plan's Off switch, which skipReasons reports first.
				reasons[0] = "harness off in the plan"
			}
			availabilityReasons := len(reasons)
			policyAllowed := len(skipReasons(step, role, resolveQuery{Role: roleName, AuthorFamily: q.AuthorFamily, Harness: q.Harness, OffHarnesses: q.OffHarnesses}, now, nil)) == 0
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
			if q.Escalation && escalationRank(profile, q.Area) >= 99 {
				reasons = append(reasons, "not a stronger route for this work area")
			}
			if lockedEscalation && !lockedProfiles[profile.ID] {
				reasons = append(reasons, "locked model preference")
			}
			if slices.Contains(excluded, profile.ID) || excludedIdentity[profile.Harness+":"+profile.Model+":"+profile.Effort] {
				reasons = append(reasons, "already attempted")
			}
			if q.Escalation && policyAllowed && len(reasons) == availabilityReasons {
				admissionWait = true
			}
			ids := []string{}
			residencyOnly := false
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
				}
				if err == nil && len(reasons) == 0 && len(ids) == 0 && requirement != "any" {
					residencyOnly, err = refusedForResidency(ctx, tx, profile, q.ProjectID, review, now)
				}
				if err == nil && !review && q.Escalation {
					ids, err = agentaccounts.EscalationRoomIDs(ctx, tx, ids, now)
				}
				if err != nil {
					return nil, err
				}
			}
			if len(reasons) == 0 && len(ids) > 0 {
				var cause string
				ids, cause, err = daily.filter(ctx, tx, profile, ids, now)
				if err != nil {
					return nil, err
				}
				if cause != "" {
					reasons = append(reasons, cause)
					if daily.wait || q.Harness != "" {
						dailyWait = daily.blocked
					}
				}
			}
			if len(reasons) == 0 && len(ids) == 0 {
				if residencyOnly {
					reasons = append(reasons, "not allowed: "+requirement)
					residencyHeld = true
				} else {
					reasons = append(reasons, "no qualified account with available capacity")
				}
			}
			stage := order.Stage
			if stage == "quick" || stage == "automatic" {
				stage = "column"
			}
			candidate := Candidate{ProfileID: profile.ID, SkipReasons: reasons, Stage: stage}
			if len(reasons) == 0 {
				candidate.Selected, out.OwnerRequired = true, false
				if order.Stage == "quick" {
					out.Trace.Prefs = "quick jobs"
				}
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
		if order.Stage == "default" {
			firstCause = "default: " + firstCause
		}
		d.Held = append(d.Held, modelprefs.HeldLine{Line: order.Line, Reason: firstCause})
		waitCode = dailyWait
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
	out.Trace.Residency = modelprefs.ResolveResidency(chain)
	out.Trace.Residency.Value = requirement
	if out.Profile != nil {
		out.CommandTemplate, err = commandTemplate(out.Profile.Harness, out.Profile.Model, out.Profile.Effort, role.readOnly)
		if err != nil {
			return nil, err
		}
	} else {
		if out.Trace.Blocked == "" {
			out.Trace.Blocked = "no model in the ranked order can run now"
			if len(d.Held) > 0 {
				out.Trace.Blocked = d.Held[0].Reason
			}
			if residencyHeld {
				out.Trace.Blocked = "no " + requirement + " model route"
			}
			// An escalation outcome is the whole walk's, not its first line's:
			// the Decision Desk stores it as the route block.
			if q.Escalation {
				out.Trace.Blocked = "no allowed stronger route"
				if lockedEscalation {
					out.Trace.Blocked = "locked model preference"
				}
			}
		}
		if roleName == "review-gate-security" {
			out.Trace.Blocked = "no qualified security review profile/account"
			out.Trace.Hard = []string{"security_review"}
		}
		if waitCode == "daily_limit_unknown" {
			out.Trace.Blocked = waitCode
		}
		if q.Escalation && admissionWait {
			out.Trace.Blocked = "admission_wait"
			out.OwnerRequired = false
		}
	}
	return &out, nil
}

// refusedForResidency reports whether accounts would run this route without
// the residency requirement, so the walk names residency as the cause.
func refusedForResidency(ctx context.Context, tx pgx.Tx, profile Profile, project string, review bool, now time.Time) (bool, error) {
	var ids []string
	var err error
	if review {
		ids, err = reviewQualifiedAccountIDs(ctx, tx, profile, project, "any", now)
	} else {
		ids, err = boardRouteAccounts(ctx, tx, profile, project, "any", now)
	}
	return len(ids) > 0, err
}

// Keep the queued-round policy and saved family review rows during the S2
// transition. Explicit Models situations and workspace pick locks win; the
// ordinary Models walk retains its saved order when no previous family is sent.
func modelsBoardDecision(s modelprefs.BoardState, q modelprefs.BoardQuery) modelprefs.BoardDecision {
	d := modelprefs.ResolveModelsBoard(s, q)
	if q.Review && d.FollowsFirst {
		column := "review:" + q.AuthorFamily
		legacy := s
		for _, rule := range s.Rules {
			if rule.Column == d.Column && rule.Scope == "workspace" && rule.Lock == "top" {
				legacy.Person = nil
				break
			}
		}
		legacy.Orders = slices.Clone(s.Orders)
		saved := false
		for i := range legacy.Orders {
			o := &legacy.Orders[i]
			if o.Column != column || o.Situation != "first" || o.Rank == nil || o.ProfileID != legacy.Workspace.ID && (legacy.Person == nil || o.ProfileID != legacy.Person.ID) {
				continue
			}
			saved = true
			if o.End == "" {
				// A saved review chain must not acquire a build Default tail.
				o.End = "wait"
			}
		}
		if saved {
			q.Column, q.Situation, q.Review = column, "first", false
			d = modelprefs.ResolveModelsBoard(legacy, q)
		}
	}
	if d.Situation != "stuck" || !d.FollowsFirst || q.PreviousFamily == "" {
		return d
	}
	for _, rule := range s.Rules {
		if rule.Column == d.Column && rule.Scope == "workspace" && rule.Lock == "top" {
			return d
		}
	}
	// Only inherited, unpinned row steps rotate. Keep pinned positions, Default
	// continuation, and each step's native effort attached to their saved line.
	positions := []int{}
	steps := []modelprefs.BoardStep{}
	for i, step := range d.Steps {
		if step.Stage == "column" && d.Locks[step.Line] == nil {
			positions = append(positions, i)
			steps = append(steps, step)
		}
	}
	family := func(step modelprefs.BoardStep) string { value, _, _ := strings.Cut(step.Line, ":"); return value }
	sort.SliceStable(steps, func(i, j int) bool {
		return family(steps[i]) != q.PreviousFamily && family(steps[j]) == q.PreviousFamily
	})
	for i, position := range positions {
		d.Steps[position] = steps[i]
	}
	d.Rank = d.Rank[:0]
	for _, step := range d.Steps {
		d.Rank = append(d.Rank, step.Line)
	}
	return d
}

// ModelsReviewHighestNative is the reversible 2026-10-10 review-floor decision.
// The minimum equivalent stays xhigh even when the native catalog has no name.
const ModelsReviewHighestNative = modelprefs.ReviewHighestNative
const modelsReviewMinLevel = 4

func qualifiedReviewProfile(p Profile) bool {
	return (p.Tier == "strong" || p.Tier == "frontier") && p.EffortLevel != nil && *p.EffortLevel >= modelsReviewMinLevel
}

// Normalize provider identities across today's harness ids. AEON-1145 supplies
// explicit identities later; this does not invent catalog models or routes.
func modelsLine(p Profile) (string, string) {
	return modelprefs.ModelsLine(p.Family, p.Harness, p.Model)
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

// Existing pickers stored harness-derived line ids. Fold those references into
// the provider line too, so a saved Cursor pick retains its meaning until S2/S3
// migrate storage and the picker. The stored document is never rewritten here.
func modelsBoardIdentities(s modelprefs.BoardState, c boardCatalog) modelprefs.BoardState {
	aliases := map[string]string{}
	for old, ps := range c.profiles {
		if len(ps) > 0 {
			aliases[old], _ = modelsLine(ps[0])
		}
	}
	canonical := func(id string) string {
		if next, ok := aliases[id]; ok {
			return next
		}
		return id
	}
	s.Orders = slices.Clone(s.Orders)
	for i := range s.Orders {
		o := &s.Orders[i]
		o.Rank, o.Not = slices.Clone(o.Rank), slices.Clone(o.Not)
		for j, id := range o.Rank {
			o.Rank[j] = canonical(id)
		}
		for j, id := range o.Not {
			o.Not[j] = canonical(id)
		}
		if len(o.Efforts) > 0 {
			efforts := map[string]string{}
			keys := []string{}
			for id := range o.Efforts {
				keys = append(keys, id)
			}
			sort.Strings(keys)
			for _, id := range keys {
				next := canonical(id)
				if own, ok := o.Efforts[next]; ok {
					efforts[next] = own
				} else {
					efforts[next] = o.Efforts[id]
				}
			}
			o.Efforts = efforts
		}
	}
	s.Rules = slices.Clone(s.Rules)
	for i := range s.Rules {
		s.Rules[i].Line = canonical(s.Rules[i].Line)
	}
	return s
}

// Select the nearest level separately on each route, so a route lacking the
// exact name still gets its chance before the next model. Equal distance ties up.
// The account picker shares this choice (modelprefs.RouteCandidates).
func modelsRouteCandidates(ps []Profile, effort string, level int, review bool) []Profile {
	routes := make([]modelprefs.RouteProfile, len(ps))
	for i, p := range ps {
		routes[i] = modelprefs.NewRouteProfile(p.Family, p.Harness, p.Model, p.Effort, p.EffortLevel, p.Enabled && !p.Retired)
	}
	out := []Profile{}
	for _, i := range modelprefs.RouteCandidates(routes, effort, level, review) {
		out = append(out, ps[i])
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
			if p.Enabled && !p.Retired && qualifiedReviewProfile(p) {
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
