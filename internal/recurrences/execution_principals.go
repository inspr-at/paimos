// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type executionPrincipal struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type executionPrincipalPage struct {
	Items      []executionPrincipal `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

func (m *Module) listExecutionPrincipals(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if !modelregistry.InteractivePerson(p) || r.Header.Get("Authorization") != "" {
		reply(w, 0, nil, workorders.Fail(403, "interactive_person_required"))
		return
	}
	after := r.URL.Query().Get("after")
	if after != "" && !workorders.UUID(after) {
		reply(w, 0, nil, workorders.Fail(400, "invalid_execution_principal_cursor"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := executionPrincipalPage{Items: []executionPrincipal{}}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		// Keep the saved definition and its identity checks under the same
		// tenant/tree access fence as consent, without writing any record.
		if _, err := lock(ctx, tx, p.TenantID, false); err != nil {
			return err
		}
		item, err := load(ctx, tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err := manageDefinition(ctx, tx, p, item.Input); err != nil {
			return err
		}
		person, err := canonicalPerson(ctx, tx, p)
		if err != nil {
			return err
		}
		if item.Definition == nil || item.Definition.Assignment == nil {
			return workorders.Fail(409, "ticket_only_routine")
		}
		if person != item.Definition.OwnerPrincipalID {
			return authz.ErrForbidden
		}
		rows, err := tx.Query(ctx, `SELECT id::text,kind,name FROM principals
 WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT 101`, p.TenantID, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		candidates := make([]executionPrincipal, 0, 101)
		for rows.Next() {
			var candidate executionPrincipal
			if err := rows.Scan(&candidate.ID, &candidate.Kind, &candidate.Label); err != nil {
				return err
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if len(candidates) > 100 {
			id := candidates[99].ID
			out.NextCursor = &id
			candidates = candidates[:100]
		}
		// Bound the candidates, not just the successes: an arbitrarily long
		// run of ineligible principals must not create unbounded DB work.
		// The cursor advances over checked candidates, including empty pages.
		for _, candidate := range candidates {
			if err := executionIdentityTx(ctx, tx, p.TenantID, item.Input, candidate.ID); err != nil {
				if errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return err
			}
			out.Items = append(out.Items, candidate)
		}
		return nil
	})
	reply(w, http.StatusOK, out, err)
}
