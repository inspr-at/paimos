// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// PreviewSessionLease captures only the session's one-way database identifier,
// never the cookie or an API key. A preview cannot renew the login session.
// Each use rechecks expiry/logout and loads the currently active principal;
// the attachment service separately rechecks current resource permissions.
func (m *Module) PreviewSessionLease(r *http.Request) (func(context.Context) (tenant.Principal, error), error) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Person || r.Header.Get("Authorization") != "" {
		return nil, pgx.ErrNoRows
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, pgx.ErrNoRows
	}
	raw, err := decodeSessionToken(c.Value)
	if err != nil {
		return nil, pgx.ErrNoRows
	}
	id := sessionID(raw)
	return func(ctx context.Context) (tenant.Principal, error) {
		var current tenant.Principal
		err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
			var principalID string
			if err := tx.QueryRow(ctx, `SELECT principal_id::text FROM sessions
				WHERE tenant_id=$1::uuid AND id=$2 AND principal_id=$3::uuid AND expires_at>now()`, p.TenantID, id, p.ID).Scan(&principalID); err != nil {
				return err
			}
			var err error
			current, err = displayPrincipal(ctx, tx, p.TenantID, principalID)
			return err
		})
		return current, err
	}, nil
}
