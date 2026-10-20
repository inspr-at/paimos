// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Inventory projections deliberately contain no key, proof or daemon secrets.
// Evidence is provenance, never a selected owner or an authorization grant.
type ComputerInventory struct {
	Computers  []ComputerEvidence `json:"computers"`
	NextCursor *string            `json:"next_cursor"`
}

type ComputerEvidence struct {
	ComputerID           string                `json:"computer_id"`
	ComputerName         string                `json:"computer_name"`
	State                string                `json:"state"`
	Owner                string                `json:"owner"`
	ApprovedBy           *string               `json:"approved_by"`
	WorkstationOwners    []WorkstationEvidence `json:"workstation_owners"`
	Enrollments          []EnrollmentEvidence  `json:"enrollments"`
	EnrollmentsTruncated bool                  `json:"enrollments_truncated"`
}

type WorkstationEvidence struct {
	PersonID             *string `json:"person_id"`
	CreatedByPrincipalID *string `json:"created_by_principal_id"`
}

type EnrollmentEvidence struct {
	AccountID     string  `json:"account_id"`
	State         string  `json:"state"`
	OwnerPersonID *string `json:"owner_person_id"`
	ApprovedBy    *string `json:"approved_by"`
}

func (m *Module) inventory(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			WriteError(w, fail(400, "invalid", "limit must be between 1 and 100"))
			return
		}
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && (len(cursor) != 36 || !uuidRE.MatchString(cursor)) {
		WriteError(w, fail(400, "invalid", "cursor must be a computer UUID"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out := ComputerInventory{Computers: []ComputerEvidence{}}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return fail(403, "forbidden", "signed-in person permission required")
			}
			return err
		}
		rows, err := tx.Query(ctx, `SELECT c.id::text,left(coalesce(nullif(c.display_name,''),q.details->>'computer_name',''),200),c.state,q.approved_by::text
		 FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
		 WHERE c.tenant_id=$1 AND ($2::uuid IS NULL OR c.id>$2::uuid)
		 ORDER BY c.id LIMIT $3`, p.TenantID, nullableCursor(cursor), limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			v := ComputerEvidence{Owner: "unconfirmed", WorkstationOwners: []WorkstationEvidence{}, Enrollments: []EnrollmentEvidence{}}
			if err = rows.Scan(&v.ComputerID, &v.ComputerName, &v.State, &v.ApprovedBy); err != nil {
				rows.Close()
				return err
			}
			out.Computers = append(out.Computers, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Computers) > limit {
			out.Computers = out.Computers[:limit]
			last := out.Computers[limit-1].ComputerID
			out.NextCursor = &last
		}
		for i := range out.Computers {
			v := &out.Computers[i]
			// The existing partial unique index permits at most one live marked
			// workstation key per tenant. Read only its person provenance.
			rows, err = tx.Query(ctx, `SELECT person_id::text,created_by_principal_id::text FROM agent_keys
			 WHERE tenant_id=$1 AND workstation_computer_id=$2 AND owner_workstation AND revoked_at IS NULL LIMIT 1`, p.TenantID, v.ComputerID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var e WorkstationEvidence
				if err = rows.Scan(&e.PersonID, &e.CreatedByPrincipalID); err != nil {
					rows.Close()
					return err
				}
				v.WorkstationOwners = append(v.WorkstationOwners, e)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			rows, err = tx.Query(ctx, `SELECT e.account_id::text,e.state,a.owner_person_id::text,q.approved_by::text
			 FROM agent_pairing_enrollments e JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
			 JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
			 WHERE e.tenant_id=$1 AND e.computer_id=$2 ORDER BY e.account_id LIMIT 101`, p.TenantID, v.ComputerID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var e EnrollmentEvidence
				if err = rows.Scan(&e.AccountID, &e.State, &e.OwnerPersonID, &e.ApprovedBy); err != nil {
					rows.Close()
					return err
				}
				v.Enrollments = append(v.Enrollments, e)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			v.classifyEvidence()
			if len(v.Enrollments) > 100 {
				v.Enrollments = v.Enrollments[:100]
				v.EnrollmentsTruncated = true
			}
		}
		return nil
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

func nullableCursor(cursor string) any {
	if cursor == "" {
		return nil
	}
	return cursor
}

func (v *ComputerEvidence) classifyEvidence() {
	people := map[string]bool{}
	add := func(id *string) {
		if id != nil {
			people[*id] = true
		}
	}
	add(v.ApprovedBy)
	for _, e := range v.WorkstationOwners {
		add(e.PersonID)
		add(e.CreatedByPrincipalID)
	}
	for _, e := range v.Enrollments {
		add(e.OwnerPersonID)
		add(e.ApprovedBy)
	}
	if len(people) > 1 {
		v.Owner = "ambiguous"
	}
}
