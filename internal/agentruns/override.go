// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"github.com/inspr-at/paimos/internal/agentpairing"
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

// cancel ends a queued run from /agents (AEON-402): person-only, like Run now
// once. The run ends as cancelled and its capacity holds are released in the
// same transaction. A run that started is never changed here.
func (m *module) cancel(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "only a person can cancel a run")
	}
	v, o, err := lockRun(r.Context(), tx, r.PathValue("runId"))
	if err != nil {
		return nil, err
	}
	if err := workorders.CanEdit(p, o); err != nil {
		return nil, err
	}
	if v.Status == "cancelled" {
		return v, nil
	}
	if v.Status != "queued" {
		return nil, workorders.Fail(409, "only a queued run can be cancelled")
	}
	before := v
	if _, err := agentpairing.CancelQueuedRun(r.Context(), tx, v.ID); err != nil {
		return nil, err
	}
	if v, err = load(r.Context(), tx, v.ID, false); err != nil {
		return nil, err
	}
	return v, workorders.Record(r.Context(), tx, p, o.NodeID, "run.cancelled", before, v)
}
