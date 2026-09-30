// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

const codeKindChangeNotAllowed = "kind_change_not_allowed"

// refuseKindChange rejects a patch that names a different kind. The same kind
// is dropped and is not written. kindOnly is true when the patch named only
// the current kind, so the caller returns the node without an event.
func refuseKindChange(ctx context.Context, tx pgx.Tx, current nodeJSON, raw map[string]json.RawMessage) (kindOnly bool, err error) {
	_, hasID := raw["kind_id"]
	_, hasType := raw["type"]
	_, hasKind := raw["kind"]
	if !hasID && !hasType && !hasKind {
		return false, nil
	}
	requested, err := requestedKind(ctx, tx, raw)
	if err != nil {
		return false, err
	}
	if requested.ID != current.KindID {
		return false, conflictCoded("kind is immutable", codeKindChangeNotAllowed)
	}
	delete(raw, "kind_id")
	delete(raw, "type")
	delete(raw, "kind")
	for key := range raw {
		switch key {
		case "title", "body", "fields", "state":
			return false, nil
		}
	}
	return true, nil
}

func requestedKind(ctx context.Context, tx pgx.Tx, raw map[string]json.RawMessage) (kindJSON, error) {
	var id, slug string
	if v, ok := raw["kind_id"]; ok {
		s, err := parsePatchString(v)
		if err != nil {
			return kindJSON{}, err
		}
		parsed, ok := parseUUID(s)
		if !ok {
			return kindJSON{}, badRequest("invalid kind")
		}
		id = parsed
	}
	for _, key := range []string{"type", "kind"} {
		v, ok := raw[key]
		if !ok {
			continue
		}
		s, err := parsePatchString(v)
		if err != nil {
			return kindJSON{}, err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return kindJSON{}, badRequest("invalid type")
		}
		if slug != "" && slug != s {
			return kindJSON{}, badRequest("kind and type disagree")
		}
		slug = s
	}
	var byID, bySlug kindJSON
	if id != "" {
		loaded, _, err := loadKind(ctx, tx, id)
		if err != nil {
			return kindJSON{}, err
		}
		byID = loaded
	}
	if slug != "" {
		loaded, err := loadKindBySlug(ctx, tx, slug)
		if err != nil {
			return kindJSON{}, err
		}
		bySlug = loaded
	}
	if id != "" && slug != "" && byID.ID != bySlug.ID {
		return kindJSON{}, badRequest("kind and type disagree")
	}
	if id != "" {
		return byID, nil
	}
	return bySlug, nil
}

func loadKindBySlug(ctx context.Context, tx pgx.Tx, slug string) (kindJSON, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text, slug, label, short_prefix, icon, allowed_child_kinds, field_schema
		FROM node_kinds WHERE slug = $1`, slug)
	kind, err := scanKind(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return kindJSON{}, badRequest("unknown type")
	}
	if err != nil {
		return kindJSON{}, err
	}
	return kind, nil
}
