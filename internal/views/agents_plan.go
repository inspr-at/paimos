// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type agentsPlanSnapshot struct {
	agentplan.Plan
	PrincipalID  string         `json:"principal_id"`
	Running      map[string]int `json:"running"`
	RunningTotal int            `json:"running_total"`
	Source       string         `json:"source"`
	UpdatedAt    *time.Time     `json:"updated_at"`
}

func (m *Module) getAgentsPlan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	owner := p.ID
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
		owner = p.KeyCreatorID
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
		canonicalOwner, err := agentsPlanOwner(ctx, tx, p.TenantID, owner)
		if err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, agentplan.ReadScope, authz.Scope{}); err != nil {
			return err
		}
		out.PrincipalID = canonicalOwner
		raw, at, err := readAgentsPlanPreference(ctx, tx, p.TenantID, canonicalOwner)
		if err != nil {
			return err
		}
		out.UpdatedAt = at
		out.Plan, out.Source, err = agentplan.Decode(raw)
		if err != nil {
			return err // Malformed stored plans fail closed, never fall back.
		}
		rows, err := tx.Query(ctx, `SELECT s.harness,count(*) FROM harness_sessions s
			WHERE s.tenant_id=$1::uuid AND (
			 (s.stopped_at IS NULL AND s.archived_at IS NULL AND s.phase<>'stopped')
			 OR (EXISTS(SELECT 1 FROM project_leads l WHERE l.tenant_id=s.tenant_id AND l.session_id=s.id)
			     AND NOT aeon_work_session_stopped(s.stopped_at,s.stop_reason)))
			AND (s.owner_principal_id IN (SELECT id FROM principals WHERE tenant_id=$1::uuid AND coalesce(linked_to,id)=$2::uuid)
			OR (s.owner_principal_id IS NULL AND EXISTS (
				SELECT 1 FROM agent_keys k JOIN principals creator ON creator.tenant_id=k.tenant_id AND creator.id=k.created_by_principal_id
				WHERE k.tenant_id=s.tenant_id AND k.principal_id=s.agent_principal_id AND creator.kind='person'
				AND coalesce(creator.linked_to,creator.id)=$2::uuid
			) AND NOT EXISTS (
				SELECT 1 FROM agent_keys k LEFT JOIN principals creator ON creator.tenant_id=k.tenant_id AND creator.id=k.created_by_principal_id
				WHERE k.tenant_id=s.tenant_id AND k.principal_id=s.agent_principal_id
				AND (creator.id IS NULL OR creator.kind<>'person' OR coalesce(creator.linked_to,creator.id)<>$2::uuid)
			))) GROUP BY s.harness`, p.TenantID, canonicalOwner)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var harness string
			var count int
			if err := rows.Scan(&harness, &count); err != nil {
				return err
			}
			out.Running[harness] = count
			out.RunningTotal += count
		}
		return rows.Err()
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

func agentsPlanOwner(ctx context.Context, tx pgx.Tx, tenantID, owner string) (string, error) {
	var canonical string
	err := tx.QueryRow(ctx, `SELECT canonical.id::text FROM principals person
		JOIN principals canonical ON canonical.tenant_id=person.tenant_id AND canonical.id=coalesce(person.linked_to,person.id)
		WHERE person.tenant_id=$1::uuid AND person.id=$2::uuid AND person.kind='person'
		AND person.status='active' AND canonical.kind='person' AND canonical.status='active'`, tenantID, owner).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", authz.ErrForbidden
	}
	return canonical, err
}

// Read every saved plan in the family, preferring the canonical row. Comparing
// effective ceilings preserves legacy shapes and treats a missing limit like
// explicit no_limit, while never guessing between conflicting start allowances.
func readAgentsPlanPreference(ctx context.Context, tx pgx.Tx, tenantID, owner string) ([]byte, *time.Time, error) {
	return agentplan.ReadPreference(ctx, tx, tenantID, owner)
}

func sameAgentsPlan(a, b agentplan.Plan) bool {
	return agentplan.SameLimits(a, b)
}
