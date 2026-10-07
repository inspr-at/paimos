// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// PreferenceDecision is the public, grouped explanation for placement-aware
// resolution. The pre-existing trace remains its own backward-compatible DTO.
type PreferenceDecision struct {
	Kind struct {
		Slug   string `json:"slug"`
		Source string `json:"source"`
	} `json:"kind"`
	Complexity struct {
		Value  string `json:"value"`
		Bucket string `json:"bucket"`
		Source string `json:"source"`
	} `json:"complexity"`
	Role      string  `json:"role"`
	PersonID  *string `json:"person_id"`
	ProjectID *string `json:"project_id"`
	Cell      struct {
		SetBy    string          `json:"set_by"`
		LockedBy string          `json:"locked_by,omitempty"`
		Selector modelprefs.Cell `json:"selector"`
	} `json:"cell"`
	Residency struct {
		modelprefs.ResidencyResult
		TicketRequirement    string   `json:"ticket_requirement"`
		QualifyingAccountIDs []string `json:"qualifying_account_ids"`
	} `json:"residency"`
	LatestResolvedTo string            `json:"latest_resolved_to,omitempty"`
	Fallback         *PreferenceReason `json:"fallback"`
	Blocked          *PreferenceReason `json:"blocked"`
	HardRules        []string          `json:"hard_rules"`
}
type PreferenceReason struct {
	Reason string `json:"reason"`
}

func decisionTrace(trace PreferenceTrace) PreferenceDecision {
	var out PreferenceDecision
	out.Kind.Slug, out.Kind.Source = trace.Kind, trace.KindSource
	out.Complexity.Value, out.Complexity.Bucket, out.Complexity.Source = trace.Complexity, trace.Bucket, trace.ComplexitySource
	out.Role, out.PersonID = trace.Role, trace.PersonID
	if trace.ProjectID != "" {
		out.ProjectID = &trace.ProjectID
	}
	out.Cell.SetBy, out.Cell.LockedBy = trace.SetBy, trace.LockedBy
	out.Cell.Selector = modelprefs.Cell{Mode: "auto"}
	if trace.Selector != nil {
		out.Cell.Selector = *trace.Selector
	}
	out.Residency.ResidencyResult, out.Residency.TicketRequirement = trace.Residency, trace.TicketRequirement
	out.Residency.QualifyingAccountIDs = trace.QualifyingAccountIDs
	out.LatestResolvedTo = trace.LatestResolvedTo
	if trace.Fallback != "" {
		out.Fallback = &PreferenceReason{trace.Fallback}
	}
	if trace.Blocked != "" {
		out.Blocked = &PreferenceReason{trace.Blocked}
	}
	out.HardRules = append([]string{}, trace.Hard...)
	return out
}

func (m *Module) resolvePreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	params := r.URL.Query()
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	requestedPerson := params.Get("person_id")
	q := WorkQuery{Role: strings.TrimSpace(params.Get("role")), AuthorFamily: params.Get("author_family"), Harness: params.Get("harness"), Area: params.Get("area"), Complexity: params.Get("complexity"), ProjectID: project}
	q.Situation = params.Get("situation")
	if q.Situation != "" && !validBoardSituation(q.Situation) {
		writePreferenceError(w, prefFail(400, "invalid_situation"))
		return
	}
	if params.Has("fix_round") {
		q.FixRound, err = strconv.Atoi(params.Get("fix_round"))
		if err != nil || q.FixRound < 0 || q.FixRound > 1000 {
			writePreferenceError(w, prefFail(400, "invalid_fix_round"))
			return
		}
	}
	if params.Has("estimate_hours") {
		q.EstimateHours, err = strconv.ParseFloat(params.Get("estimate_hours"), 64)
		if err != nil || q.EstimateHours < 0 || q.EstimateHours > 100000 || math.IsNaN(q.EstimateHours) || math.IsInf(q.EstimateHours, 0) {
			writePreferenceError(w, prefFail(400, "invalid_estimate"))
			return
		}
	}
	if params.Has("concept") {
		q.Concept, err = strconv.ParseBool(params.Get("concept"))
		if err != nil {
			writePreferenceError(w, prefFail(400, "invalid_concept"))
			return
		}
	}
	q.PreviousFamily, err = NormalizeAuthorFamily(params.Get("previous_family"))
	if err != nil {
		writePreferenceError(w, prefFail(400, "invalid_previous_family"))
		return
	}
	ticket := strings.TrimSpace(params.Get("ticket"))
	if len(ticket) > 80 || params.Has("ticket") && ticket == "" || len(q.Area) > 48 || len(q.Role) > 32 || len(q.AuthorFamily) > 32 || len(q.Harness) > 32 || q.Complexity != "" && q.Complexity != "S" && q.Complexity != "M" && q.Complexity != "L" {
		writePreferenceError(w, prefFail(400, "invalid_placement"))
		return
	}
	if params.Has("person_id") && !uuidRE.MatchString(requestedPerson) {
		writePreferenceError(w, prefFail(400, "invalid_placement"))
		return
	}
	q.AuthorFamily, err = NormalizeAuthorFamily(q.AuthorFamily)
	if err != nil {
		writePreferenceError(w, fail(400, err.Error()))
		return
	}
	if q.Harness != "" && !validHarness(q.Harness) {
		writePreferenceError(w, fail(400, "unsupported model harness"))
		return
	}
	if q.Role != "" || ticket == "" {
		if err := validatePlacementQuery(q); err != nil {
			writePreferenceError(w, err)
			return
		}
	}
	var out struct {
		WorkResolution
		Preference PreferenceDecision `json:"preference"`
	}
	if err := PrepareCatalog(r.Context(), m.pool, p, CatalogPreparation{Operation: CatalogRead, Request: r, Authorize: func(ctx context.Context, tx pgx.Tx, current tenant.Principal) (bool, error) {
		_, err := authorizedPlacement(ctx, tx, current, q, ticket, project, requestedPerson, params.Has("person_id"))
		return err == nil, err
	}}); err != nil {
		writePreferenceError(w, err)
		return
	}
	err = m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		current, err := currentModelReader(r, tx, p)
		if err != nil {
			return err
		}
		q, err := authorizedPlacement(ctx, tx, current, q, ticket, project, requestedPerson, params.Has("person_id"))
		if err != nil {
			return err
		}
		if err := requireCatalog(ctx, tx); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		out.WorkResolution, err = ResolveWork(ctx, tx, current, q, now)
		if err != nil {
			return err
		}
		if out.Trace.Kind == "" {
			kind, fallback, err := modelprefs.LookupKind(ctx, tx, q.Area, q.ProjectID)
			if err != nil {
				return err
			}
			out.Trace.Kind = kind.Slug
			out.Trace.KindSource = "ticket"
			if fallback {
				out.Trace.KindSource = "fallback"
			}
		}
		out.Trace.Hard = append(out.Trace.Hard, "residency")
		if q.Role == "review-gate" || q.Role == "review-gate-security" {
			out.Trace.Hard = append(out.Trace.Hard, "cross_family", "review_qualification")
		}
		out.Trace.Role = out.Role
		out.Trace.ProjectID = q.ProjectID
		out.Trace.TicketRequirement = modelprefs.NormalizeResidency(q.TicketResidency)
		out.Preference = decisionTrace(out.Trace)
		return nil
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// Resolve the current target, placement and canonical caller under preparation's
// tenant/tree fences, then repeat the same checks in the read snapshot. No cached
// ticket project or key creator chooses the preparation scope.
func authorizedPlacement(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, ticket, project, requestedPerson string, personSupplied bool) (WorkQuery, error) {
	var err error
	q.PersonID, err = currentPreferencePerson(ctx, tx, p)
	if err != nil {
		return q, err
	}
	if personSupplied {
		if !uuidRE.MatchString(requestedPerson) {
			return q, prefFail(400, "invalid_placement")
		}
		person, err := modelprefs.CanonicalPerson(ctx, tx, requestedPerson)
		if err != nil {
			return q, err
		}
		if q.PersonID == nil || person == nil || *q.PersonID != *person {
			return q, prefFail(403, "person_not_caller")
		}
	}
	if ticket != "" {
		var fields []byte
		var ticketProject *string
		if uuidRE.MatchString(ticket) {
			err = tx.QueryRow(ctx, `SELECT n.id::text,n.fields,n.project_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug IN ('work','ticket','task')`, ticket).Scan(&q.TicketID, &fields, &ticketProject)
		} else {
			err = tx.QueryRow(ctx, `SELECT n.id::text,n.fields,n.project_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.deleted_at IS NULL AND k.slug IN ('work','ticket','task') AND (n.key=$1 OR n.id=(SELECT node_id FROM node_key_aliases WHERE key=$1 LIMIT 1)) ORDER BY (n.key=$1) DESC LIMIT 1`, ticket).Scan(&q.TicketID, &fields, &ticketProject)
		}
		if err != nil {
			return q, err
		}
		q.ProjectID = ""
		if ticketProject != nil {
			q.ProjectID = *ticketProject
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: q.ProjectID}); err != nil {
			return q, err
		}
		if project != "" && project != q.ProjectID {
			return q, prefFail(400, "ticket_project_mismatch")
		}
		placement := modelprefs.PlacementFields(fields)
		q.Area = placement.Area
		q.Complexity = placement.Complexity
		q.ComplexitySource = placement.ComplexitySource
		q.TicketRole = placement.RouteRole
		q.TicketResidency = placement.Residency
		if q.Role == "" {
			q.Role = q.TicketRole
		}
	}
	if err := readableProject(ctx, tx, p, q.ProjectID); err != nil {
		return q, err
	}

	return q, validatePlacementQuery(q)
}

// Security review has the same input requirements as ordinary review, but its
// owner-required outcome belongs to ResolveWork rather than the legacy ladder.
func validatePlacementQuery(q WorkQuery) error {
	role := q.Role
	if role == "review-gate-security" {
		role = "review-gate"
	}
	_, err := validateResolveQuery(resolveQuery{Role: role, AuthorFamily: q.AuthorFamily, Harness: q.Harness})
	return err
}
