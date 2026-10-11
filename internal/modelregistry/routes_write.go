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

	"github.com/inspr-at/paimos/internal/httpapi"
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
	out, err := listRoutes(ctx, tx)
	if err != nil {
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

// The legacy writer is retired. Do not prepare a catalog, decode a body or
// acquire mutation locks merely to report the successor API.
func (m *Module) replace(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</api/model-preferences/orders>; rel="successor-version"`)
	httpapi.WriteError(w, http.StatusGone, "Role ladders moved to Settings > Models; use model preference orders and model pauses.")
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
