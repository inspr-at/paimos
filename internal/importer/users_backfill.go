// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// BackfillPrincipals restores classic usernames, emails and missing native assignments from stored
// classic users/identities, without contacting classic. The coordinator can
// invoke this once per tenant after migration; replays append no events.
// Explicit native assignments (including null) and custom names are preserved.
func BackfillPrincipals(ctx context.Context, pool *pgxpool.Pool, tenantID string) (Report, error) {
	report := Report{Counts: map[string]int{}}
	if pool == nil || tenantID == "" {
		return report, errors.New("database pool and tenant id are required")
	}
	err := db.InTenant(db.AllProjects(ctx, "classic importer"), pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,42))`, tenantID+":principal-backfill"); err != nil {
			return err
		}
		// Serialize email repairs with invitation candidate selection, imports
		// and manual linking. Acquire this before taking any principal row locks.
		// Tree and tenant precede alias, since repairs also write node rows and
		// may create the import actor (whose FK takes a tenant key-share lock).
		if err := authz.LockProjectMutation(ctx, tx, tenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,532))`, tenantID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT p.id::text,p.name,i.display_name,p.email,i.email,u.after->'classic',u.after->'principal'->>'name',to_jsonb(p)
   FROM principals p JOIN identities i ON i.id=p.identity_id
   LEFT JOIN LATERAL (SELECT after FROM events WHERE tenant_id=p.tenant_id
    AND type IN ('import.user_created','import.user_updated')
    AND after->'principal'->>'id'=p.id::text ORDER BY id DESC LIMIT 1) u ON true
   WHERE p.tenant_id=$1 AND i.issuer='paimos-classic' ORDER BY p.id FOR UPDATE OF p`, tenantID)
		if err != nil {
			return err
		}
		type person struct {
			id, name              string
			display, importedName *string
			email, identityEmail  *string
			classic, before       []byte
		}
		people := []person{}
		for rows.Next() {
			var p person
			if err := rows.Scan(&p.id, &p.name, &p.display, &p.email, &p.identityEmail, &p.classic, &p.importedName, &p.before); err != nil {
				rows.Close()
				return err
			}
			people = append(people, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		actor := ""
		ensureActor := func() error {
			if actor != "" {
				return nil
			}
			var err error
			actor, err = ensureImportActor(ctx, tx, tenantID)
			return err
		}
		for _, p := range people {
			var classic Record
			if len(p.classic) > 0 {
				if err := json.Unmarshal(p.classic, &classic); err != nil {
					return err
				}
			}
			name := p.name
			username := stringField(classic, "username")
			if username != "" && (p.name == username || p.name == userFullName(classic) || (p.importedName != nil && p.name == *p.importedName) || (p.display != nil && p.name == *p.display)) {
				name = username
			}
			email := p.email
			if email == nil && p.identityEmail != nil {
				email = p.identityEmail
			}
			if email == nil {
				if value := stringField(classic, "email"); value != "" {
					email = &value
				}
			}
			sameEmail := (email == nil && p.email == nil) || (email != nil && p.email != nil && *email == *p.email)
			if name == p.name && sameEmail {
				continue
			}
			if err := ensureActor(); err != nil {
				return err
			}
			var after []byte
			if err := tx.QueryRow(ctx, `UPDATE principals SET name=$3,email=$4 WHERE tenant_id=$1 AND id=$2 RETURNING to_jsonb(principals)`, tenantID, p.id, name, email).Scan(&after); err != nil {
				return err
			}
			if _, err := events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{Type: "principal.updated", Before: json.RawMessage(p.before), After: json.RawMessage(after)}); err != nil {
				return err
			}
			report.Writes++
			if name != p.name {
				report.Counts["names"]++
			}
			if !sameEmail {
				report.Counts["emails"]++
			}
		}
		rows, err = tx.Query(ctx, `SELECT n.id::text,coalesce(p.linked_to,p.id)::text,to_jsonb(n) FROM nodes n
   JOIN identities i ON i.issuer='paimos-classic' AND i.subject=(n.fields->'classic'->>'source_id')||':'||(n.fields->'classic'->>'assignee_id')
   JOIN principals p ON p.identity_id=i.id AND p.tenant_id=n.tenant_id
   WHERE n.tenant_id=$1 AND NOT (n.fields ? 'assignee' OR n.fields ? 'assignee_id')
   ORDER BY n.id FOR UPDATE OF n`, tenantID)
		if err != nil {
			return err
		}
		type assignment struct {
			node, principal string
			before          []byte
		}
		assignments := []assignment{}
		for rows.Next() {
			var a assignment
			if err := rows.Scan(&a.node, &a.principal, &a.before); err != nil {
				rows.Close()
				return err
			}
			assignments = append(assignments, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, a := range assignments {
			if err := ensureActor(); err != nil {
				return err
			}
			var after []byte
			if err := tx.QueryRow(ctx, `UPDATE nodes SET fields=jsonb_set(fields,'{assignee}',to_jsonb($3::text)),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE tenant_id=$1 AND id=$2 RETURNING to_jsonb(nodes)`, tenantID, a.node, a.principal).Scan(&after); err != nil {
				return err
			}
			if _, err := events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{NodeID: &a.node, Type: "node.updated", Before: json.RawMessage(a.before), After: json.RawMessage(after)}); err != nil {
				return err
			}
			report.Writes++
			report.Counts["assignees"]++
		}
		return nil
	})
	report.Counts["writes"] = report.Writes
	return report, err
}
