// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	if q.Role == "scout" || q.Role == "mechanical" {
		return nil, nil
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
	// A revision-0 workspace row is the closed initializer. It must not replace
	// saved role routes for dispatch, review or escalation. Migrated profiles
	// use the default revision, and a save or a rule adopts the board. An
	// explicit placement request still reads the initialized board, but never
	// one that was not initialized (saved role routes without a board).
	if !boardDispatchAdopted(s) && !(q.ExplicitBoard && boardInitialized(s)) {
		return nil, nil
	}
	c, err := loadBoardCatalog(ctx, tx)
	if err != nil {
		return nil, err
	}
	if q.WorkspaceOnly {
		s.Person = nil
	}
	s.Lines = c.lines
	requirement, err := boardResidency(ctx, tx, s, q.PersonID, q.ProjectID, q.TicketResidency)
	if err != nil {
		return nil, err
	}
	roleName := q.Role
	review := strings.HasPrefix(q.Role, "review-gate")
	if review && q.Area == "security" {
		roleName = "review-gate-security"
	}
	role, ok := roleByName(roleName)
	if !ok {
		return nil, fail(400, "unknown model role")
	}
	out := WorkResolution{Resolution: Resolution{Role: roleName, AuthorFamily: q.AuthorFamily, Source: "aeon", Ladder: []Candidate{}, OwnerRequired: true}, Residency: requirement}
	out.Trace = PreferenceTrace{PersonID: q.PersonID, Kind: q.Area, KindSource: "ticket", Complexity: q.Complexity, ComplexitySource: q.ComplexitySource, Bucket: modelprefs.BucketOf(q.Complexity, q.Role), SetBy: "default", Mode: "auto", QualifyingAccountIDs: []string{}}
	health, err := agentaccounts.HarnessHealthAt(ctx, tx, now, q.ProjectID)
	if err != nil {
		return nil, err
	}
	query := modelprefs.BoardQuery{Column: q.Column, Area: q.Area, Labels: q.Labels, Situation: q.Situation, Review: review, Concept: q.Concept, AuthorFamily: q.AuthorFamily, PreviousFamily: q.PreviousFamily, EstimateHours: q.EstimateHours, FixRound: q.FixRound}
	initial := modelprefs.ResolveBoard(s, query, nil)
	if modelprefs.Strictness(requirement) > 0 {
		counts, err := c.residencyCounts(ctx, tx, q.ProjectID, requirement)
		if err != nil {
			return nil, err
		}
		s.Lines = c.residencyLines(s.Lines, counts, initial, requirement)
	}
	var callbackErr error
	admissionWait := false
	stage := "column"
	available := func(line string, level int) (bool, string) {
		ps := c.effortCandidates(line, initial.Effort, level, review)
		reasons := []string{}
		for _, profile := range ps {
			step, err := preferenceStep(ctx, tx, profile, roleName)
			if err != nil {
				callbackErr = err
				return false, "policy unavailable"
			}
			skipped := skipReasons(step, role, resolveQuery{Role: roleName, AuthorFamily: q.AuthorFamily, Harness: q.Harness}, now, health)
			if profile.Family == "xai" && !review && !q.Concept {
				skipped = append(skipped, "No tools in PAIMOS")
			}
			if review && (profile.Effort != "xhigh" || profile.Tier != "strong" && profile.Tier != "frontier") {
				skipped = append(skipped, "Review requires a qualified strong or frontier model at xhigh")
			}
			if slices.Contains(q.OffHarnesses, profile.Harness) {
				skipped = append(skipped, "harness is off in the plan")
			}
			if slices.Contains(excluded, profile.ID) {
				skipped = append(skipped, "already attempted")
			}
			if q.Situation == "stuck" && len(excluded) > 0 && escalationRank(profile, q.Area) >= 99 {
				skipped = append(skipped, "not a stronger route for this work area")
			}
			ids := []string{}
			if len(skipped) == 0 {
				admissionWait = true
			}
			if review && len(skipped) == 0 {
				capability := agentverification.For(profile.Harness, "darwin", "arm64")
				if !capability.Supported {
					skipped = append(skipped, capability.Reason)
				} else {
					account, err := agentaccounts.ReviewAccount(ctx, tx, profile.ID, profile.Harness, q.ProjectID, now, requirement)
					if err != nil {
						callbackErr = err
						return false, "account unavailable"
					}
					if account != nil {
						native := true
						if profile.Harness == "grok" {
							native, err = nativeGrokEnrolled(ctx, tx, account.ID)
							if err != nil {
								callbackErr = err
								return false, "enrollment unavailable"
							}
						}
						if native {
							ids = append(ids, account.ID)
						} else {
							skipped = append(skipped, "native Grok needs a qualified macOS arm64 enrollment")
						}
					}
				}
			} else if !review && len(skipped) == 0 {
				ids, err = boardRouteAccounts(ctx, tx, profile, q.ProjectID, requirement, now)
				if err != nil {
					callbackErr = err
					return false, "account unavailable"
				}
				if q.Situation == "stuck" && len(excluded) > 0 {
					ids, err = agentaccounts.EscalationRoomIDs(ctx, tx, ids, now)
					if err != nil {
						callbackErr = err
						return false, "escalation account unavailable"
					}
				}
			}
			if len(ids) == 0 && len(skipped) == 0 {
				skipped = append(skipped, "no qualified account with available capacity")
			}
			candidate := Candidate{ProfileID: profile.ID, SkipReasons: skipped, Stage: stage}
			if len(skipped) == 0 {
				candidate.Selected = true
				out.Profile = &profile
				out.Trace.QualifyingAccountIDs = ids
				out.OwnerRequired = false
				out.Ladder = append(out.Ladder, candidate)
				return true, ""
			}
			out.Ladder = append(out.Ladder, candidate)
			reasons = append(reasons, skipped...)
		}
		if len(reasons) == 0 {
			return false, "no registered effort for this line"
		}
		return false, joinReasons(reasons)
	}
	d := modelprefs.ResolveBoard(s, query, available)
	// Policy exclusions are traced even when the pure board resolver removes
	// them before attempting account qualification.
	for _, held := range d.Cant {
		d.Held = append(d.Held, held)
	}
	for _, line := range d.Not {
		reason := "not allowed"
		if lock := d.Locks[line]; lock != nil && lock.Why != "" {
			reason = lock.Why
		}
		d.Held = append(d.Held, modelprefs.HeldLine{Line: line, Reason: reason})
	}
	// Exhaust the column, then the default order on the deciding layer, then
	// the job's role ladder. Policies and cross-family checks precede accounts.
	if out.Profile == nil {
		try := func(line string, nextStage string) bool {
			stage = nextStage
			if slices.Contains(d.Not, line) {
				d.Held = append(d.Held, modelprefs.HeldLine{Line: line, Reason: "not allowed"})
				return false
			}
			ok, reason := available(line, initial.EffortLevel)
			if ok {
				d.CardIndex = 0
				d.Lock = d.Locks[line]
			}
			if !ok {
				d.Held = append(d.Held, modelprefs.HeldLine{Line: line, Reason: nextStage + ": " + reason})
			}
			return ok
		}
		if d.Column != "other" {
			fallbackState := s
			if d.PreferenceOf.Source != "person" {
				fallbackState.Person = nil
			}
			if len(d.Top) > 0 {
				if lock := d.Locks[d.Top[0]]; lock != nil && lock.Scope == "workspace" {
					fallbackState.Person = nil
				}
			}
			def := rawBoardDecision(fallbackState, "other", d.Situation)
			for _, line := range def.Not {
				if !slices.Contains(d.Not, line) {
					d.Not = append(d.Not, line)
				}
				d.Held = append(d.Held, modelprefs.HeldLine{Line: line, Reason: "default: not allowed"})
			}
			for _, line := range def.Rank {
				if slices.Contains(def.Not, line) {
					continue
				}
				if try(line, "default") {
					break
				}
			}
		}
		if out.Profile == nil {
			steps, err := loadLadderLimit(ctx, tx, roleName, 257)
			if err != nil {
				return nil, err
			}
			if len(steps) > 256 {
				return nil, modelprefs.ErrBoardBounds
			}
			seen := map[string]bool{}
			for _, step := range steps {
				family, line, _ := ProfileLine(step.Profile)
				id := family + ":" + line
				if seen[id] {
					continue
				}
				seen[id] = true
				if try(id, "role") {
					break
				}
			}
		}
	}
	if callbackErr != nil {
		return nil, callbackErr
	}
	out.Trace.PreferenceOf = &preferenceOwner{d.PreferenceOf.Person, d.PreferenceOf.Source}
	out.Trace.Column, out.Trace.Situation, out.Trace.CardIndex, out.Trace.Lock, out.Trace.Held = d.Column, d.Situation, d.CardIndex, d.Lock, d.Held
	out.Trace.Kind = d.Column
	if d.Column == "other" && q.Area != "other" {
		out.Trace.KindSource = "fallback"
	}
	out.Trace.PrefsRevision = s.Workspace.Revision
	if s.Person != nil && d.PreferenceOf.Source == "person" {
		out.Trace.PrefsRevision = s.Person.Revision
		out.Trace.SetBy = "person"
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
	} else if out.Profile == nil {
		out.Trace.Blocked = "no model in the ranked order can run now"
		if roleName == "review-gate-security" {
			out.Trace.Blocked = "no qualified security review profile/account"
		}
		if q.Situation == "stuck" && admissionWait {
			out.Trace.Blocked = "admission_wait"
		}
	}
	return &out, nil
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
