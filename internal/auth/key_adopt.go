// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var errKeyOwned = errors.New("key already has a person owner")

func (m *Module) handleAdoptAgentKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w)
		return
	}
	if p.Kind != tenant.Person || p.ID == "" {
		writeForbidden(w)
		return
	}
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeBadRequest(w, "invalid id")
		return
	}
	key, err := m.adoptAgentKey(r.Context(), p, id)
	switch {
	case errors.Is(err, authz.ErrForbidden):
		writeForbidden(w)
	case errors.Is(err, errNotFound):
		writeJSON(w, http.StatusNotFound, errorJSON{Error: "key not found"})
	case errors.Is(err, errKeyOwned), errors.Is(err, errKeyInactive):
		writeJSON(w, http.StatusConflict, errorJSON{Error: err.Error()})
	case err != nil:
		writeInternal(w)
	default:
		writeJSON(w, http.StatusOK, keyJSON(key))
	}
}

func (m *Module) adoptAgentKey(ctx context.Context, p tenant.Principal, id string) (keyRecord, error) {
	var key keyRecord
	if p.Kind != tenant.Person || p.ID == "" {
		return key, authz.ErrForbidden
	}
	// Ownership changes share the key-use admission fence. Acquire it before
	// the tenant access fence, so no admitted request outlives adoption.
	ctx = db.WithKeyScopeUse(tenant.WithPrincipal(ctx, p), p.TenantID, id)
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID).Scan(&locked); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "keys.manage", authz.Scope{}); err != nil {
			return err
		}
		var err error
		key, err = lockAgentKey(ctx, tx, id)
		if err != nil {
			return err
		}
		if key.CreatedByPrincipalID != nil {
			return errKeyOwned
		}
		if key.RevokedAt != nil || key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
			return errKeyInactive
		}
		before := keySnapshot(key)
		changed, err := tx.Exec(ctx, `UPDATE agent_keys SET created_by_principal_id=$2::uuid, person_owner_required=true WHERE tenant_id=$3::uuid AND id=$1::uuid AND created_by_principal_id IS NULL`, id, p.ID, p.TenantID)
		if err != nil {
			return err
		}
		if changed.RowsAffected() != 1 {
			return errNotFound
		}
		key.CreatedByPrincipalID = &p.ID
		_, err = events.Append(ctx, tx, p, events.Change{Type: "agent_key.adopted", Before: before, After: keySnapshot(key)})
		return err
	})
	return key, err
}
