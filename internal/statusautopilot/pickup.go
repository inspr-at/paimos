// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// PickupTx is called only after the existing daemon claim validates its exact
// reservation set. The caller holds the tenant tree lock and owns the commit.
// Workspace/project policy controls audit attribution; pickup still transitions
// directly when automatic status rules are disabled.
func PickupTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, runID string) error {
	c, before, err := loadCandidate(ctx, tx, id, time.Now().UTC())
	if err != nil {
		return err
	}
	s, err := Load(ctx, tx)
	if err != nil {
		return err
	}
	enabled := s.Enabled
	if c.Node.ProjectID != nil {
		override, err := Project(ctx, tx, *c.Node.ProjectID, enabled)
		if err != nil {
			return err
		}
		enabled = override.Effective
	}
	if enabled && s.ModeAt(time.Now().UTC()) == "on" {
		return apply(ctx, tx, p, c.Node, decision{Rule: "queue_pickup", To: "in_progress", Reason: "Agent picked up queued work after account allowance and daemon claim checks.", Anchor: runID})
	}
	var after json.RawMessage
	err = tx.QueryRow(ctx, `UPDATE nodes SET state='in_progress',status_autopilot='{}',updated_at=clock_timestamp() WHERE id=$1 RETURNING to_jsonb(nodes)`, id).Scan(&after)
	if err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "node.updated", Before: before, After: after})
	return err
}
