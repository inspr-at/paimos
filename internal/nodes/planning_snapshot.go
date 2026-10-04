// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/usagedashboard"
	"github.com/jackc/pgx/v5"
)

type estimateRateBasis struct {
	planningCalibration
	Speed              float64 `json:"speed,omitempty"`
	TokensPerHourExact string  `json:"tokens_per_hour_exact"`
	ListPerHour        *string `json:"list_per_hour,omitempty"`
	PriceVersion       *int64  `json:"price_version,omitempty"`
	Input              *string `json:"input_usd_per_million,omitempty"`
	Output             *string `json:"output_usd_per_million,omitempty"`
	Cached             *string `json:"cached_input_usd_per_million,omitempty"`
	CachedMix          float64 `json:"cached_mix"`
	InputMix           float64 `json:"input_mix"`
	OutputMix          float64 `json:"output_mix"`
}

type planningSnapshot struct {
	WorkClassification json.RawMessage                      `json:"work_classification,omitempty"`
	ModelEstimate      *usagedashboard.ModelEstimateHistory `json:"model_estimate,omitempty"`
	CostProject        string                               `json:"-"`
	ID                 string                               `json:"id"`
	StartedAt          time.Time                            `json:"started_at"`
	Source             string                               `json:"source"`
	Hours              *float64                             `json:"estimate_hours"`
	Tokens             *int64                               `json:"estimated_tokens"`
	Cost               *string                              `json:"estimated_cost_usd,omitempty"`
	Route              *planningRoute                       `json:"route"`
	RateBasis          estimateRateBasis                    `json:"rate_basis"`
}

// CapturePlanningStart serializes starts on the node, including competing
// registrations. A status transition and its first session share one baseline.
// Re-estimates never mutate it; done, cancelled or archived closes the episode.
func CapturePlanningStart(ctx context.Context, tx pgx.Tx, id, source string) error {
	var project, kind string
	err := tx.QueryRow(ctx, `SELECT coalesce(n.project_id::text,''),k.slug FROM nodes n
        JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
        WHERE n.id=$1::uuid AND n.deleted_at IS NULL FOR UPDATE OF n`, id).Scan(&project, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if kind != "ticket" && kind != "task" {
		return nil
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_estimate_snapshots WHERE ticket_node_id=$1::uuid AND closed_at IS NULL)`, id).Scan(&exists); err != nil || exists {
		return err
	}
	rows, err := loadPlanRows(ctx, tx, []string{id}, nil)
	if err != nil || len(rows) == 0 {
		return err
	}
	routes, err := resolvePlanRoutes(ctx, tx, rows)
	if err != nil {
		return err
	}
	// A stored cost never incorporates private readings from another project.
	samples, err := loadCalibrationSamples(ctx, tx, routes, func(p string) bool { return p == project })
	if err != nil {
		return err
	}
	pl := &planner{routes: routes, samples: samples, billing: map[string]planBilling{}, calibrations: map[routeKey]calibration{}}
	if err := pl.loadLearning(ctx, tx, func(p string) bool { return p == project }, project); err != nil {
		return err
	}
	row := rows[0]
	route := pl.route(row)
	cal := pl.calibration(route)
	snap := planningSnapshot{ModelEstimate: pl.modelEstimate(row), Source: source, Hours: row.hours, Tokens: pl.estimate(row).tokens,
		RateBasis: estimateRateBasis{Speed: cal.speed, planningCalibration: planningCalibration{BasisText: cal.basisText, Level: cal.level, Basis: cal.basis, Tickets: cal.tickets, TokensPerHour: int64(math.Round(cal.tokensPerHour)), AnyRoute: route == nil}, TokensPerHourExact: strconv.FormatFloat(cal.tokensPerHour, 'f', -1, 64), CachedMix: mixCached, InputMix: mixInput, OutputMix: mixOutput}}
	// Freeze placement with the size estimate; later edits cannot relabel evidence.
	if err = tx.QueryRow(ctx, `SELECT jsonb_strip_nulls(jsonb_build_object('area',fields->'area','route_role',fields->'route_role','complexity',fields->'complexity')) FROM nodes WHERE id=$1`, id).Scan(&snap.WorkClassification); err != nil {
		return err
	}
	if route != nil {
		snap.Route = route.view
		if price := route.price; price != nil {
			snap.RateBasis.PriceVersion = price.Version
			snap.RateBasis.Input, snap.RateBasis.Output, snap.RateBasis.Cached = price.Input, price.Output, price.Cached
		}
	}
	if cal.listPerHour != nil {
		rate := strconv.FormatFloat(*cal.listPerHour*cal.speed, 'f', -1, 64)
		snap.RateBasis.ListPerHour = &rate
		if row.hours != nil {
			// Match the live planning numeric multiplication and micro-dollar rounding.
			if err = tx.QueryRow(ctx, `SELECT (round($1::numeric * $2::numeric * 1000000)/1000000)::numeric(30,6)::text`, *row.hours, rate).Scan(&snap.Cost); err != nil {
				return err
			}
		}
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO ticket_estimate_snapshots(tenant_id,ticket_node_id,snapshot,source_project_id)
        VALUES(current_setting('aeon.tenant_id')::uuid,$1::uuid,$2::jsonb,NULLIF($3,'')::uuid)
        ON CONFLICT (tenant_id,ticket_node_id) WHERE closed_at IS NULL DO NOTHING`, id, string(raw), project)
	return err
}

func snapshotNodeChange(ctx context.Context, tx pgx.Tx, e Event) error {
	if e.NodeID == nil || (e.Type != evNodeCreated && e.Type != evNodeUpdated) {
		return nil
	}
	var before, after struct {
		State string `json:"state"`
	}
	if len(e.Before) > 0 {
		if err := json.Unmarshal(e.Before, &before); err != nil {
			return err
		}
	}
	if err := json.Unmarshal(e.After, &after); err != nil {
		return err
	}
	if before.State == after.State {
		return nil
	}
	// Use the same kind categories and fallback spellings as project counts
	// and Hide closed. QA and blocked are part of the existing work episode.
	var was, now string
	err := tx.QueryRow(ctx, `WITH `+workStateCategoryCTE()+`
        SELECT `+workCountBucketSQL("$2::text", "before_category")+`, `+workCountBucketSQL("$3::text", "after_category")+`
        FROM nodes n
        LEFT JOIN configured before_category ON before_category.kind_id=n.kind_id AND before_category.norm=`+workStateNormSQL("$2::text")+`
        LEFT JOIN configured after_category ON after_category.kind_id=n.kind_id AND after_category.norm=`+workStateNormSQL("$3::text")+`
        WHERE n.id=$1::uuid AND n.deleted_at IS NULL`, *e.NodeID, before.State, after.State).Scan(&was, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if was != "in_progress" && now == "in_progress" {
		return CapturePlanningStart(ctx, tx, *e.NodeID, "status")
	}
	// A first session can start work while the status is still open.
	switch now {
	case "done", "cancelled", "archived":
		_, err := tx.Exec(ctx, `UPDATE ticket_estimate_snapshots SET closed_at=clock_timestamp() WHERE ticket_node_id=$1::uuid AND closed_at IS NULL`, *e.NodeID)
		return err
	}
	return nil
}

func loadPlanningSnapshots(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*planningSnapshot, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (ticket_node_id) ticket_node_id::text,id::text,started_at,snapshot,coalesce(source_project_id::text,'')
        FROM ticket_estimate_snapshots WHERE ticket_node_id=ANY($1::uuid[]) ORDER BY ticket_node_id,started_at DESC,id DESC`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*planningSnapshot{}
	for rows.Next() {
		var node, id, project string
		var at time.Time
		var raw []byte
		if err = rows.Scan(&node, &id, &at, &raw, &project); err != nil {
			return nil, err
		}
		var snap planningSnapshot
		if err = json.Unmarshal(raw, &snap); err != nil {
			return nil, err
		}
		snap.ID, snap.StartedAt, snap.CostProject = id, at, project
		out[node] = &snap
	}
	return out, rows.Err()
}

func (s *planningSnapshot) hideCost() {
	s.Cost = nil
	s.RateBasis.ListPerHour, s.RateBasis.PriceVersion = nil, nil
	s.RateBasis.Input, s.RateBasis.Output, s.RateBasis.Cached = nil, nil, nil
}
