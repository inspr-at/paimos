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

func resolveBoardWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time, excluded []string) (*WorkResolution, error) {
	if q.Role == "scout" || q.Role == "mechanical" {
		return nil, nil
	}
	if q.PersonID == nil {
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
	// During the read-only legacy release, unconverted in-memory callers retain
	// their old selector semantics. Migrated tenants and saved boards use this path.
	if s.Workspace.ID == "" && (s.Person == nil || s.Person.ID == "") {
		return nil, nil
	}
	c, err := loadBoardCatalog(ctx, tx)
	if err != nil {
		return nil, err
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
	health, err := agentaccounts.HarnessHealthAt(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	var callbackErr error
	d := modelprefs.ResolveBoard(s, modelprefs.BoardQuery{Area: q.Area, Labels: q.Labels, Situation: q.Situation, Review: review, Concept: q.Concept, AuthorFamily: q.AuthorFamily, PreviousFamily: q.PreviousFamily, EstimateHours: q.EstimateHours, FixRound: q.FixRound}, func(line string, level int) (bool, string) {
		ps := c.candidates(line, level, review)
		reasons := []string{}
		for _, profile := range ps {
			step, err := preferenceStep(ctx, tx, profile, roleName)
			if err != nil {
				callbackErr = err
				return false, "policy unavailable"
			}
			skipped := skipReasons(step, role, resolveQuery{Role: roleName, AuthorFamily: q.AuthorFamily, Harness: q.Harness}, now, health)
			if slices.Contains(excluded, profile.ID) {
				skipped = append(skipped, "already attempted")
			}
			if len(excluded) > 0 && escalationRank(profile, q.Area) >= 99 {
				skipped = append(skipped, "not a stronger route for this work area")
			}
			ids := []string{}
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
							err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_requests r ON r.tenant_id=e.tenant_id AND r.id=e.request_id WHERE e.account_id=$1 AND r.details->>'platform'='darwin' AND r.details->>'arch'='arm64')`, account.ID).Scan(&native)
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
				if len(excluded) > 0 {
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
			candidate := Candidate{ProfileID: profile.ID, SkipReasons: skipped}
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
		return false, strings.Join(reasons, "; ")
	})
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
	if s.Person != nil {
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
	} else {
		out.Trace.Blocked = "no model in the ranked order can run now"
	}
	if len(d.Cant) > 0 {
		out.Trace.Held = append(out.Trace.Held, d.Cant...)
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
