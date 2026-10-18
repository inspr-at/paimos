// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ResolveRound uses the same board as dispatch, with the queued round's
// situation instead of the ticket's historical escalation episode. Its caller
// has already checked ticket/project visibility and the live plan authority.
// It neither prepares preferences nor reserves accounts or claims runs.
func ResolveRound(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, kind string, designReady bool, now time.Time) (WorkResolution, bool, error) {
	var fields []byte
	var project string
	if err := tx.QueryRow(ctx, `SELECT n.fields,n.project_id::text FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=$1::uuid AND n.project_id=$2::uuid AND n.deleted_at IS NULL
 AND k.slug IN ('work','ticket','task')`, q.TicketID, q.ProjectID).Scan(&fields, &project); err != nil {
		return WorkResolution{}, false, err
	}
	round := q.FixRound
	parsed, err := boardTicketFields(ctx, tx, q, fields)
	if err != nil {
		return WorkResolution{}, false, err
	}
	q = parsed
	q.FixRound = round
	f := modelprefs.PlacementFields(fields)
	q.Area, q.Complexity, q.ComplexitySource, q.TicketResidency = f.Area, f.Complexity, f.ComplexitySource, f.Residency
	q.ProjectID, q.TicketRole = project, f.RouteRole
	q.Role = "build"
	q.Situation = "first"
	designFirst := kind == "first_build" && q.Area == "frontend" && !designReady
	switch kind {
	case "first_build":
	case "fix":
		// Empty situation lets the board apply the person's situation threshold.
		q.Situation = ""
	case "merge", "land":
		q.Situation = "fix"
	case "review":
		q.Role = "review-gate"
	case "design":
		q.Column = "design"
	default:
		return WorkResolution{}, false, fmt.Errorf("unknown round kind")
	}
	if designFirst {
		q.Column = "design"
	}
	if q.PersonID == nil {
		q.PersonID = modelprefs.PrefsPerson(ctx, tx, p)
	}
	resolved, err := resolveBoardWork(ctx, tx, p, q, now, nil)
	if err != nil {
		return WorkResolution{}, designFirst, err
	}
	if resolved == nil {
		return WorkResolution{}, designFirst, fmt.Errorf("model board has not been initialized")
	}
	resolved.Trace.Role, resolved.Trace.ProjectID = q.Role, q.ProjectID
	resolved.Trace.TicketRequirement = modelprefs.NormalizeResidency(q.TicketResidency)
	return *resolved, designFirst, nil
}
