// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type agentsPlanSnapshot = agentplan.Snapshot

func (m *Module) getAgentsPlan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if p.Kind == tenant.Agent {
		// Neither a broad coordinator ceiling nor a supplied owner ID is
		// enough. This read is explicitly delegated by the key's live creator.
		hasScope := false
		for _, scope := range p.Scopes {
			hasScope = hasScope || strings.ReplaceAll(scope, ":", ".") == agentplan.ReadScope
		}
		if !hasScope {
			httpapi.WriteError(w, http.StatusForbidden, "agents.plan.read scope missing")
			return
		}
		if p.KeyCreatorID == "" {
			httpapi.WriteError(w, http.StatusForbidden, "key has no person owner — adopt it in Settings › Keys")
			return
		}
	} else if p.Kind != tenant.Person {
		httpapi.WriteError(w, http.StatusForbidden, "person plan required")
		return
	}
	out := agentsPlanSnapshot{Running: map[string]int{}}
	// The plan's total spans projects. This narrowly scoped aggregate is
	// authorized by person ownership, not project visibility. Keep explicit
	// tenant and owner predicates; return no project/session identities.
	ctx := db.AllProjects(r.Context(), "agents plan: authorized person's running count only")
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = agentplan.ReadTx(ctx, tx, p)
		return err
	})
	if errors.Is(err, authz.ErrForbidden) {
		httpapi.WriteError(w, http.StatusForbidden, "permission denied")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "plan read failed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
