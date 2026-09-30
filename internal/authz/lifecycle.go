// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

func (m *Module) deactivate(w http.ResponseWriter, r *http.Request) {
	m.setStatus(w, r, "deactivated")
}

func (m *Module) reactivate(w http.ResponseWriter, r *http.Request) {
	m.setStatus(w, r, "active")
}

func (m *Module) setStatus(w http.ResponseWriter, r *http.Request, next string) {
	p := actor(r)
	id := r.PathValue("principal_id")
	if !uuidPattern.MatchString(id) {
		apiFail(w, 400, "invalid", "principal_id", "Principal ID must be a UUID")
		return
	}
	var member Member
	var kind string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorizeMutation(r.Context(), tx, p, "members.manage", nil); err != nil {
			return err
		}
		var status string
		var legacy []string
		var linked *string
		if err := tx.QueryRow(r.Context(), `SELECT kind,status,roles,linked_to::text FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid FOR UPDATE`, p.TenantID, id).Scan(&kind, &status, &legacy, &linked); err != nil {
			return err
		}
		if linked != nil {
			return errAliasTarget
		}
		if kind == "agent" {
			for _, v := range legacy {
				if v == "system" || v == "importer" || v == "operator" || v == "embedding" || strings.HasPrefix(v, "quote_") {
					return ErrForbidden
				}
			}
		}
		want := "active"
		if next == "deactivated" {
			want = "deactivated"
		}
		if status == want {
			return errStatusUnchanged
		}
		if kind == "agent" && want == "deactivated" {
			// A connected computer's runtime identity goes with the computer:
			// deactivating it here would cut a live daemon off from its key.
			var connected bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_pairing_computers WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND state<>'revoked')`, p.TenantID, id).Scan(&connected); err != nil {
				return err
			}
			if connected {
				return errConnectedComputer
			}
		}
		if _, err := tx.Exec(r.Context(), `UPDATE principals SET status=$3 WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, id, want); err != nil {
			return err
		}
		typ := "principal.reactivated"
		if want == "deactivated" {
			typ = "principal.deactivated"
		}
		if err := appendEvent(r.Context(), tx, p, typ, map[string]any{"principal_id": id, "status": status}, map[string]any{"principal_id": id, "status": want}); err != nil {
			return err
		}
		var err error
		member, err = readMember(r.Context(), tx, p.TenantID, id)
		return err
	})
	if errors.Is(err, errAliasTarget) {
		apiFail(w, 409, "conflict", "principal_id", "Change the person this alias belongs to")
		return
	}
	if errors.Is(err, errConnectedComputer) {
		apiFail(w, 409, "connected_computer", "principal_id", "This is a connected computer. Disconnect it under Agents first.")
		return
	}
	if errors.Is(err, errStatusUnchanged) {
		who := "person"
		if kind == "agent" {
			who = "agent"
		}
		reason := "This " + who + " is already active"
		if next == "deactivated" {
			reason = "This " + who + " is already deactivated"
		}
		apiFail(w, 409, "conflict", "principal_id", reason)
		return
	}
	if lastOwnerViolation(err) {
		apiFail(w, 409, "last_owner", "principal_id", "The last active owner cannot be deactivated. Make another person an owner first.")
		return
	}
	if err != nil {
		internalFail(w, err)
		return
	}
	reply(w, http.StatusOK, member)
}

// RetireAgentTx deactivates an agent identity that no longer has a purpose, in
// the caller's transaction, the way the Access lifecycle does: status, event,
// and (by trigger) its sessions and keys. It reports whether anything changed;
// an identity that is not an active, plain agent is left alone. extra is merged
// into the event's after snapshot.
func RetireAgentTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, id string, extra map[string]any) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE tenant_id=$1::uuid AND id=$2::uuid AND kind='agent' AND status='active' AND linked_to IS NULL`, actor.TenantID, id)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	after := map[string]any{"principal_id": id, "status": "deactivated"}
	for k, v := range extra {
		after[k] = v
	}
	return true, appendEvent(ctx, tx, actor, "principal.deactivated", map[string]any{"principal_id": id, "status": "active"}, after)
}

var (
	errAliasTarget       = errors.New("alias target")
	errStatusUnchanged   = errors.New("status unchanged")
	errConnectedComputer = errors.New("connected computer")
)
