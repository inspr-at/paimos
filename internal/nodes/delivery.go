// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func refuseReleaseNode(ctx context.Context, tx pgx.Tx, id string) error {
	if err := delivery.RefuseReleaseNodes(ctx, tx, []string{id}); err != nil {
		if errors.Is(err, delivery.ErrReleaseAPI) {
			return conflict("use the release API")
		}
		return err
	}
	return nil
}

func guardPlacedKind(ctx context.Context, tx pgx.Tx, id, kind string) error {
	if delivery.ItemKind(kind) {
		return nil
	}
	var placed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ships_in WHERE item_node_id=$1 AND release_node_id IS NOT NULL)`, id).Scan(&placed); err != nil {
		return err
	}
	if placed {
		return conflict("remove the item from its release before changing its kind")
	}
	return nil
}
func guardReleasePatch(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, raw map[string]json.RawMessage) error {
	var project string
	err := tx.QueryRow(ctx, `SELECT project_node_id::text FROM project_releases WHERE release_node_id=$1`, id).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for key := range raw {
		if key != "title" && key != "body" {
			return conflict("use the release API")
		}
	}
	var person bool
	if err = tx.QueryRow(ctx, `SELECT kind='person' AND status='active' FROM principals WHERE id=$1`, p.ID).Scan(&person); err != nil {
		return err
	}
	if p.Kind != tenant.Person || !person {
		return &httpError{status: 403, msg: "person required"}
	}
	if err = authz.RequireTx(ctx, tx, p, "releases.write", authz.Scope{ProjectID: project}); err != nil {
		return &httpError{status: 403, msg: "release access required"}
	}
	return nil
}

type shipsMoveSnapshot struct {
	Item     string `json:"item_id"`
	Project  string `json:"project_id"`
	Rank     string `json:"rank"`
	Revision int64  `json:"revision"`
}

func lockShipsForMoves(ctx context.Context, tx pgx.Tx, ids []string) error {
	rows, err := tx.Query(ctx, `WITH RECURSIVE subtree AS (SELECT n.id FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=ANY($1::uuid[]) AND k.slug<>'project' UNION SELECT n.id FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id JOIN subtree t ON n.parent_id=t.id WHERE k.slug<>'project') SELECT s.item_node_id FROM ships_in s JOIN subtree t ON t.id=s.item_node_id ORDER BY s.item_node_id LIMIT 5001 FOR UPDATE OF s`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n > 5000 {
		return conflict("move exceeds release placement limits")
	}
	return nil
}

// shipsInBeforeMove runs under the canonical tree/access fence before nodes
// change project. Release placements block; backlog rows are deleted and the
// caller records them. Undo deliberately returns them to their original tail.
func shipsInBeforeMove(ctx context.Context, tx pgx.Tx, id string, parent *string) ([]shipsMoveSnapshot, error) {
	var rootKind string
	if err := tx.QueryRow(ctx, `SELECT k.slug FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1`, id).Scan(&rootKind); err != nil {
		return nil, err
	}
	if rootKind == "project" {
		return nil, nil
	}
	var target *string
	if parent != nil {
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN k.slug='project' THEN n.id ELSE n.project_id END::text FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1`, *parent).Scan(&target); err != nil {
			return nil, err
		}
	}
	var releases []string
	rows, err := tx.Query(ctx, `WITH RECURSIVE subtree AS (SELECT id FROM nodes WHERE id=$1 UNION ALL SELECT n.id FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id JOIN subtree t ON n.parent_id=t.id WHERE k.slug<>'project') SELECT r.release_node_id::text FROM project_releases r JOIN subtree t ON t.id=r.release_node_id WHERE r.project_node_id IS DISTINCT FROM $2::uuid ORDER BY r.release_node_id LIMIT 201`, id, target)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var release string
		if err = rows.Scan(&release); err != nil {
			rows.Close()
			return nil, err
		}
		releases = append(releases, release)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(releases) > 0 {
		return nil, conflict("use the release API; subtree contains releases: " + strings.Join(releases, ", "))
	}
	rows, err = tx.Query(ctx, `WITH RECURSIVE subtree AS (SELECT id FROM nodes WHERE id=$1 UNION ALL SELECT n.id FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id JOIN subtree t ON n.parent_id=t.id WHERE k.slug<>'project')
	 SELECT s.item_node_id::text,s.project_node_id::text,s.release_node_id::text,s.rank,s.revision FROM ships_in s JOIN subtree t ON t.id=s.item_node_id WHERE s.project_node_id IS DISTINCT FROM $2::uuid ORDER BY s.item_node_id LIMIT 5001`, id, target)
	if err != nil {
		return nil, err
	}
	out := []shipsMoveSnapshot{}
	ids := []string{}
	blocked := []string{}
	for rows.Next() {
		var v shipsMoveSnapshot
		var release *string
		if err = rows.Scan(&v.Item, &v.Project, &release, &v.Rank, &v.Revision); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
		ids = append(ids, v.Item)
		if release != nil {
			blocked = append(blocked, v.Item)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(out) > 5000 {
		return nil, conflict("move exceeds release placement limits")
	}
	if len(blocked) > 0 {
		return nil, &httpError{status: 409, msg: "remove items from their release first: " + strings.Join(blocked, ", "), fields: blocked}
	}
	if len(ids) > 0 {
		if _, err = tx.Exec(ctx, `DELETE FROM ships_in WHERE item_node_id=ANY($1::uuid[]) AND release_node_id IS NULL`, ids); err != nil {
			return nil, err
		}
	}
	return out, nil
}
