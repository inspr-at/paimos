// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"context"
	"strings"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// WriteAgentResultTx is the narrow result destination catalogue for a current
// person-granted agent run. The recurrence module owns the final authority,
// dispatch and idempotency checks. These ordinary writers recheck destination
// permissions and return all audit for append AFTER the result receipt.
func WriteAgentResultTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, parent, run string, d recurrences.AgentDestination, in recurrences.AgentResultInput) (string, []events.Change, error) {
	switch d.Kind {
	case "ticket_comment":
		change, err := activity.RoutineCommentTx(ctx, tx, p, d.TicketID, project, in.Body)
		return d.TicketID, []events.Change{change}, err
	case "new_ticket":
		mutation, err := nodes.CreateRoutineWorkTx(ctx, tx, p, parent, in.Title, in.Body)
		changes := make([]events.Change, 0, len(mutation.Events))
		for _, e := range mutation.Events {
			changes = append(changes, events.Change{NodeID: e.NodeID, Type: e.Type, Before: e.Before, After: e.After})
		}
		return mutation.ID, changes, err
	case "knowledge":
		node, _, changes, err := knowledge.WriteRoutineTx(ctx, tx, p, project, project, 0, "memory", "routine-"+strings.ReplaceAll(run, "-", ""), in.Title, in.Body)
		return node, changes, err
	default:
		return "", nil, workorders.Fail(409, "invalid agent destination")
	}
}
