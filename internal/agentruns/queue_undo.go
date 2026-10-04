// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// queueUndo restores only the caller's stale addition at its exact audited
// revision. Tenant/pairing/tree locks serialize access changes and pickup.
func (m *module) queueUndo(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in queueUndoToken
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RunID) || in.Revision.IsZero() {
		return nil, workorders.Fail(400, "run_id and revision required")
	}
	ctx := r.Context()
	t, err := queueLoadTicket(ctx, tx, r.PathValue("nodeId"), true)
	if err != nil {
		return nil, err
	}
	if err = queuePermission(ctx, tx, p, t.ProjectID, true); err != nil {
		return nil, err
	}
	v, _, err := lockRun(ctx, tx, in.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(409, "queued work changed")
	}
	if err != nil {
		return nil, err
	}
	if v.Status != "queued" || v.QueueNodeID == nil || *v.QueueNodeID != t.ID {
		return nil, workorders.Fail(409, "queued work has started or changed")
	}
	var before json.RawMessage
	err = tx.QueryRow(ctx, `SELECT e.before FROM events e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id
 WHERE e.node_id=$1 AND e.type='queue.added' AND e.actor_principal_id=$2
 AND e.metadata->>'run_id'=$3 AND n.updated_at=$4
 AND (e.after->>'updated_at')::timestamptz=n.updated_at
 ORDER BY e.id DESC LIMIT 1`, t.ID, p.ID, in.RunID, in.Revision).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(409, "ticket changed since queueing")
	}
	if err != nil {
		return nil, err
	}
	var previous struct {
		State  string
		Fields map[string]any
	}
	if err = json.Unmarshal(before, &previous); err != nil {
		return nil, err
	}
	if !workqueue.Progress(previous.State) {
		return nil, workorders.Fail(409, "queue change is not a stale-work recovery")
	}
	// Check assignment and other runs/sessions again; only this queued run is
	// excluded, and the original In progress state comes from the audit record.
	if _, err = tx.Exec(ctx, `UPDATE nodes SET state=$2 WHERE id=$1`, t.ID, previous.State); err != nil {
		return nil, err
	}
	stale, err := workqueue.Stale(ctx, tx, []string{t.ID}, in.RunID)
	if err != nil {
		return nil, err
	}
	if !stale[t.ID] {
		return nil, workorders.Fail(409, "ticket is no longer idle")
	}
	removed, err := workqueue.RemoveQueued(ctx, tx, p, t.ID)
	if err != nil {
		return nil, err
	}
	if !removed {
		return nil, workorders.Fail(409, "queued work changed")
	}
	t.State, t.Fields = previous.State, previous.Fields
	if err = queueUpdateTicket(ctx, tx, p, t, "queue.add_undone"); err != nil {
		return nil, err
	}
	return map[string]bool{"removed": true}, nil
}
