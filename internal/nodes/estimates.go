// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type estimateView struct {
	PlannedHours      *float64        `json:"planned_hours"`
	IsParent          bool            `json:"is_parent"`
	LeafCount         int             `json:"leaf_count"`
	EstimatedLeaves   int             `json:"estimated_leaves"`
	Hours             *float64        `json:"hours"`
	EstimatedChildren int             `json:"estimated_children"`
	OpenChildren      int             `json:"open_children"`
	By                *estimatePerson `json:"by,omitempty"`
}
type estimatePerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Fields are replacement documents. Preserve untouched legacy values and their
// provenance, but never trust client-supplied attribution for a new estimate.
func canonicalEstimate(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, raw, before json.RawMessage) (json.RawMessage, error) {
	var next, old map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&next); err != nil {
		return nil, badRequest("invalid estimate fields")
	}
	if len(before) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(before))
		decoder.UseNumber()
		if err := decoder.Decode(&old); err != nil {
			return nil, err
		}
	}
	keys := []string{"estimate_hours", "estimate_source", "estimate_by", "estimate_at", "estimate_confirmed"}
	unchanged := true
	for _, key := range keys {
		if key == "estimate_hours" {
			a, aok := estimateNumber(next[key])
			b, bok := estimateNumber(old[key])
			if aok && bok && a == b {
				continue
			}
		}
		if !reflect.DeepEqual(next[key], old[key]) {
			unchanged = false
		}
	}
	if unchanged {
		return raw, nil
	}
	value, present := next["estimate_hours"]
	if !present || value == nil {
		for _, key := range keys {
			delete(next, key)
		}
		return json.Marshal(next)
	}
	if _, ok := estimateNumber(value); !ok {
		return nil, badRequest("estimate_hours must be a number greater than 0 and at most 200")
	}
	if source, supplied := next["estimate_source"]; supplied && source != string(p.Kind) {
		return nil, badRequest("estimate_source must match the acting principal kind")
	}
	confirmed := p.Kind == tenant.Person
	if p.Kind == tenant.Agent && id != "" {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE tenant_id=$1::uuid AND agent_principal_id=$2::uuid AND ticket_node_id=$3::uuid AND stopped_at IS NULL AND archived_at IS NULL AND phase='working')`, p.TenantID, p.ID, id).Scan(&confirmed); err != nil {
			return nil, err
		}
	}
	next["estimate_source"], next["estimate_by"], next["estimate_at"], next["estimate_confirmed"] = string(p.Kind), p.ID, time.Now().UTC().Format(time.RFC3339Nano), confirmed
	return json.Marshal(next)
}

// Callers are told which field to set. The CLI adds its own flag hint.
func missingEstimateWarning() string {
	return "add an agent-hours estimate in fields.estimate_hours (for example 2 or 0.5)"
}

// Match the client hour range without expanding an untrusted decimal exponent.
func estimateNumber(value any) (float64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	hours, err := number.Float64()
	return hours, err == nil && !math.IsNaN(hours) && !math.IsInf(hours, 0) && hours > 0 && hours <= 200
}

// Guard casts against malformed legacy JSON, including numeric strings. The
// same expression drives reads, epic totals and sorting.
func estimateHoursSQL(fields string) string {
	return `CASE WHEN jsonb_typeof(` + fields + `->'estimate_hours')='number' THEN CASE WHEN (` + fields + `->>'estimate_hours')::numeric>0 AND (` + fields + `->>'estimate_hours')::numeric<=200 THEN (` + fields + `->>'estimate_hours')::numeric END END`
}

// loadWorkTotals shares the ETA/estimate traversal for a selected list page.
func loadWorkTotals(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*estimateView, map[string]eta.View, error) {
	aggregates, err := eta.LoadAggregates(ctx, tx, ids)
	if err != nil {
		return nil, nil, err
	}
	estimates := map[string]*estimateView{}
	views := map[string]eta.View{}
	for id, a := range aggregates {
		view := &estimateView{Hours: a.Hours, PlannedHours: a.PlannedHours, IsParent: a.IsParent, LeafCount: a.LeafCount, EstimatedLeaves: a.EstimatedLeaves, EstimatedChildren: a.EstimatedLeaves, OpenChildren: a.LeafCount}
		if a.EstimateByID != nil && a.EstimateByName != nil {
			view.By = &estimatePerson{*a.EstimateByID, *a.EstimateByName}
		}
		if view.Hours != nil || view.PlannedHours != nil || view.IsParent || view.LeafCount > 0 {
			estimates[id] = view
		}
		if !a.Blank() {
			views[id] = a.View
		}
	}
	return estimates, views, nil
}
func loadEstimates(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*estimateView, error) {
	estimates, _, err := loadWorkTotals(ctx, tx, ids)
	return estimates, err
}
