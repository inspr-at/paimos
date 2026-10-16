// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

type kindSnapshot struct {
	ID                string          `json:"id"`
	Slug              string          `json:"slug"`
	Label             string          `json:"label"`
	Prefix            string          `json:"short_prefix"`
	Icon              string          `json:"icon"`
	AllowedChildKinds []string        `json:"allowed_child_kinds"`
	FieldSchema       json.RawMessage `json:"field_schema"`
}

type kindChange struct {
	Before *kindSnapshot
	After  kindSnapshot
}

// ensureKinds is called only inside an authorized create transaction, after
// its tenant fence and before any node or event writes. It returns changes for
// the caller to append as kind.created/kind.updated after taking all row locks.
// Repeated creates preserve tenant labels, prefixes, icons and custom fields.
// There is intentionally no startup or migration-time backfill.
func ensureKinds(ctx context.Context, tx pgx.Tx, tenantID string) (map[string]kindSnapshot, []kindChange, error) {
	kinds := map[string]kindSnapshot{}
	var changes []kindChange
	for _, spec := range []struct {
		slug, label, prefix, icon string
		children                  []string
	}{
		{GoalKind, "Goal", "GOAL", "goal", []string{OutcomeKind}},
		{OutcomeKind, "Outcome", "OUT", "list-checks", []string{}},
	} {
		schema := kindFieldSchema(spec.slug)
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO node_kinds
			(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds,field_schema)
			VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)
			ON CONFLICT (tenant_id,slug) DO NOTHING RETURNING id::text`,
			tenantID, spec.slug, spec.label, spec.prefix, spec.icon, spec.children, schema).Scan(&id)
		created := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
		kind, err := readKind(ctx, tx, tenantID, spec.slug)
		if err != nil {
			return nil, nil, err
		}
		if created {
			changes = append(changes, kindChange{After: kind})
		} else {
			before := kind
			// Restore only the model-owned properties and child-kind constraint.
			// Tenant-owned schema keys and presentation remain unchanged.
			tag, err := tx.Exec(ctx, `UPDATE node_kinds SET
				allowed_child_kinds=$3,
				field_schema=field_schema || jsonb_build_object('properties',
					coalesce(field_schema->'properties','{}'::jsonb) || ($4::jsonb->'properties')),
				updated_at=clock_timestamp()
				WHERE tenant_id=$1 AND slug=$2 AND (allowed_child_kinds IS DISTINCT FROM $3::text[]
				 OR field_schema->'properties' IS DISTINCT FROM
					(coalesce(field_schema->'properties','{}'::jsonb) || ($4::jsonb->'properties')))`,
				tenantID, spec.slug, spec.children, schema)
			if err != nil {
				return nil, nil, err
			}
			if tag.RowsAffected() != 0 {
				kind, err = readKind(ctx, tx, tenantID, spec.slug)
				if err != nil {
					return nil, nil, err
				}
				changes = append(changes, kindChange{Before: &before, After: kind})
			}
		}
		kinds[spec.slug] = kind
	}
	// An unrestricted release already accepts outcomes; a restricted release
	// gains outcome without losing any pre-existing permitted child kind.
	release, err := readKind(ctx, tx, tenantID, "release")
	if err != nil {
		return nil, nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE node_kinds SET
		allowed_child_kinds=array_append(allowed_child_kinds,'outcome'),updated_at=clock_timestamp()
		WHERE tenant_id=$1 AND slug='release' AND allowed_child_kinds IS NOT NULL
		 AND NOT ('outcome'=ANY(allowed_child_kinds))`, tenantID)
	if err != nil {
		return nil, nil, err
	}
	if tag.RowsAffected() != 0 {
		after, err := readKind(ctx, tx, tenantID, "release")
		if err != nil {
			return nil, nil, err
		}
		changes = append(changes, kindChange{Before: &release, After: after})
		release = after
	}
	kinds["release"] = release
	return kinds, changes, nil
}

func readKind(ctx context.Context, tx pgx.Tx, tenantID, slug string) (kindSnapshot, error) {
	var kind kindSnapshot
	err := tx.QueryRow(ctx, `SELECT id::text,slug,label,short_prefix,icon,allowed_child_kinds,field_schema
		FROM node_kinds WHERE tenant_id=$1 AND slug=$2`, tenantID, slug).Scan(
		&kind.ID, &kind.Slug, &kind.Label, &kind.Prefix, &kind.Icon, &kind.AllowedChildKinds, &kind.FieldSchema)
	return kind, err
}

func kindFieldSchema(slug string) json.RawMessage {
	text := func(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
	// fieldschema intentionally supports a bounded subset of JSON Schema;
	// RFC3339 parsing and the relative ETA bound are enforced by Validate.
	clock := text(40)
	principal := map[string]any{"type": "string", "pattern": uuidPattern.String()}
	properties := map[string]any{
		"eta_text": text(40), "eta_at": clock, "eta_set_by": principal, "eta_set_at": clock,
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if slug == OutcomeKind {
		properties["outcome_ref"] = map[string]any{"type": "string", "pattern": refPattern.String()}
		properties["note"] = text(200)
		properties["state_set"] = map[string]any{"type": "string", "enum": []string{"moving", "queued", "paused", "done"}}
		properties["handed_over_at"] = clock
		properties["handed_over_by"] = principal
		properties["handed_over_detail"] = text(2000)
		schema["required"] = []string{"outcome_ref"}
	} else {
		properties["steps"] = map[string]any{"type": "array", "maxItems": MaxSteps, "items": map[string]any{
			"type": "object", "required": []string{"id", "text", "state"}, "additionalProperties": false,
			"properties": map[string]any{
				"id":    map[string]any{"type": "string", "pattern": stepPattern.String()},
				"text":  map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
				"state": map[string]any{"type": "string", "enum": []string{"done", "now", "later"}},
			},
		}}
		properties["progress_override_pct"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 100}
		properties["progress_override_note"] = text(200)
		properties["progress_override_by"] = principal
		properties["progress_override_at"] = clock
		properties["retired_refs"] = map[string]any{"type": "array", "maxItems": MaxRetiredRefs,
			"items": map[string]any{"type": "string", "pattern": refPattern.String()}}
	}
	// All values above are JSON primitives; failure is a programmer error.
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return raw
}
