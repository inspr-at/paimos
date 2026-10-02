// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// WorkPlacement freezes the preference decision at work start, separately
// from the actual session profile. It is internal session metadata.
type WorkPlacement struct {
	PreferenceTrace
	PlannedProfileID *string `json:"planned_profile_id"`
}

func PlacementFor(ctx context.Context, tx pgx.Tx, p tenant.Principal, q WorkQuery, now time.Time) (WorkPlacement, error) {
	var out WorkPlacement
	if q.TicketID != "" {
		var fields []byte
		var project *string
		if err := tx.QueryRow(ctx, `SELECT fields,project_id::text FROM nodes WHERE id=$1::uuid AND deleted_at IS NULL`, q.TicketID).Scan(&fields, &project); err != nil {
			return out, err
		}
		f := modelprefs.PlacementFields(fields)
		q.Area, q.Complexity, q.ComplexitySource, q.TicketRole, q.TicketResidency = f.Area, f.Complexity, f.ComplexitySource, f.RouteRole, f.Residency
		if q.Role == "" {
			q.Role = f.RouteRole
		}
		if project != nil {
			q.ProjectID = *project
		}
	}
	q.Role = strings.TrimSpace(q.Role)
	if q.PersonID == nil {
		q.PersonID = modelprefs.PrefsPerson(ctx, tx, p)
	}
	if KnownRouteRole(q.Role) && !(strings.HasPrefix(q.Role, "review-gate") && q.AuthorFamily == "") {
		// Fields are already read in this transaction; avoid a second lookup.
		q.TicketID = ""
		resolved, err := ResolveWork(ctx, tx, tenant.Principal{}, q, now)
		if err != nil {
			return out, err
		}
		out.PreferenceTrace = resolved.Trace
		if resolved.Profile != nil {
			id := resolved.Profile.ID
			out.PlannedProfileID = &id
		}
	} else {
		_, trace, _, err := placementTrace(ctx, tx, q)
		if err != nil {
			return out, err
		}
		out.PreferenceTrace = trace
	}
	if out.Kind == "" {
		kind, fallback, err := modelprefs.LookupKind(ctx, tx, q.Area, q.ProjectID)
		if err != nil {
			return out, err
		}
		out.Kind, out.KindSource = kind.Slug, "ticket"
		if fallback {
			out.KindSource = "fallback"
		}
	}
	out.Role, out.ProjectID, out.TicketRequirement = q.Role, q.ProjectID, modelprefs.NormalizeResidency(q.TicketResidency)
	return out, nil
}

// DispatchPlacement finds the nearest ticket/task of a work order. A person
// preview never enters this path: q is keyed by the run's starter.
func DispatchPlacement(ctx context.Context, tx pgx.Tx, p tenant.Principal, order string, now time.Time) (*WorkPlacement, error) {
	var ticket *string
	err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (
 SELECT n.id,n.parent_id,k.slug,0 AS depth FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND n.deleted_at IS NULL
 UNION ALL SELECT n.id,n.parent_id,k.slug,up.depth+1 FROM up JOIN nodes n ON n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=up.parent_id
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE up.slug NOT IN ('ticket','task') AND up.depth<32 AND n.deleted_at IS NULL)
 SELECT (SELECT id::text FROM up WHERE slug IN ('ticket','task') ORDER BY depth LIMIT 1)`, order).Scan(&ticket)
	if err != nil || ticket == nil {
		return nil, err
	}
	out, err := PlacementFor(ctx, tx, p, WorkQuery{TicketID: *ticket}, now)
	return &out, err
}

func (p WorkPlacement) JSON() (json.RawMessage, error) { return json.Marshal(p) }
