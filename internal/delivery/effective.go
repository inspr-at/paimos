// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import "github.com/inspr-at/paimos/internal/deliverymodel"

// Effective is a SQL relation fragment. Join it as a subquery with a caller's
// alias; its columns are tenant_id, project_node_id, item_node_id, kind, state,
// created_at, release_node_id, release_rank, release_state, rank, revision,
// expedite, due_on and source. Callers must bind tenant/project/item predicates
// and bound lists before executing, in a tenant transaction with visibility.
//
// It returns only live permitted items in live releases-mode projects. No
// ships_in row means the unranked backlog tail (rank NULL, revision 0).
// Tombstones and hidden backlog kinds keep their stored placement but are
// absent from this read.
// New creates therefore need no placement write or maintenance trigger.
const Effective = deliverymodel.Effective

func ItemKind(kind string) bool { return kind == "epic" || kind == "ticket" || kind == "task" }

// Completed is the unit's own successful state, never an epic rollup or a
// tenant-defined completion guess. Callers select tickets/tasks as units.
func Completed(state string) bool {
	return state == "done" || state == "accepted" || state == "delivered"
}

func Cancelled(state string) bool { return state == "cancelled" || state == "canceled" }
