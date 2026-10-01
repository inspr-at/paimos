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

// Apply links from to to. An empty to unlinks. Linking deletes the source's
// role bindings; an alias never keeps its own. Unlink may restore a classic
// workspace binding. The caller writes the event.
func Apply(ctx context.Context, tx pgx.Tx, tenantID, from, to string) (Outcome, error) {
	var out Outcome
	if strings.TrimSpace(from) == "" {
		return out, errors.New("from is required")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,532))`, tenantID); err != nil {
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
		if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid`, tenantID, source.ID); err != nil {
			return Outcome{}, err
		}
	} else if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_uninvited($1::uuid,$2::uuid)`, tenantID, source.ID); err != nil {
		return Outcome{}, err
	}
	out.Person.LinkedTo = target
	out.Changed = true
	return out, nil
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
