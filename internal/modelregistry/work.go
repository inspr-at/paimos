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
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type WorkQuery struct {
	Escalation       bool
	ReviewPolicies   []reviewgate.FamilyPolicy
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
	Situation        string
	Labels           []string
	EstimateHours    float64
	FixRound         int
	PreviousFamily   string
	Concept          bool
	Queued           bool
	// Column and OffHarnesses are supplied by shadow round routing only.
	Column       string
	OffHarnesses []string
	// ExplicitBoard is retained for placement callers during the S2 transition;
	// all live choices now use Models regardless of adoption revision.
	WorkspaceOnly bool
	ExplicitBoard bool
}
type PreferenceTrace struct {
	PreferenceOf         *preferenceOwner           `json:"preference_of,omitempty"`
	Column               string                     `json:"column,omitempty"`
	Situation            string                     `json:"situation,omitempty"`
	CardIndex            int                        `json:"card_index,omitempty"`
	Lock                 *modelprefs.BoardLock      `json:"lock,omitempty"`
	Held                 []modelprefs.HeldLine      `json:"held,omitempty"`
	OrderMode            string                     `json:"order_mode,omitempty"`
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

// ResolveWork routes every role through Models and the same live account policy.
func ResolveWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time) (WorkResolution, error) {
	return resolveDailyWork(ctx, tx, p, q, now)
}

func resolveWorkWithCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time, catalog *preferencePreviewCatalog) (WorkResolution, error) {
	if q.TicketID != "" {
		var fields []byte
		var project *string
		if err := tx.QueryRow(ctx, `SELECT n.fields,n.project_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug IN ('work','ticket','task')`, q.TicketID).Scan(&fields, &project); err != nil {
			return WorkResolution{}, err
		}
		placement := modelprefs.PlacementFields(fields)
		parsed, parseErr := boardTicketFields(ctx, tx, q, fields)
		if parseErr != nil {
			return WorkResolution{}, parseErr
		}
		q = parsed
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
	if q.Role != "review-gate" && q.Role != "review-gate-security" {
		if escalated, err := escalationForWork(ctx, tx, p, q, now); err != nil {
			return WorkResolution{}, err
		} else if escalated != nil {
			return *escalated, nil
		}
	}
	if strings.HasPrefix(q.Role, "review-gate") {
		var err error
		q.AuthorFamily, err = NormalizeAuthorFamily(q.AuthorFamily)
		if err != nil || q.AuthorFamily == "" {
			return WorkResolution{}, fail(400, "review-gate requires a known author_family")
		}
	}
	board, err := resolveBoardWork(ctx, tx, p, q, now, nil)
	if err != nil {
		return WorkResolution{}, err
	}
	return *board, nil
}

func preferenceStep(ctx context.Context, tx pgx.Tx, profile Profile, role string) (ladderStep, error) {
	step := ladderStep{Profile: profile, Route: Route{Role: role, ProfileID: profile.ID, State: "available"}}
	err := tx.QueryRow(ctx, `SELECT coalesce(r.state,'available'),coalesce(r.reason,''),r.valid_until,
  EXISTS(SELECT 1 FROM model_profile_retirements x WHERE x.tenant_id=p.tenant_id AND x.profile_id=p.id AND x.retire_at IS NULL),
 (SELECT x.retire_at FROM model_profile_retirements x WHERE x.tenant_id=p.tenant_id AND x.profile_id=p.id),
  o.suppressed_until
  FROM model_profiles p LEFT JOIN (`+agentaccounts.ModelRoleRoutesSQL+`) r ON r.tenant_id=p.tenant_id AND r.profile_id=p.id AND r.role=$2
  LEFT JOIN model_observations o ON o.tenant_id=p.tenant_id AND o.harness=p.harness AND o.model=p.model AND o.effort=p.effort
  WHERE p.id=$1::uuid`, profile.ID, role).Scan(&step.State, &step.Reason, &step.ValidUntil, &step.Retired, &step.RetireAt, &step.SuppressedUntil)
	if err != nil {
		return step, fmt.Errorf("preference policy: %w", err)
	}
	return step, nil
}
