// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// MergeRefusalTx adds an informational Needs You proposal. It cannot apply a
// state change: the normal ticket editor fixes the gate, then ingress retries.
// The caller holds the tenant/tree fence and all node/FK locks before calling.
func MergeRefusalTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, id, anchor, reason string) error {
	if len(anchor) > 200 || reason == "" || len(reason) > 2000 {
		return fmt.Errorf("invalid merge refusal")
	}
	var seen bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM status_autopilot_proposals WHERE node_id=$1 AND rule='merge_refused' AND anchor=$2)`, id, anchor).Scan(&seen); err != nil || seen {
		return err
	}
	var before json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, id).Scan(&before); err != nil {
		return err
	}
	d := decision{Rule: "merge_refused", Flag: "merge_refused", Reason: reason, Anchor: anchor, Skip: true}
	meta, _ := json.Marshal(map[string]string{"job": "merge-done", "rule": d.Rule, "reason": reason})
	e, err := events.Append(ctx, tx, actor, events.Change{NodeID: &id, Type: "status_autopilot.proposed", Before: before, After: before, Metadata: meta})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(d)
	_, err = tx.Exec(ctx, `INSERT INTO status_autopilot_proposals(tenant_id,event_id,node_id,rule,anchor,decision) VALUES($1,$2,$3,$4,$5,$6)`, actor.TenantID, e.ID, id, d.Rule, anchor, raw)
	return err
}
