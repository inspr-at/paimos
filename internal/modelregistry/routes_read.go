// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

const routesDisplayLimit = 50

type routeDisplay struct {
	Route
	Profile Profile `json:"profile"`
}

type routesRead struct {
	Role                string         `json:"role"`
	Steps               []routeDisplay `json:"steps"`
	Setup               bool           `json:"setup"`
	Truncated           bool           `json:"truncated"`
	DispatchFamilyOrder []string       `json:"dispatch_family_order"`
	ReviewFloors        []string       `json:"review_floors"`
}

func dispatchFamilyOrder() []string {
	families := make([]string, 0, len(ReviewFamilyRank))
	for family := range ReviewFamilyRank {
		families = append(families, family)
	}
	sort.Slice(families, func(i, j int) bool { return ReviewFamilyRank[families[i]] < ReviewFamilyRank[families[j]] })
	return families
}

func (m *Module) readRoutes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := principal(w, r)
	if !ok {
		return
	}
	roles := r.URL.Query()["role"]
	if len(roles) != 1 || !validRole(roles[0]) {
		writeErr(w, fail(http.StatusBadRequest, "one known role required"))
		return
	}
	timeout := m.routesTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout+time.Second)
	defer cancel()
	out := routesRead{Role: roles[0], Steps: []routeDisplay{}, DispatchFamilyOrder: dispatchFamilyOrder(), ReviewFloors: []string{
		"A reviewer must be from a different family than the author.",
		"Review dispatch requires a frontier or strong profile at xhigh reasoning.",
		"Dispatch checks platform capability, an approved account with capacity, and model preferences.",
	}}
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, timeout); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "models.read", authz.Scope{}); err != nil {
			return err
		}
		// One statement supplies both setup and the capped ladder under the same
		// snapshot. A LEFT JOIN retains setup even if this role has no steps.
		rows, err := tx.Query(ctx, `
			SELECT catalog.setup, step.value
			FROM (SELECT EXISTS(SELECT 1 FROM model_profiles) AS setup) catalog
			LEFT JOIN LATERAL (
				SELECT jsonb_build_object('role', r.role, 'priority', r.priority,
					'profile_id', r.profile_id, 'state', r.state, 'reason', r.reason,
					'valid_until', r.valid_until, 'profile',
					(to_jsonb(p) - 'tenant_id') || jsonb_build_object(
						'display_name', COALESCE(d.model_display->>'display_name',''),
						'short_name', COALESCE(d.model_display->>'short_name',''),
						'model_version', COALESCE(d.model_display->>'model_version',''),
						'effort_level', d.effort_level, 'provider', d.provider)) AS value,
					r.priority, r.profile_id
				FROM model_role_routes r
				JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.profile_id
				LEFT JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
				WHERE r.role=$1 ORDER BY r.priority, r.profile_id LIMIT $2
			) step ON true
			ORDER BY step.priority, step.profile_id`, out.Role, routesDisplayLimit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var step *routeDisplay
			if err := rows.Scan(&out.Setup, &step); err != nil {
				return err
			}
			if step != nil {
				out.Steps = append(out.Steps, *step)
			}
		}
		return rows.Err()
	})
	if err != nil {
		if db.IsStatementTimeout(err) {
			w.Header().Set("Retry-After", "1")
			httpapi.WriteError(w, http.StatusServiceUnavailable, "Review ladder read timed out; retry shortly.")
		} else if errors.Is(err, authz.ErrForbidden) {
			writeErr(w, fail(http.StatusForbidden, "permission denied"))
		} else {
			writeErr(w, err)
		}
		return
	}
	out.Truncated = len(out.Steps) > routesDisplayLimit
	if out.Truncated {
		out.Steps = out.Steps[:routesDisplayLimit]
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
