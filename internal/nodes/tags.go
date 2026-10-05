// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/tenant"
)

type tagPatch struct {
	Name        *string `json:"name"`
	Color       *string `json:"color"`
	Description *string `json:"description"`
}

var allowedTagColors = map[string]bool{
	"gray": true, "slate": true, "blue": true, "indigo": true,
	"purple": true, "pink": true, "red": true, "orange": true,
	"yellow": true, "green": true, "teal": true, "cyan": true,
}

func (m *Module) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("tagId"), "invalid tag id")
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var patch tagPatch
	if err := decodeJSON(body, &patch); err != nil {
		writeErr(w, err)
		return
	}
	if patch.Name != nil && (p.Kind != tenant.Person || authz.Require(authz.BindPool(r.Context(), m.pool), "tags.manage", authz.Scope{}) != nil) {
		writeError(w, http.StatusForbidden, "permission denied")
		return
	}
	if patch.Name == nil && patch.Color == nil && patch.Description == nil {
		writeErr(w, badRequest("patch is empty"))
		return
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			writeErr(w, badRequest("name is required"))
			return
		}
		patch.Name = &name
	}
	if patch.Color != nil && !allowedTagColors[*patch.Color] {
		writeErr(w, badRequest("invalid tag color"))
		return
	}
	node, err := m.updateTag(r.Context(), p, id, patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (m *Module) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("tagId"), "invalid tag id")
	if !ok {
		return
	}
	if err := m.deleteTag(r.Context(), p, id); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// The table lock makes the assignment scan complete with respect to concurrent
// node writes, including writers outside this module. RLS still scopes every
// row query to the principal's tenant. The lock is held only for this request.
func lockTagAssignments(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `LOCK TABLE nodes IN SHARE ROW EXCLUSIVE MODE`)
	return err
}

func loadTag(ctx context.Context, tx pgx.Tx, id string) (nodeJSON, error) {
	tag, err := loadNode(ctx, tx, id, true)
	if err != nil {
		return nodeJSON{}, err
	}
	var slug string
	if err := tx.QueryRow(ctx, `SELECT slug FROM node_kinds WHERE id=$1::uuid`, tag.KindID).Scan(&slug); err != nil {
		return nodeJSON{}, dbErr("load tag kind", err)
	}
	if slug != "tag" {
		return nodeJSON{}, notFound("tag not found")
	}
	return tag, nil
}

func (m *Module) updateTag(ctx context.Context, p tenant.Principal, id string, patch tagPatch) (nodeJSON, error) {
	var updated nodeJSON
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		if err := lockTagAssignments(ctx, tx); err != nil {
			return err
		}
		before, err := loadTag(ctx, tx, id)
		if err != nil {
			return err
		}
		name, description := before.Title, before.Body
		fields := before.Fields
		if patch.Name != nil {
			name = *patch.Name
			if name != before.Title {
				var exists bool
				err := tx.QueryRow(ctx, `SELECT EXISTS (
					SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id
					WHERE k.slug='tag' AND n.deleted_at IS NULL AND n.id<>$1::uuid
					AND lower(n.title)=lower($2))`, id, name).Scan(&exists)
				if err != nil {
					return dbErr("check tag name", err)
				}
				if exists {
					return conflict("tag name already exists")
				}
			}
			if name != before.Title {
				if err := m.rewriteTagAssignments(ctx, tx, p, id, before.Title, &name); err != nil {
					return err
				}
				fields, err = rewriteTagFields(fields, before.Title, &name)
				if err != nil {
					return err
				}
			}
		}
		if patch.Description != nil {
			description = *patch.Description
		}
		if patch.Color != nil {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(fields, &object); err != nil {
				return dbErr("decode tag fields", err)
			}
			object["color"], _ = json.Marshal(*patch.Color)
			fields, err = json.Marshal(object)
			if err != nil {
				return err
			}
		}
		updated, err = scanNode(tx.QueryRow(ctx, `UPDATE nodes SET title=$2, body=$3, fields=$4::jsonb,
			updated_at=greatest(clock_timestamp(), updated_at + interval '1 microsecond')
			WHERE id=$1::uuid AND deleted_at IS NULL RETURNING `+nodeReturning,
			id, name, description, string(fields)))
		if err != nil {
			return dbErr("update tag", err)
		}
		return m.record(ctx, tx, p.ID, &id, evNodeUpdated, before, updated)
	})
	return updated, err
}

func (m *Module) deleteTag(ctx context.Context, p tenant.Principal, id string) error {
	return m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		if err := lockTagAssignments(ctx, tx); err != nil {
			return err
		}
		before, err := loadTag(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := m.rewriteTagAssignments(ctx, tx, p, id, before.Title, nil); err != nil {
			return err
		}
		deleted, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET deleted_at=now(),
			updated_at=greatest(clock_timestamp(), updated_at + interval '1 microsecond')
			WHERE id=$1::uuid AND deleted_at IS NULL RETURNING `+nodeReturning, id))
		if err != nil {
			return dbErr("delete tag", err)
		}
		return m.record(ctx, tx, p.ID, &id, evNodeDeleted, before, deleted)
	})
}

func (m *Module) rewriteTagAssignments(ctx context.Context, tx pgx.Tx, p tenant.Principal, tagID, oldName string, newName *string) error {
	needle, err := json.Marshal([]string{oldName})
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM nodes WHERE id<>$1::uuid AND deleted_at IS NULL
		AND jsonb_typeof(fields->'tags')='array' AND fields->'tags' @> $2::jsonb
		ORDER BY id FOR UPDATE`, tagID, string(needle))
	if err != nil {
		return dbErr("find tag assignments", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := delivery.RefuseReleaseNodes(ctx, tx, []string{id}); errors.Is(err, delivery.ErrReleaseAPI) {
			continue
		} else if err != nil {
			return err
		}
		before, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		raw, err := rewriteTagFields(before.Fields, oldName, newName)
		if err != nil {
			return err
		}
		after, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET fields=$2::jsonb,
			updated_at=greatest(clock_timestamp(), updated_at + interval '1 microsecond')
			WHERE id=$1::uuid AND deleted_at IS NULL RETURNING `+nodeReturning, id, string(raw)))
		if err != nil {
			return dbErr("rewrite tag assignment", err)
		}
		if err := m.record(ctx, tx, p.ID, &id, evNodeUpdated, before, after); err != nil {
			return err
		}
	}
	return nil
}

func rewriteTagFields(raw json.RawMessage, oldName string, newName *string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	value, ok := fields["tags"]
	if !ok {
		return raw, nil
	}
	var tags []string
	if err := json.Unmarshal(value, &tags); err != nil {
		return nil, conflict("invalid tag assignments")
	}
	changed := make([]string, 0, len(tags))
	seenReplacement := false
	for _, tag := range tags {
		if tag == oldName {
			if newName == nil {
				continue
			}
			tag = *newName
		}
		if newName != nil && tag == *newName {
			if seenReplacement {
				continue
			}
			seenReplacement = true
		}
		changed = append(changed, tag)
	}
	fields["tags"], _ = json.Marshal(changed)
	return json.Marshal(fields)
}
