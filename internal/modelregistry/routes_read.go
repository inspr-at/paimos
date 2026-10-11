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
	OrderMode            string         `json:"order_mode,omitempty"`
	ManagedFallbackOrder []string       `json:"managed_fallback_order"`
	Role                 string         `json:"role"`
	Steps                []routeDisplay `json:"steps"`
	Setup                bool           `json:"setup"`
	Truncated            bool           `json:"truncated"`
	DispatchFamilyOrder  []string       `json:"dispatch_family_order"`
	ReviewFloors         []string       `json:"review_floors"`
	Routes               []Route        `json:"routes"`
	EditToken            *string        `json:"edit_token"`
	CanEdit              bool           `json:"can_edit"`
}

func dispatchFamilyOrder() []string {
	ranks := ReviewFamilyRank()
	families := make([]string, 0, len(ranks))
	for family := range ranks {
		families = append(families, family)
	}
	sort.Slice(families, func(i, j int) bool { return ranks[families[i]] < ranks[families[j]] })
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
	out := routesRead{ManagedFallbackOrder: []string{}, Role: roles[0], Steps: []routeDisplay{}, DispatchFamilyOrder: dispatchFamilyOrder(), ReviewFloors: []string{
		"A reviewer must be from a different family than the author.",
		"Review dispatch requires a frontier or strong profile at its highest native level, equivalent to at least xhigh.",
		"Dispatch checks platform capability, an approved account with capacity, and model preferences.",
	}}
	err := m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, timeout); err != nil {
			return err
		}
		current, err := currentModelReader(r.WithContext(ctx), tx, p)
		if err != nil {
			return err
		}

		_ = current // currentModelReader rechecks live models.read authority.
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles)`).Scan(&out.Setup); err != nil {
			return err
		}
		steps, mode, err := derivedRoleSteps(ctx, tx, nil, out.Role, routesDisplayLimit+1)
		if err != nil {
			return err
		}
		out.OrderMode = mode
		for _, step := range steps {
			out.Steps = append(out.Steps, routeDisplay{Route: step.Route, Profile: step.Profile})
		}
		return nil
	})
	if err != nil {
		if db.IsStatementTimeout(err) {
			w.Header().Set("Retry-After", "1")
			httpapi.WriteError(w, http.StatusServiceUnavailable, "Models compatibility read timed out; retry shortly.")
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
	if out.Role == "review-gate" {
		managed := append([]routeDisplay{}, out.Steps...)
		if out.OrderMode == reviewOrderLegacy {
			sortLegacyReview(managed, func(s routeDisplay) Profile { return s.Profile })
		}
		out.ManagedFallbackOrder = []string{}
		for _, step := range managed {
			out.ManagedFallbackOrder = append(out.ManagedFallbackOrder, step.ProfileID)
		}
	} else {
		out.OrderMode = ""
	}
	out.Routes = []Route{}
	for _, step := range out.Steps {
		out.Routes = append(out.Routes, step.Route)
	}
	// This compatibility endpoint has no write authority or edit token.
	out.CanEdit, out.EditToken = false, nil
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</api/model-preferences/simple>; rel="successor-version"`)
	httpapi.WriteJSON(w, http.StatusOK, out)
}
