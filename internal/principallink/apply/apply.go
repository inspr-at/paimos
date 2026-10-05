// SPDX-License-Identifier: AGPL-3.0-only

// Package apply changes principal links inside an existing tenant transaction.
// It does not import the event or authorization packages, so both the operator
// CLI and the members API can share it.
package apply

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

type Person struct {
	ID       string
	Name     string
	Email    *string
	LinkedTo *string
}

type Outcome struct {
	Person  Person
	Changed bool
	Before  json.RawMessage
	After   json.RawMessage
}

// Lock takes the access fence before the principal-link advisory. Removing a
// binding invokes aeon_protect_last_owner, which also locks the tenant row.
// Callers that create an actor or accept an invite take this before those writes.
func Lock(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,532))`, tenantID)
	return err
}

// Apply links from to to. An empty to unlinks. Linking deletes the source's
// role bindings; an alias never keeps its own. Unlink may restore a classic
// workspace binding. Every deleted binding is audited as actorID in this
// transaction; the caller writes the principal link event. Legacy role labels
// are retained as import metadata, not access grants.
func Apply(ctx context.Context, tx pgx.Tx, tenantID, from, to, actorID string) (Outcome, error) {
	var out Outcome
	if strings.TrimSpace(from) == "" {
		return out, errors.New("from is required")
	}
	if err := Lock(ctx, tx, tenantID); err != nil {
		return out, err
	}
	source, err := lookup(ctx, tx, tenantID, from)
	if err != nil {
		return out, err
	}
	var target *string
	if to != "" {
		dest, err := lookup(ctx, tx, tenantID, to)
		if err != nil {
			return out, err
		}
		if dest.ID == source.ID || dest.LinkedTo != nil {
			return out, errors.New("self-links, chains and cycles are forbidden")
		}
		target = &dest.ID
		if source.LinkedTo != nil && *source.LinkedTo != dest.ID {
			return out, errors.New("principal already linked; unlink first")
		}
	}
	out.Person = source
	if (source.LinkedTo == nil && target == nil) || (source.LinkedTo != nil && target != nil && *source.LinkedTo == *target) {
		return out, nil
	}
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(p) FROM principals p WHERE tenant_id=$1 AND id=$2`, tenantID, source.ID).Scan(&out.Before); err != nil {
		return Outcome{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE principals SET linked_to=$3 WHERE tenant_id=$1 AND id=$2 RETURNING to_jsonb(principals)`, tenantID, source.ID, target).Scan(&out.After); err != nil {
		return Outcome{}, err
	}
	if target != nil {
		if err := removeBindings(ctx, tx, tenantID, source.ID, actorID); err != nil {
			return Outcome{}, err
		}
	} else if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_uninvited($1::uuid,$2::uuid)`, tenantID, source.ID); err != nil {
		return Outcome{}, err
	}
	out.Person.LinkedTo = target
	out.Changed = true
	return out, nil
}

func removeBindings(ctx context.Context, tx pgx.Tx, tenantID, principalID, actorID string) error {
	rows, err := tx.Query(ctx, `DELETE FROM role_bindings b USING roles r
		WHERE b.tenant_id=$1::uuid AND b.principal_id=$2::uuid
		  AND r.tenant_id=b.tenant_id AND r.id=b.role_id
		RETURNING jsonb_build_object(
		  'id',b.id,'principal_id',b.principal_id,'scope_type',b.scope_type,
		  'project_id',b.scope_id,'role',jsonb_build_object('id',r.id,'key',r.key,'name',r.name),
		  'reason','principal_alias_linked')`, tenantID, principalID)
	if err != nil {
		return err
	}
	var removed []json.RawMessage
	for rows.Next() {
		var before json.RawMessage
		if err := rows.Scan(&before); err != nil {
			rows.Close()
			return err
		}
		removed = append(removed, before)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Access audit is workspace activity, as for invite binding.set. The event
	// trigger records project_id from each snapshot in node_refs, preserving
	// project visibility even for the operator's workspace-only transaction.
	for _, before := range removed {
		if _, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,before,at)
			VALUES($1::uuid,$2::uuid,'binding.removed',$3::jsonb,clock_timestamp())`,
			tenantID, actorID, before); err != nil {
			return err
		}
	}
	return nil
}

func lookup(ctx context.Context, tx pgx.Tx, tenantID, ref string) (Person, error) {
	var p Person
	rows, err := tx.Query(ctx, `SELECT id::text,name,email,linked_to::text FROM principals
 WHERE tenant_id=$1 AND kind='person' AND (id::text=$2 OR name=$2) ORDER BY id LIMIT 2`, tenantID, ref)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&p.ID, &p.Name, &p.Email, &p.LinkedTo); err != nil {
			return p, err
		}
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	if count == 0 {
		return p, errors.New("person not found in tenant")
	}
	if count != 1 {
		return p, errors.New("ambiguous principal name; use an ID")
	}
	return p, nil
}
