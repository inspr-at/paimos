// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/principallink/apply"
)

func (m *Module) linkAlias(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	id := r.PathValue("principal_id")
	var body struct {
		FromPrincipalID string `json:"from_principal_id"`
	}
	if !uuidPattern.MatchString(id) {
		apiFail(w, 400, "invalid", "principal_id", "Principal ID must be a UUID")
		return
	}
	if err := decode(r, &body); err != nil || !uuidPattern.MatchString(body.FromPrincipalID) {
		apiFail(w, 400, "invalid", "from_principal_id", "From principal ID must be a UUID")
		return
	}
	var member Member
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorizeMutation(r.Context(), tx, p, "members.manage", nil); err != nil {
			return err
		}
		out, err := apply.Apply(r.Context(), tx, p.TenantID, body.FromPrincipalID, id, p.ID)
		if err != nil {
			return err
		}
		if out.Changed {
			if err := appendEvent(r.Context(), tx, p, "principal.alias_linked", out.Before, out.After); err != nil {
				return err
			}
		}
		member, err = readMember(r.Context(), tx, p.TenantID, id)
		return err
	})
	if err != nil {
		writeAliasErr(w, err)
		return
	}
	reply(w, http.StatusOK, member)
}

func (m *Module) unlinkAlias(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	id := r.PathValue("principal_id")
	from := r.PathValue("from_principal_id")
	if !uuidPattern.MatchString(id) || !uuidPattern.MatchString(from) {
		apiFail(w, 400, "invalid", "from_principal_id", "Principal IDs must be UUIDs")
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorizeMutation(r.Context(), tx, p, "members.manage", nil); err != nil {
			return err
		}
		var linked *string
		if err := tx.QueryRow(r.Context(), `SELECT linked_to::text FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='person' FOR UPDATE`, p.TenantID, from).Scan(&linked); err != nil {
			return err
		}
		if linked == nil || *linked != id {
			return errNotAlias
		}
		out, err := apply.Apply(r.Context(), tx, p.TenantID, from, "", p.ID)
		if err != nil {
			return err
		}
		if !out.Changed {
			return nil
		}
		return appendEvent(r.Context(), tx, p, "principal.alias_unlinked", out.Before, out.After)
	})
	if errors.Is(err, errNotAlias) || errors.Is(err, pgx.ErrNoRows) {
		apiFail(w, 404, "not_found", "from_principal_id", "That alias is not linked to this person")
		return
	}
	if err != nil {
		writeAliasErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errNotAlias = errors.New("not an alias")

func writeAliasErr(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case lastOwnerViolation(err):
		apiFail(w, 409, "last_owner", "from_principal_id", "The last active owner cannot become an alias. Make another person an owner first.")
	case strings.Contains(msg, "person not found"):
		apiFail(w, 404, "not_found", "from_principal_id", "That person is not in this workspace")
	case strings.Contains(msg, "self-links"):
		apiFail(w, 400, "invalid", "from_principal_id", "An alias cannot point at itself, and a link cannot form a chain")
	case strings.Contains(msg, "already linked"):
		apiFail(w, 409, "conflict", "from_principal_id", "That person is already linked. Unlink them first.")
	case strings.Contains(msg, "ambiguous"):
		apiFail(w, 400, "invalid", "from_principal_id", "That name matches more than one person")
	default:
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23514" {
			apiFail(w, 409, "conflict", "from_principal_id", "That link is not allowed")
			return
		}
		internalFail(w, err)
	}
}
