// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// FeedbackRecipient is the person who receives feedback sent from the app's
// help menu: a workspace owner other than the caller (AEON-312). The web sends
// the feedback itself through POST /api/inbox/messages.
type FeedbackRecipient struct {
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name"`
}

// handleFeedbackRecipient answers the longest-standing active owner who is not
// the caller, or 404 when there is none (the caller is the only owner).
func (m *module) handleFeedbackRecipient(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var out FeedbackRecipient
	err := db.InTenant(tenant.WithPrincipal(r.Context(), p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT p.id::text, p.name FROM role_bindings b
			JOIN principals p ON p.tenant_id=b.tenant_id AND p.id=b.principal_id
			JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
			WHERE b.scope_type='workspace' AND r.key='owner' AND p.kind='person'
			  AND p.status='active' AND p.linked_to IS NULL AND p.id<>$1::uuid
			ORDER BY b.created_at, p.id LIMIT 1`, p.ID).Scan(&out.PrincipalID, &out.Name)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, errNotFound)
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
