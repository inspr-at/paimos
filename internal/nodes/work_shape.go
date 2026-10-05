// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"fmt"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/jackc/pgx/v5"
)

type WorkShape struct {
	IsLeaf            bool   `json:"is_leaf"`
	Depth             int    `json:"depth"`
	LevelName         string `json:"level_name"`
	LevelIcon         string `json:"level_icon"`
	WorkChildrenCount int    `json:"work_children_count"`
	StatusDerived     bool   `json:"status_derived"`
}

func workName(v workVocabulary, leaf bool, depth int) workLevel {
	level := workLevel{Name: fmt.Sprintf("Level %d", depth), Icon: "layers"}
	if leaf {
		level = workLevel{Name: "Ticket", Icon: "ticket"}
	} else if depth == 1 {
		level = workLevel{Name: "Epic", Icon: "epic"}
	} else if depth == 2 {
		level.Name = "Story"
	}
	var custom workLevel
	if leaf {
		custom = v.Leaf
	} else if depth > 0 && depth <= len(v.Levels) {
		custom = v.Levels[depth-1]
	}
	if custom.Name != "" {
		level.Name = custom.Name
	}
	if custom.Icon != "" {
		level.Icon = custom.Icon
	}
	return level
}

// One bounded batch shared by node details, list pages, tree pages and Graph.
// Historical snapshots never acquire new meaning or become write inputs.
func loadWorkShapes(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*WorkShape, error) {
	ctx, cancel := context.WithTimeout(ctx, eta.AggregateReadTimeout)
	defer cancel()
	out := map[string]*WorkShape{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 2000 {
		return nil, badRequest("too many work shapes")
	}
	v, err := loadVocabulary(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text,s.is_leaf,s.depth,s.work_children_count,s.status_derived,
 aeon_work_status_enabled(n.project_id),k.label,k.icon FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 CROSS JOIN LATERAL aeon_work_shape(n.id) s WHERE n.id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, label, icon string
		var enabled bool
		var s WorkShape
		if err = rows.Scan(&id, &s.IsLeaf, &s.Depth, &s.WorkChildrenCount, &s.StatusDerived, &enabled, &label, &icon); err != nil {
			return nil, err
		}
		name := workLevel{Name: label, Icon: icon}
		if enabled {
			name = workName(v, s.IsLeaf, s.Depth)
		}
		s.LevelName, s.LevelIcon = name.Name, name.Icon
		out[id] = &s
	}
	return out, rows.Err()
}
func enrichNodes(ctx context.Context, tx pgx.Tx, nodes []*nodeJSON) error {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	shapes, err := loadWorkShapes(ctx, tx, ids)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		n.WorkShape = shapes[n.ID]
	}
	return nil
}
