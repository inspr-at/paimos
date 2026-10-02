// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"fmt"

	"github.com/inspr-at/paimos/internal/usagedashboard"
	"github.com/jackc/pgx/v5"
)

func (pl *planner) loadLearning(ctx context.Context, tx pgx.Tx, visible func(string) bool) error {
	cells := []usagedashboard.LearningCell{}
	seen := map[usagedashboard.LearningCell]bool{}
	for _, route := range pl.routes {
		if route.view != nil && !seen[route.cell] {
			cells = append(cells, route.cell)
			seen[route.cell] = true
		}
	}
	var err error
	pl.learning, err = usagedashboard.LoadLearningSamples(ctx, tx, cells, "", visible)
	return err
}

func scaledRate(rate *float64, speed float64) *float64 {
	if rate == nil {
		return nil
	}
	v := *rate * speed
	return &v
}

// Model speed and token rate have different evidence thresholds: a token
// sample may lack the frozen size required to learn speed. Speed never backs
// off across models, kinds, versions or complexity buckets.
func (pl *planner) calibrateLearning(route *planRoute, key routeKey, mix *float64) calibration {
	out := calibrate(pl.samples, key, mix)
	out.speed = 1
	out.level = "route"
	out.basisText = fmt.Sprintf("median of %d finished tickets on route %s %s %s (n=%d)", out.tickets, key.harness, key.model, key.effort, out.tickets)
	if route != nil {
		cell := route.cell
		history := usagedashboard.History(pl.learning, cell)
		if history.SpeedFactor != nil {
			out.speed = *history.SpeedFactor
		}
		levels := []struct {
			name    string
			matches func(usagedashboard.LearningSample) bool
		}{
			{"cell", func(s usagedashboard.LearningSample) bool { return cell.Exact(s.Cell) }},
			{"line", func(s usagedashboard.LearningSample) bool { return cell.SameLine(s.Cell) }},
			{"profile", func(s usagedashboard.LearningSample) bool {
				return cell.ProfileID != "" && cell.ProfileID == s.Cell.ProfileID && cell.Effort == s.Cell.Effort
			}},
		}
		for _, level := range levels {
			samples := usagedashboard.SelectLearning(pl.learning, level.matches)
			if len(samples) < usagedashboard.LearningMinimum {
				continue
			}
			rates := make([]float64, len(samples))
			for i, s := range samples {
				rates[i] = s.Tokens / s.Hours
			}
			out.basis, out.level, out.tickets, out.tokensPerHour = "median", level.name, len(samples), median(rates)
			out.basisText = usagedashboard.LearningBasis(samples, cell, level.name)
			// Historical list costs from another model/version do not price the
			// selected model. Use its current published price and token mix.
			out.listPerHour = nil
			if mix != nil {
				v := out.tokensPerHour * *mix
				out.listPerHour = &v
			}
			return out
		}
	}
	if out.basis == "default" {
		out.level = "default"
		for _, sample := range pl.samples {
			if key.matches(sample) && out.tickets < calibrationWindow {
				out.tickets++
			}
		}
		if route != nil {
			n := usagedashboard.History(pl.learning, route.cell).Tickets
			if n > out.tickets {
				out.tickets = n
			}
		}
		out.basisText = fmt.Sprintf("uncalibrated: documented planning fallback (n=%d)", out.tickets)
	}
	return out
}

func (pl *planner) modelEstimate(row planRow) *usagedashboard.ModelEstimateHistory {
	route := pl.route(row)
	if route == nil || row.hours == nil {
		return nil
	}
	history := usagedashboard.History(pl.learning, route.cell)
	// Typical historical hours are for the picker. Planning hours multiply
	// this ticket's size only if its exact cell has a measured speed factor.
	history.Hours = nil
	if history.SpeedFactor != nil {
		v := *row.hours * *history.SpeedFactor
		history.Hours = &v
	}
	history.Basis += fmt.Sprintf("; speed: exact cell (n=%d)", history.SpeedTickets)
	return &history
}
