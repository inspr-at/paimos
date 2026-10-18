// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type nodeRecurrence struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"project_id"`
	ProjectKey string          `json:"project_key"`
	Number     int64           `json:"number"`
	Trigger    json.RawMessage `json:"trigger"`
	Retired    bool            `json:"retired"`
}

// Enrich only the selected bounded page in one query. Both source tables and
// the node are subject to tenant/project RLS; editable fields are not evidence
// that a ticket was created by a recurrence. Retirement preserves receipts.
func loadNodeRecurrences(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*nodeRecurrence, error) {
	out := make(map[string]*nodeRecurrence)
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 500 {
		return nil, fmt.Errorf("recurrence page exceeds 500 nodes")
	}
	rows, err := tx.Query(ctx, `SELECT o.node_id::text,r.id::text,r.project_id::text,project.key,o.number,r.trigger,r.retired_at IS NOT NULL
		FROM recurrence_occurrences o
		JOIN recurrences r ON r.tenant_id=o.tenant_id AND r.id=o.recurrence_id
		JOIN nodes project ON project.tenant_id=r.tenant_id AND project.id=r.project_id
		JOIN nodes n ON n.tenant_id=o.tenant_id AND n.id=o.node_id AND n.deleted_at IS NULL
		WHERE o.tenant_id=current_setting('aeon.tenant_id')::uuid
		AND o.node_id=ANY($1::uuid[]) AND o.outcome='created'`, ids)
	if err != nil {
		return nil, dbErr("load recurrence provenance", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var item nodeRecurrence
		if err := rows.Scan(&id, &item.ID, &item.ProjectID, &item.ProjectKey, &item.Number, &item.Trigger, &item.Retired); err != nil {
			return nil, err
		}
		out[id] = &item
	}
	return out, rows.Err()
}
