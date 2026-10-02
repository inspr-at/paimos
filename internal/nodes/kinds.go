// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/tenant"
)

type kindJSON struct {
	ID                string          `json:"id"`
	Slug              string          `json:"slug"`
	Label             string          `json:"label"`
	ShortPrefix       string          `json:"short_prefix"`
	Icon              string          `json:"icon"`
	AllowedChildKinds []string        `json:"allowed_child_kinds"`
	FieldSchema       json.RawMessage `json:"field_schema"`
}

type kindWrite struct {
	Slug              string          `json:"slug"`
	Label             string          `json:"label"`
	ShortPrefix       string          `json:"short_prefix"`
	Icon              string          `json:"icon"`
	AllowedChildKinds json.RawMessage `json:"allowed_child_kinds"`
	FieldSchema       json.RawMessage `json:"field_schema"`
}

func (m *Module) handleListKinds(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	items, err := m.listKinds(r.Context(), p.TenantID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []kindJSON `json:"items"`
	}{Items: items})
}

func (m *Module) handleCreateKind(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var in kindWrite
	if err := decodeJSON(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	kind, err := m.createKind(r.Context(), p, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, kind)
}

func (m *Module) handleGetKind(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("kindId"), "invalid kind id")
	if !ok {
		return
	}
	kind, err := m.getKind(r.Context(), p.TenantID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, kind)
}

func (m *Module) handleUpdateKind(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("kindId"), "invalid kind id")
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	raw, err := decodeObject(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	kind, err := m.updateKind(r.Context(), p, id, raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, kind)
}

func (m *Module) handleDeleteKind(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("kindId"), "invalid kind id")
	if !ok {
		return
	}
	if err := m.deleteKind(r.Context(), p, id); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func pathUUID(w http.ResponseWriter, raw, msg string) (string, bool) {
	id, ok := parseUUID(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, msg)
		return "", false
	}
	return id, true
}

func (m *Module) listKinds(ctx context.Context, tenantID string) ([]kindJSON, error) {
	items := []kindJSON{}
	err := m.tx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, slug, label, short_prefix, icon, allowed_child_kinds, field_schema
			FROM node_kinds
			ORDER BY slug, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			kind, err := scanKind(rows)
			if err != nil {
				return err
			}
			items = append(items, kind)
		}
		return rows.Err()
	})
	return items, err
}

func (m *Module) getKind(ctx context.Context, tenantID, id string) (kindJSON, error) {
	var kind kindJSON
	err := m.tx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		loaded, _, err := loadKind(ctx, tx, id)
		if err != nil {
			return err
		}
		kind = loaded
		return nil
	})
	return kind, err
}

func (m *Module) createKind(ctx context.Context, p tenant.Principal, in kindWrite) (kindJSON, error) {
	if !validSlug(in.Slug) {
		return kindJSON{}, badRequest("invalid slug")
	}
	if !nonBlank(in.Label) {
		return kindJSON{}, badRequest("label is required")
	}
	if !validPrefix(in.ShortPrefix) {
		return kindJSON{}, badRequest("invalid short_prefix")
	}
	if !nonBlank(in.Icon) {
		return kindJSON{}, badRequest("icon is required")
	}
	allowed, err := parseStringList(in.AllowedChildKinds)
	if err != nil {
		return kindJSON{}, err
	}
	if _, err := compileSchema(in.FieldSchema); err != nil {
		return kindJSON{}, err
	}
	var kind kindJSON
	err = m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO node_kinds (tenant_id, slug, label, short_prefix, icon, allowed_child_kinds, field_schema)
			VALUES (
				NULLIF(current_setting('aeon.tenant_id', true), '')::uuid,
				$1, $2, $3, $4, $5, $6::jsonb
			)
			RETURNING id::text, slug, label, short_prefix, icon, allowed_child_kinds, field_schema`,
			in.Slug, in.Label, in.ShortPrefix, in.Icon, allowed, string(in.FieldSchema))
		loaded, scanErr := scanKind(row)
		if scanErr != nil {
			return dbErr("insert kind", scanErr)
		}
		if err := m.record(ctx, tx, p.ID, nil, evKindCreated, nil, loaded); err != nil {
			return err
		}
		kind = loaded
		return nil
	})
	return kind, err
}

func (m *Module) updateKind(ctx context.Context, p tenant.Principal, id string, raw map[string]json.RawMessage) (kindJSON, error) {
	if len(raw) == 0 {
		return kindJSON{}, badRequest("patch is empty")
	}
	if _, ok := raw["slug"]; ok {
		return kindJSON{}, badRequest("slug is immutable")
	}
	for key := range raw {
		switch key {
		case "label", "short_prefix", "icon", "allowed_child_kinds", "field_schema":
		default:
			return kindJSON{}, badRequest("unknown field")
		}
	}
	var kind kindJSON
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Lock before loading the full snapshot used to merge a partial patch.
		if _, err := tx.Exec(ctx, `SELECT id FROM node_kinds WHERE id=$1::uuid FOR NO KEY UPDATE`, id); err != nil {
			return err
		}
		current, _, err := loadKind(ctx, tx, id)
		if err != nil {
			return err
		}
		next := current
		if v, ok := raw["label"]; ok {
			s, err := parsePatchString(v)
			if err != nil || !nonBlank(s) {
				return badRequest("label is required")
			}
			next.Label = s
		}
		if v, ok := raw["short_prefix"]; ok {
			s, err := parsePatchString(v)
			if err != nil || !validPrefix(s) {
				return badRequest("invalid short_prefix")
			}
			next.ShortPrefix = s
		}
		if v, ok := raw["icon"]; ok {
			s, err := parsePatchString(v)
			if err != nil || !nonBlank(s) {
				return badRequest("icon is required")
			}
			next.Icon = s
		}
		if v, ok := raw["allowed_child_kinds"]; ok {
			allowed, err := parseStringList(v)
			if err != nil {
				return err
			}
			next.AllowedChildKinds = allowed
		}
		if v, ok := raw["field_schema"]; ok {
			if _, err := compileSchema(v); err != nil {
				return err
			}
			next.FieldSchema = v
		}
		row := tx.QueryRow(ctx, `
			UPDATE node_kinds
			SET label = $1, short_prefix = $2, icon = $3, allowed_child_kinds = $4,
				field_schema = $5::jsonb, updated_at = now()
			WHERE id = $6::uuid
			RETURNING id::text, slug, label, short_prefix, icon, allowed_child_kinds, field_schema`,
			next.Label, next.ShortPrefix, next.Icon, next.AllowedChildKinds, string(next.FieldSchema), id)
		loaded, scanErr := scanKind(row)
		if scanErr != nil {
			return dbErr("update kind", scanErr)
		}
		if err := m.record(ctx, tx, p.ID, nil, evKindUpdated, current, loaded); err != nil {
			return err
		}
		kind = loaded
		return nil
	})
	return kind, err
}

func (m *Module) deleteKind(ctx context.Context, p tenant.Principal, id string) error {
	return m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		current, _, err := loadKind(ctx, tx, id)
		if err != nil {
			return err
		}
		var used int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE kind_id = $1::uuid`, id).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return conflict("kind is in use")
		}
		tag, err := tx.Exec(ctx, `DELETE FROM node_kinds WHERE id = $1::uuid`, id)
		if err != nil {
			if he := mapDB(err); he != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23503" {
					return conflict("kind is in use")
				}
				return he
			}
			return dbErr("delete kind", err)
		}
		if tag.RowsAffected() == 0 {
			return notFound("kind not found")
		}
		return m.record(ctx, tx, p.ID, nil, evKindDeleted, current, nil)
	})
}

func loadKind(ctx context.Context, tx pgx.Tx, id string) (kindJSON, *jsSchema, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text, slug, label, short_prefix, icon, allowed_child_kinds, field_schema
		FROM node_kinds WHERE id = $1::uuid`, id)
	kind, err := scanKind(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return kindJSON{}, nil, notFound("kind not found")
	}
	if err != nil {
		return kindJSON{}, nil, err
	}
	compiled, err := compileSchema(kind.FieldSchema)
	if err != nil {
		return kindJSON{}, nil, err
	}
	return kind, compiled, nil
}

func scanKind(row pgx.Row) (kindJSON, error) {
	var kind kindJSON
	var schema string
	if err := row.Scan(&kind.ID, &kind.Slug, &kind.Label, &kind.ShortPrefix, &kind.Icon, &kind.AllowedChildKinds, &schema); err != nil {
		return kindJSON{}, err
	}
	if schema == "" {
		schema = "{}"
	}
	kind.FieldSchema = json.RawMessage(schema)
	return kind, nil
}

func parsePatchString(raw json.RawMessage) (string, error) {
	if string(raw) == "null" {
		return "", badRequest("bad request")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", badRequest("bad request")
	}
	return s, nil
}

func (m *Module) record(ctx context.Context, tx pgx.Tx, actor string, nodeID *string, eventType string, before, after any) error {
	beforeRaw, err := snapshot(before)
	if err != nil {
		return err
	}
	afterRaw, err := snapshot(after)
	if err != nil {
		return err
	}
	return m.writeEvent(ctx, tx, Event{
		ActorPrincipalID: actor,
		NodeID:           nodeID,
		Type:             eventType,
		Before:           beforeRaw,
		After:            afterRaw,
	})
}
