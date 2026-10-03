// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type WorkQuery struct {
	TicketID         string
	Role             string
	TicketRole       string
	Area             string
	Complexity       string
	ComplexitySource string
	ProjectID        string
	PersonID         *string
	AuthorFamily     string
	Harness          string
	TicketResidency  string
}
type PreferenceTrace struct {
	Selector             *modelprefs.Cell           `json:"selector,omitempty"`
	Role                 string                     `json:"role,omitempty"`
	ProjectID            string                     `json:"project_id,omitempty"`
	TicketRequirement    string                     `json:"ticket_requirement,omitempty"`
	Kind                 string                     `json:"kind"`
	KindSource           string                     `json:"kind_source"`
	Complexity           string                     `json:"complexity"`
	ComplexitySource     string                     `json:"complexity_source"`
	Bucket               string                     `json:"bucket"`
	PersonID             *string                    `json:"person_id"`
	SetBy                string                     `json:"set_by"`
	LockedBy             string                     `json:"locked_by,omitempty"`
	Mode                 string                     `json:"mode"`
	PrefsRevision        int64                      `json:"prefs_revision"`
	Residency            modelprefs.ResidencyResult `json:"residency"`
	QualifyingAccountIDs []string                   `json:"qualifying_account_ids"`
	PreferredCandidates  []Candidate                `json:"preferred_candidates,omitempty"`
	LatestResolvedTo     string                     `json:"latest_resolved_to,omitempty"`
	Fallback             string                     `json:"fallback,omitempty"`
	Blocked              string                     `json:"blocked,omitempty"`
	Prefs                string                     `json:"prefs,omitempty"`
	Hard                 []string                   `json:"hard,omitempty"`
}
type WorkResolution struct {
	Resolution
	Residency string          `json:"residency"`
	Trace     PreferenceTrace `json:"trace"`
}

func placementTrace(ctx context.Context, tx pgx.Tx, q WorkQuery) ([]modelprefs.Scope, PreferenceTrace, string, error) {
	person := q.PersonID
	if person != nil {
		var err error
		person, err = modelprefs.CanonicalPerson(ctx, tx, *person)
		if err != nil {
			return nil, PreferenceTrace{}, "", err
		}
	}
	chain, err := modelprefs.LoadChain(ctx, tx, person, q.ProjectID)
	if err != nil {
		return nil, PreferenceTrace{}, "", err
	}
	rr := modelprefs.ResolveResidency(chain)
	requirement := modelprefs.Strictest(rr.Value, q.TicketResidency)
	source := q.ComplexitySource
	if q.Complexity == "" {
		source = "role"
	}
	role := q.TicketRole
	if role == "" {
		role = q.Role
	}
	trace := PreferenceTrace{PersonID: person, Complexity: q.Complexity, ComplexitySource: source, Bucket: modelprefs.BucketOf(q.Complexity, role),
		SetBy: "default", Mode: "auto", Residency: rr, QualifyingAccountIDs: []string{}}
	return chain, trace, requirement, nil
}
func traceCell(trace *PreferenceTrace, result modelprefs.CellResult) {
	trace.SetBy = result.SetBy
	trace.LockedBy = result.LockedBy
	trace.PrefsRevision = result.Revision
	if result.Cell != nil {
		trace.Selector = result.Cell
		trace.Mode = result.Cell.Mode
	}
	if result.KindFallback {
		trace.KindSource = "fallback"
	}
}
func expandPreference(cell modelprefs.Cell, profiles []Profile) []Profile {
	out := []Profile{}
	for _, p := range profiles {
		if cell.Mode == "pinned" {
			if cell.ProfileID == p.ID {
				out = append(out, p)
			}
			continue
		}
		family, line, version := ProfileLine(p)
		if version != "" && family == cell.Family && line == cell.Line && p.Effort == cell.Effort && (cell.Harness == "" || p.Harness == cell.Harness) {
			out = append(out, p)
		}
	}
	rank := map[string]int{"codex": 0, "claude": 1, "grok": 2, "pi": 3, "cursor": 4}
	sort.SliceStable(out, func(i, j int) bool {
		_, _, a := ProfileLine(out[i])
		_, _, b := ProfileLine(out[j])
		if n := CompareModelVersions(a, b); n != 0 {
			return n > 0
		}
		if rank[out[i].Harness] != rank[out[j].Harness] {
			return rank[out[i].Harness] < rank[out[j].Harness]
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ResolveWork preserves resolveRole byte-for-byte with an empty matrix and an
// any requirement. Placement chooses a cell; it never changes the role ladder.
func ResolveWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time) (WorkResolution, error) {
	if q.TicketID != "" {
		var fields []byte
		var project *string
		if err := tx.QueryRow(ctx, `SELECT n.fields,n.project_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug IN ('ticket','task')`, q.TicketID).Scan(&fields, &project); err != nil {
			return WorkResolution{}, err
		}
		placement := modelprefs.PlacementFields(fields)
		q.Area = placement.Area
		q.Complexity = placement.Complexity
		q.ComplexitySource = placement.ComplexitySource
		q.TicketRole = placement.RouteRole
		q.TicketResidency = modelprefs.Strictest(q.TicketResidency, placement.Residency)
		if q.Role == "" {
			q.Role = q.TicketRole
		}
		if project != nil {
			q.ProjectID = *project
		}
	}
	q.Role = strings.TrimSpace(q.Role)
	if q.PersonID == nil {
		q.PersonID = modelprefs.PrefsPerson(ctx, tx, p)
	}
	if q.Role == "review-gate" || q.Role == "review-gate-security" {
		if q.AuthorFamily == "" {
			return WorkResolution{}, fail(400, "review-gate requires author_family")
		}
		route, err := ResolveReviewFor(ctx, tx, p, q, now)
		if err != nil {
			return WorkResolution{}, err
		}
		out := WorkResolution{Resolution: Resolution{Role: route.Role, AuthorFamily: q.AuthorFamily, Profile: route.Profile, Ladder: route.Ladder,
			OwnerRequired: route.OwnerRequired, Source: "aeon"}, Residency: route.Residency, Trace: route.Trace}
		if route.Profile != nil {
			out.CommandTemplate, err = commandTemplate(route.Profile.Harness, route.Profile.Model, route.Profile.Effort, true)
		}
		return out, err
	}
	role, ok := roleByName(q.Role)
	if !ok {
		return WorkResolution{}, fail(400, "unknown model role")
	}
	chain, trace, requirement, err := placementTrace(ctx, tx, q)
	if err != nil {
		return WorkResolution{}, err
	}
	out := WorkResolution{Residency: requirement, Trace: trace}
	out.Resolution = Resolution{Role: q.Role, Ladder: []Candidate{}, Source: "aeon"}
	if q.Harness != "" && !validHarness(q.Harness) {
		return out, fail(400, "unsupported model harness")
	}
	if q.Role == "scout" || q.Role == "mechanical" {
		out.Trace.Prefs = "not applied: role"
	} else {
		kind, fallback, err := modelprefs.LookupKind(ctx, tx, q.Area, q.ProjectID)
		if err != nil {
			return out, err
		}
		out.Trace.Kind = kind.Slug
		out.Trace.KindSource = "ticket"
		if fallback {
			out.Trace.KindSource = "fallback"
		}
		result := modelprefs.ResolveCell(chain, kind.Slug, out.Trace.Bucket)
		traceCell(&out.Trace, result)
		if result.Cell != nil && result.Cell.Mode != "auto" {
			profiles, err := listProfiles(ctx, tx)
			if err != nil {
				return out, err
			}
			profiles = expandPreference(*result.Cell, profiles)
			if len(profiles) == 0 {
				out.Trace.Fallback = "preferred profile unavailable"
			}
			health, err := agentaccounts.HarnessHealthAt(ctx, tx, now)
			if err != nil {
				return out, err
			}
			for _, profile := range profiles {
				step, err := preferenceStep(ctx, tx, profile, q.Role)
				if err != nil {
					return out, err
				}
				reasons := skipReasons(step, role, resolveQuery{Role: q.Role, Harness: q.Harness}, now, health)
				ids, err := agentaccounts.QualifyingAccountIDs(ctx, tx, profile.ID, profile.Harness, q.ProjectID, requirement, now)
				if err != nil {
					return out, err
				}
				if len(ids) == 0 {
					if requirement != "any" {
						reasons = append(reasons, "not allowed: "+requirement)
					} else {
						reasons = append(reasons, "no available account allows profile")
					}
				}
				candidate := Candidate{ProfileID: profile.ID, SkipReasons: reasons}
				if len(reasons) == 0 {
					candidate.Selected = true
					out.Trace.Fallback = ""
					out.Trace.PreferredCandidates = append(out.Trace.PreferredCandidates, candidate)
					out.Profile = &profile
					out.OwnerRequired = false
					for i := range out.Ladder {
						out.Ladder[i].Selected = false
					}
					out.Trace.QualifyingAccountIDs = ids
					if result.Cell.Mode == "latest" {
						out.Trace.LatestResolvedTo = profile.ID
					}
					out.CommandTemplate, err = commandTemplate(profile.Harness, profile.Model, profile.Effort, role.readOnly)
					return out, err
				}
				out.Trace.PreferredCandidates = append(out.Trace.PreferredCandidates, candidate)
				out.Trace.Fallback = strings.Join(reasons, "; ")
			}
		}
	}
	out.Resolution, err = resolveRole(ctx, tx, resolveQuery{Role: q.Role, Harness: q.Harness}, now)
	if err != nil {
		return out, err
	}
	// H4 is a no-op for any so historical resolutions do not gain account gates.
	if requirement == "any" {
		return out, nil
	}
	steps, err := loadLadder(ctx, tx, q.Role)
	if err != nil {
		return out, err
	}
	out.Profile = nil
	out.CommandTemplate = ""
	for i := range out.Ladder {
		c := &out.Ladder[i]
		c.Selected = false
		profile := steps[i].Profile
		ids, err := agentaccounts.QualifyingAccountIDs(ctx, tx, profile.ID, profile.Harness, q.ProjectID, requirement, now)
		if err != nil {
			return out, err
		}
		if len(ids) == 0 {
			c.SkipReasons = append(c.SkipReasons, "not allowed: "+requirement)
		}
		if len(c.SkipReasons) == 0 && out.Profile == nil {
			c.Selected = true
			out.Profile = &profile
			out.Trace.QualifyingAccountIDs = ids
			out.CommandTemplate, err = commandTemplate(profile.Harness, profile.Model, profile.Effort, role.readOnly)
			if err != nil {
				return out, err
			}
		}
	}
	if out.Profile == nil {
		out.OwnerRequired = true
		out.Trace.Blocked = "no " + requirement + " model route"
	}
	return out, nil
}
func preferenceStep(ctx context.Context, tx pgx.Tx, profile Profile, role string) (ladderStep, error) {
	step := ladderStep{Profile: profile, Route: Route{Role: role, ProfileID: profile.ID, State: "available"}}
	err := tx.QueryRow(ctx, `SELECT coalesce(r.state,'available'),coalesce(r.reason,''),r.valid_until,
  EXISTS(SELECT 1 FROM model_profile_retirements x WHERE x.profile_id=p.id)
  FROM model_profiles p LEFT JOIN model_role_routes r ON r.tenant_id=p.tenant_id AND r.profile_id=p.id AND r.role=$2
  WHERE p.id=$1::uuid`, profile.ID, role).Scan(&step.State, &step.Reason, &step.ValidUntil, &step.Retired)
	if err != nil {
		return step, fmt.Errorf("preference policy: %w", err)
	}
	return step, nil
}
