// SPDX-License-Identifier: AGPL-3.0-only

package search

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/embedding"
	"github.com/inspr-at/paimos/internal/tenant"
)

// fusionWindow is the hard cap inside aeon_search_nodes. Cursor pages are
// slices of that window.
const fusionWindow = 200

type hitJSON struct {
	Node  nodeJSON `json:"node"`
	Score float64  `json:"score"`
}

type pageJSON struct {
	Items      []hitJSON `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}

type queryInput struct {
	Q      string
	Kind   string
	State  string
	Limit  int
	Cursor string
}

func (m *Module) search(ctx context.Context, p tenant.Principal, in queryInput) (pageJSON, error) {
	var cur *cursorBody
	if in.Cursor != "" {
		decoded, err := decodeCursor(in.Cursor)
		if err != nil || !cursorMatches(decoded, p.TenantID, in.Q, in.Kind, in.State) {
			return pageJSON{}, errBad("cursor does not match this search")
		}
		cur = &decoded
	}
	vector, model := m.queryVector(ctx, p.TenantID, in.Q)
	var ranked []scored
	nodes := map[string]nodeJSON{}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		ranked, err = queryRanked(ctx, tx, in.Q, vector, model, in.Kind, in.State)
		if err != nil {
			return err
		}
		nodes, err = loadNodes(ctx, tx, ranked)
		return err
	})
	if err != nil {
		return pageJSON{}, err
	}
	kept := make([]scored, 0, len(ranked))
	for _, row := range ranked {
		if _, ok := nodes[row.ID]; ok {
			kept = append(kept, row)
		}
	}
	pageRows, next, err := pageHits(kept, cur, in.Limit, cursorBody{
		Tenant: p.TenantID,
		Q:      in.Q,
		Kind:   in.Kind,
		State:  in.State,
	})
	if err != nil {
		return pageJSON{}, err
	}
	items := make([]hitJSON, 0, len(pageRows))
	for _, row := range pageRows {
		items = append(items, hitJSON{Node: nodes[row.ID], Score: row.Score})
	}
	return pageJSON{Items: items, NextCursor: next}, nil
}

func (m *Module) queryVector(ctx context.Context, tenantID, q string) ([]float32, string) {
	provider := m.provider
	if m.resolve != nil {
		var err error
		provider, err = m.resolve(ctx, tenantID)
		if err != nil {
			return nil, ""
		}
	}
	if provider == nil || provider.Model() == "" {
		return nil, ""
	}
	vecs, err := provider.Embed(ctx, []string{q})
	if err != nil || len(vecs) != 1 {
		slog.Warn("search embedding unavailable", "err", err)
		return nil, ""
	}
	if err := embedding.Validate(vecs[0]); err != nil {
		slog.Warn("search embedding rejected", "err", err)
		return nil, ""
	}
	return vecs[0], provider.Model()
}

func queryRanked(ctx context.Context, tx pgx.Tx, q string, vector []float32, model, kind, state string) ([]scored, error) {
	var literal any
	var modelArg any
	if vector != nil {
		if err := embedding.Validate(vector); err != nil {
			return nil, err
		}
		literal = pgtype.FlatArray[float32](vector)
		modelArg = model
	}
	var kindArg any
	if kind != "" {
		kindArg = kind
	}
	var stateArg any
	if state != "" {
		stateArg = state
	}
	rows, err := tx.Query(ctx, `
		SELECT node_id::text, score
		FROM aeon_search_nodes($1, $2::real[]::halfvec(1536), $3, $4::uuid, $5, $6)`,
		q, literal, modelArg, kindArg, stateArg, fusionWindow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scored
	for rows.Next() {
		var row scored
		if err := rows.Scan(&row.ID, &row.Score); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadNodes(ctx context.Context, tx pgx.Tx, ranked []scored) (map[string]nodeJSON, error) {
	out := make(map[string]nodeJSON, len(ranked))
	if len(ranked) == 0 {
		return out, nil
	}
	ids := make([]string, len(ranked))
	for i, row := range ranked {
		ids[i] = row.ID
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, key, kind_id::text, title, body, fields, state,
		       parent_id::text, trim_scale(position)::text,
		       created_at, updated_at, deleted_at
		FROM nodes
		WHERE deleted_at IS NULL AND id::text = ANY($1::text[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n nodeJSON
		var fields []byte
		var created, updated time.Time
		var deleted *time.Time
		if err := rows.Scan(&n.ID, &n.Key, &n.KindID, &n.Title, &n.Body, &fields, &n.State,
			&n.ParentID, &n.Position, &created, &updated, &deleted); err != nil {
			return nil, err
		}
		if len(fields) == 0 {
			fields = []byte(`{}`)
		}
		n.Fields = fields
		n.CreatedAt = formatTime(created)
		n.UpdatedAt = formatTime(updated)
		if deleted != nil {
			s := formatTime(*deleted)
			n.DeletedAt = &s
		}
		out[n.ID] = n
	}
	return out, rows.Err()
}
