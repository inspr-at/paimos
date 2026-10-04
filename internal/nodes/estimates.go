// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type estimateView struct {
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
	if sameEstimateFields(next, old) {
		return raw, nil
	}
	value, present := next["estimate_hours"]
	if !present || value == nil {
		for _, key := range []string{"estimate_hours", "estimate_source", "estimate_by", "estimate_at", "estimate_confirmed"} {
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

func sameEstimateFields(next, old map[string]any) bool {
	for _, key := range []string{"estimate_hours", "estimate_source", "estimate_by", "estimate_at", "estimate_confirmed"} {
		if key == "estimate_hours" {
			a, aok := estimateNumber(next[key])
			b, bok := estimateNumber(old[key])
			if aok && bok && a == b {
				continue
			}
		}
		if !reflect.DeepEqual(next[key], old[key]) {
			return false
		}
	}
	return true
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

// source selects id, fields, kind_slug inside the tenant transaction. Direct
// ticket/task children avoid counting both a ticket and its nested tasks.
func estimateSQL(source string) string {
	return `WITH ` + workStateCategoryCTE() + `
 SELECT n.id, CASE WHEN n.kind_slug='epic' THEN roll.hours ELSE ` + estimateHoursSQL("n.fields") + ` END AS hours,
 coalesce(roll.estimated,0)::int AS estimated_children,coalesce(roll.total,0)::int AS open_children,
 author.id::text,author.name
 FROM (` + source + `) n
 LEFT JOIN LATERAL (
  SELECT sum(v.hours) AS hours,count(v.hours) AS estimated,count(*) AS total
  FROM (SELECT ` + estimateHoursSQL("c.fields") + ` AS hours FROM nodes c
   JOIN node_kinds ck ON ck.id=c.kind_id AND ck.tenant_id=c.tenant_id
   LEFT JOIN configured cfg ON cfg.kind_id=c.kind_id AND cfg.norm=` + workStateNormSQL("c.state") + `
   WHERE n.kind_slug='epic' AND c.tenant_id=current_setting('aeon.tenant_id')::uuid AND c.parent_id=n.id AND c.deleted_at IS NULL
   AND ck.slug IN ('ticket','task') AND ` + workNotClosedSQL("c.state", "cfg") + `) v
 ) roll ON n.kind_slug='epic'
 LEFT JOIN principals author ON n.kind_slug<>'epic' AND author.tenant_id=current_setting('aeon.tenant_id')::uuid
 AND author.id=CASE WHEN n.fields->>'estimate_by' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN (n.fields->>'estimate_by')::uuid END`
}

func loadEstimates(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*estimateView, error) {
	rows, err := tx.Query(ctx, estimateSQL(`SELECT n.id,n.fields,k.slug AS kind_slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=ANY($1::uuid[]) AND n.deleted_at IS NULL`), ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*estimateView{}
	for rows.Next() {
		var id string
		var byID, byName *string
		view := &estimateView{}
		if err := rows.Scan(&id, &view.Hours, &view.EstimatedChildren, &view.OpenChildren, &byID, &byName); err != nil {
			return nil, err
		}
		if byID != nil && byName != nil {
			view.By = &estimatePerson{*byID, *byName}
		}
		if view.Hours != nil || view.OpenChildren > 0 {
			out[id] = view
		}
	}
	return out, rows.Err()
}
