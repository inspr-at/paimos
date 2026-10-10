// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func lockProject(ctx context.Context, tx pgx.Tx, projectID string) error {
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND k.slug='project' AND n.deleted_at IS NULL FOR UPDATE OF n`, projectID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(http.StatusNotFound, "project not found")
	}
	return err
}

func projectVisible(ctx context.Context, tx pgx.Tx, projectID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND k.slug='project' AND n.deleted_at IS NULL`, projectID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(http.StatusNotFound, "project not found")
	}
	return err
}

func fileOwned(ctx context.Context, tx pgx.Tx, fileID string) error {
	var owned bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files WHERE id = $1::uuid)`, fileID).Scan(&owned)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "42P01" {
			return fail(http.StatusConflict, "file is not owned by this tenant")
		}
		return err
	}
	if !owned {
		return fail(http.StatusConflict, "file is not owned by this tenant")
	}
	return nil
}

func appendMeta(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID *string, eventType string, after any) (events.Event, error) {
	return events.Append(ctx, tx, p, events.Change{NodeID: nodeID, Type: eventType, After: after})
}

func lastEventID(ctx context.Context, tx pgx.Tx, nodeID string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM events WHERE node_id = $1::uuid`, nodeID).Scan(&id)
	return id, err
}

func nodeInProject(ctx context.Context, tx pgx.Tx, nodeID, projectID string) (string, error) {
	var slug string
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id, parent_id FROM nodes
			WHERE id = $1::uuid AND deleted_at IS NULL
			UNION ALL
			SELECT n.id, n.parent_id FROM nodes n
			JOIN chain c ON n.id = c.parent_id
			WHERE n.deleted_at IS NULL
		)
		SELECT k.slug
		FROM nodes target
		JOIN node_kinds k ON k.tenant_id = target.tenant_id AND k.id = target.kind_id
		WHERE target.id = $1::uuid AND target.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM chain WHERE id = $2::uuid)`, nodeID, projectID).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", refusal(http.StatusConflict, "target_changed", "target is not in this project")
	}
	return slug, err
}

const nodeReturning = `id::text, key, kind_id::text, title, body, fields, state, parent_id::text, position::text, created_at, updated_at, deleted_at`

func scanNode(row pgx.Row) (nodeSnap, error) {
	var n nodeSnap
	err := row.Scan(&n.ID, &n.Key, &n.KindID, &n.Title, &n.Body, &n.Fields, &n.State, &n.ParentID, &n.Position, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt)
	if err != nil {
		return nodeSnap{}, err
	}
	if len(n.Fields) == 0 {
		n.Fields = []byte("{}")
	}
	return n, nil
}

func loadNode(ctx context.Context, tx pgx.Tx, id string, lock bool) (nodeSnap, error) {
	q := `SELECT ` + nodeReturning + ` FROM nodes WHERE id = $1::uuid AND deleted_at IS NULL`
	if lock {
		q += ` FOR UPDATE`
	}
	node, err := scanNode(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nodeSnap{}, fail(http.StatusNotFound, "target not found")
	}
	return node, err
}
