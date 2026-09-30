// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (m *module) runNow(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "only a person can run now once")
	}
	var in struct {
		Override string `json:"capacity_override"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Override != "now" {
		return nil, workorders.Fail(400, "capacity_override must be now")
	}
	v, o, err := lockRun(r.Context(), tx, r.PathValue("runId"))
	if err != nil {
		return nil, err
	}
	if err := workorders.CanEdit(p, o); err != nil {
		return nil, err
	}
	if v.Status != "queued" || v.Purpose != "managed" {
		return nil, workorders.Fail(409, "only a queued managed run can run now once")
	}
	if v.CapacityOverride == "now" {
		return v, nil
	}
	before := v
	v, err = scan(tx.QueryRow(r.Context(), `UPDATE agent_runs SET capacity_override='now' WHERE id=$1 RETURNING `+columns, v.ID))
	if err != nil {
		return nil, err
	}
	return v, workorders.Record(r.Context(), tx, p, o.NodeID, "run.capacity_override", before, v)
}
