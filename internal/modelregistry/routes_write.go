// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const routesTotalLimit = 300

// routeEditToken covers canonical stored content, independent of wall time,
// input order, timezone spelling, profile display metadata and event position.
func routeEditToken(role string, routes []Route, modes ...string) string {
	mode := ""
	if role == "review-gate" {
		mode = reviewOrderLegacy
		if len(modes) > 0 && modes[0] != "" {
			mode = modes[0]
		}
	}
	canonical := canonicalRoutes(routes)
	raw, _ := json.Marshal(struct {
		Version   int     `json:"version"`
		Role      string  `json:"role"`
		Routes    []Route `json:"routes"`
		OrderMode string  `json:"order_mode,omitempty"`
	}{1, role, canonical, mode})
	sum := sha256.Sum256(raw)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func canonicalRoutes(routes []Route) []Route {
	out := append([]Route{}, routes...)
	for i := range out {
		if out[i].ValidUntil != nil {
			t := out[i].ValidUntil.UTC().Truncate(time.Microsecond)
			out[i].ValidUntil = &t // PostgreSQL timestamptz precision.
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ProfileID < out[j].ProfileID
	})
	return out
}

func roleRoutes(role string, rows []Route) []Route {
	out := []Route{}
	for _, row := range rows {
		if row.Role == role {
			out = append(out, row)
		}
	}
	return out
}

func routeBounds(rows []Route) error {
	if len(rows) > routesTotalLimit {
		return prefFail(413, "too_many_routes")
	}
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Role]++
		if counts[row.Role] > routesDisplayLimit {
			return prefFail(413, "too_many_routes")
		}
	}
	return nil
}

func boundedStoredRoutes(ctx context.Context, tx pgx.Tx) ([]Route, error) {
	rows, err := tx.Query(ctx, `SELECT role,priority,profile_id::text,state,reason,valid_until
	 FROM (`+agentaccounts.ModelRoleRoutesSQL+`) routes ORDER BY role,priority,profile_id LIMIT 301`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		var row Route
		if err := rows.Scan(&row.Role, &row.Priority, &row.ProfileID, &row.State, &row.Reason, &row.ValidUntil); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := routeBounds(out); err != nil {
		return nil, prefFail(413, "audit_snapshot_too_large")
	}
	return out, nil
}

func normalizeStoredRoutes(incoming, stored []Route, now time.Time, clear bool) ([]Route, error) {
	out, err := normalizeRouteStructure(incoming)
	if err != nil {
		return nil, err
	}
	out = canonicalRoutes(out)
	prior := map[string]Route{}
	for _, row := range stored {
		prior[row.Role+"/"+row.ProfileID] = row
	}
	for i, row := range out {
		if row.State == "available" || row.ValidUntil.After(now) {
			continue
		}
		if clear {
			out[i].State, out[i].Reason, out[i].ValidUntil = "available", "", nil
			continue
		}
		old, exists := prior[row.Role+"/"+row.ProfileID]
		if !exists || old.State != row.State || old.Reason != row.Reason || !timePtrEqual(old.ValidUntil, row.ValidUntil) {
			return nil, prefFail(422, "suppression_expired")
		}
	}
	return out, nil
}

func (m *Module) replace(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if err := m.requirePermission(r, p, "models.manage"); err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	role := ""
	if values, present := q["role"]; present {
		if len(values) != 1 || !validRole(values[0]) {
			writePreferenceError(w, prefFail(400, "invalid_role"))
			return
		}
		role = values[0]
	}
	clear := false
	if values, present := q["expiry_policy"]; present {
		if role == "" || len(values) != 1 || (values[0] != "clear" && values[0] != "preserve") {
			writePreferenceError(w, prefFail(400, "invalid_expiry_policy"))
			return
		}
		clear = values[0] == "clear"
	}
	requestedMode := ""
	if values, present := q["order_mode"]; present {
		if role != "review-gate" || len(values) != 1 || (values[0] != reviewOrderLegacy && values[0] != reviewOrderSaved) {
			writePreferenceError(w, prefFail(400, "invalid_order_mode"))
			return
		}
		requestedMode = values[0]
	}

	expected := ""
	if role != "" {
		values := r.Header.Values("If-Match")
		if len(values) == 0 {
			writePreferenceError(w, prefFail(428, "revision_required"))
			return
		}
		if len(values) != 1 || len(values[0]) != 66 || values[0][0] != '"' || values[0][65] != '"' {
			writePreferenceError(w, prefFail(400, "invalid_revision"))
			return
		}
		if _, err := hex.DecodeString(values[0][1:65]); err != nil {
			writePreferenceError(w, prefFail(400, "invalid_revision"))
			return
		}
		expected = values[0]
	}
	if err := workBody(w, r); err != nil {
		writePreferenceError(w, err)
		return
	}
	var in []Route
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := routeBounds(in); err != nil {
		writePreferenceError(w, err)
		return
	}
	in, err := normalizeRouteStructure(in)
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, row := range in {
		if role != "" && row.Role != role {
			writePreferenceError(w, prefFail(400, "mixed_roles"))
			return
		}
	}
	if role == "" {
		if err := PrepareCatalog(ctx, m.pool, p, CatalogPreparation{Operation: CatalogManage, Request: r}); err != nil {
			writeErr(w, err)
			return
		}
	}
	var out []Route
	var outMode string
	err = m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id',true),0))`); err != nil {
			return err
		}
		if p.Kind != tenant.Person {
			return prefFail(403, "person_required")
		}
		if err := authz.RequireTx(ctx, tx, p, "models.manage", authz.Scope{}); err != nil {
			return err
		}
		if err := requireCatalog(ctx, tx); err != nil {
			return err
		}
		before, err := boundedStoredRoutes(ctx, tx)
		if err != nil {
			return err
		}
		beforeMode, err := storedReviewOrder(ctx, tx)
		if err != nil {
			return err
		}
		outMode = beforeMode
		if requestedMode != "" {
			outMode = requestedMode
		}
		if role != "" && routeEditToken(role, roleRoutes(role, before), beforeMode) != expected {
			return prefFail(409, "stale_revision")
		}
		clock := m.validationClock
		if clock == nil {
			clock = validationNow
		}
		now, err := clock(ctx, tx)
		if err != nil {
			return err
		}
		normalized, err := normalizeStoredRoutes(in, before, now, clear)
		if err != nil {
			if role == "" {
				var temporal *preferenceError
				if errors.As(err, &temporal) && temporal.code == "suppression_expired" {
					return fail(400, "suppression requires a future expiry")
				}
			}
			return err
		}
		if err := profilesExist(ctx, tx, normalized); err != nil {
			return err
		}
		after := normalized
		if role != "" {
			for _, row := range before {
				if row.Role != role {
					after = append(after, row)
				}
			}
			after = canonicalRoutes(after)
		}
		if err := routeBounds(after); err != nil {
			return err
		}
		out = normalized
		sameRoutes := routesEqual(before, after)
		if sameRoutes && beforeMode == outMode {
			return nil
		}
		if !sameRoutes {
			if role == "" {
				_, err = tx.Exec(ctx, `DELETE FROM model_role_routes`)
				if err == nil {
					_, err = tx.Exec(ctx, `DELETE FROM model_security_role_routes`)
				}
			} else {
				_, err = tx.Exec(ctx, `DELETE FROM `+roleRoutesTable(role)+` WHERE role=$1`, role)
			}
			if err != nil {
				return err
			}
			for _, row := range normalized {
				if err := insertRoute(ctx, tx, p.TenantID, row); err != nil {
					return err
				}
			}
		}
		if beforeMode != outMode {
			if err := saveReviewOrder(ctx, tx, p.TenantID, outMode); err != nil {
				return err
			}
		}
		// Read actual persisted precision before deriving response/token/audit.
		after, err = boundedStoredRoutes(ctx, tx)
		if err != nil {
			return err
		}
		out = after
		if role != "" {
			out = roleRoutes(role, after)
		}
		return writeRoutesEvent(ctx, tx, p, before, after, beforeMode, outMode)
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	if role != "" {
		w.Header().Set("ETag", routeEditToken(role, out, outMode))
	}
	if role == "review-gate" {
		w.Header().Set("Model-Order-Mode", outMode)
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}

func validationNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

// workBody bounds bytes and arrival time before a mutation retains any locks.
// It shares the endpoint helper so a failed read keeps its transport deadline
// and a transport-ended read makes exactly one deadline call (AEON-652).
func workBody(w http.ResponseWriter, r *http.Request) error {
	err := workorders.BufferEndpointBody(w, r)
	var failure *workorders.Error
	if errors.As(err, &failure) {
		return prefFail(failure.Status, failure.Message)
	}
	return err
}
