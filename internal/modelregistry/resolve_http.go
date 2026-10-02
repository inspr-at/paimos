// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

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
	if params.Has("person_id") && !uuidRE.MatchString(requestedPerson) {
		writePreferenceError(w, prefFail(403, "person_not_caller"))
		return
	}
	q := WorkQuery{Role: strings.TrimSpace(params.Get("role")), AuthorFamily: params.Get("author_family"), Harness: params.Get("harness"), Area: params.Get("area"), Complexity: params.Get("complexity"), ProjectID: project}
	ticket := strings.TrimSpace(params.Get("ticket"))
	if len(ticket) > 80 || params.Has("ticket") && ticket == "" || len(q.Area) > 48 || len(q.Role) > 32 || len(q.AuthorFamily) > 32 || len(q.Harness) > 32 || q.Complexity != "" && q.Complexity != "S" && q.Complexity != "M" && q.Complexity != "L" {
		writePreferenceError(w, prefFail(400, "invalid_placement"))
		return
	}
	var out struct {
		WorkResolution
		Preference PreferenceTrace `json:"preference"`
	}
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := authz.RequireTx(ctx, tx, p, "models.read", authz.Scope{}); err != nil {
			return err
		}
		q.PersonID = modelprefs.PrefsPerson(ctx, tx, p)
		if params.Has("person_id") {
			person, err := modelprefs.CanonicalPerson(ctx, tx, requestedPerson)
			if err != nil {
				return err
			}
			if q.PersonID == nil || person == nil || *q.PersonID != *person {
				return prefFail(403, "person_not_caller")
			}
		}
		if ticket != "" {
			var fields []byte
			var ticketProject *string
			if uuidRE.MatchString(ticket) {
				err = tx.QueryRow(ctx, `SELECT n.id::text,n.fields,n.project_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug IN ('ticket','task')`, ticket).Scan(&q.TicketID, &fields, &ticketProject)
			} else {
				err = tx.QueryRow(ctx, `SELECT n.id::text,n.fields,n.project_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.deleted_at IS NULL AND k.slug IN ('ticket','task') AND (n.key=$1 OR n.id=(SELECT node_id FROM node_key_aliases WHERE key=$1 LIMIT 1)) ORDER BY (n.key=$1) DESC LIMIT 1`, ticket).Scan(&q.TicketID, &fields, &ticketProject)
			}
			if err != nil {
				return err
			}
			q.ProjectID = ""
			if ticketProject != nil {
				q.ProjectID = *ticketProject
			}
			if project != "" && project != q.ProjectID {
				return prefFail(400, "ticket_project_mismatch")
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
			if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: q.ProjectID}); err != nil {
				return err
			}
		}
		if err := readableProject(ctx, tx, p, q.ProjectID); err != nil {
			return err
		}
		if err := ensureCatalog(ctx, tx, p); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		q.AuthorFamily, err = NormalizeAuthorFamily(q.AuthorFamily)
		if err != nil {
			return fail(400, err.Error())
		}
		out.WorkResolution, err = ResolveWork(ctx, tx, p, q, now)
		if err != nil {
			return err
		}
		out.Trace.Role = out.Role
		out.Trace.ProjectID = q.ProjectID
		out.Trace.TicketRequirement = modelprefs.NormalizeResidency(q.TicketResidency)
		out.Preference = out.Trace
		return nil
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
