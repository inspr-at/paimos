// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/fieldschema"
	"github.com/jackc/pgx/v5"
)

// migrateWorkNodes is part of 1215's transaction. FORCE RLS remains enabled.
// The rollout requires a drained queue, stopped writers and a verified backup.
// All tenant/tree/resource locks precede the final event flush. A failure in any
// tenant rolls back every tenant, starter seeding and the migration record.
func migrateWorkNodes(ctx context.Context, tx pgx.Tx) error {
	ctx = AllProjects(ctx, "AEON-649 work-kind migration")
	// No permanent provenance column: retain exact fields and use append-only
	// snapshots to explain the kind change and to reconcile importer baselines.
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE aeon_work_migration_events (
 tenant_id uuid, actor_id uuid, node_id uuid, before_row jsonb, after_row jsonb,
 legacy_kind_slug text) ON COMMIT DROP`); err != nil {
		return err
	}
	// Tenants are locked in bounded keyset pages before resource rows. Seeding
	// new tenants waits on this table lock until the new starter function commits.
	if _, err := tx.Exec(ctx, `LOCK TABLE tenants IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	if err := workTenantPages(ctx, tx, func(id string) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||$1,0))`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, id)
		return err
	}); err != nil {
		return err
	}
	// Identity-preserving kind substitution also covers tombstones under deleted
	// parents. Tree validation rejects those historical links on a kind UPDATE.
	// Freeze writers, disable just that validator during this exact substitution,
	// and restore it in this same transaction. No link/state/field changes occur.
	if _, err := tx.Exec(ctx, `ALTER TABLE nodes DISABLE TRIGGER nodes_tree_guard`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE harness_sessions, agent_runs, work_orders IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	if err := workTenantPages(ctx, tx, func(id string) error {
		if err := enterTenant(ctx, tx, id); err != nil {
			return err
		}
		return migrateTenantWorkNodes(ctx, tx, id)
	}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE nodes ENABLE TRIGGER nodes_tree_guard`); err != nil {
		return err
	}
	// Event counter is last. No resource lock or node mutation follows this pass.
	return workTenantPages(ctx, tx, func(id string) error {
		if err := enterTenant(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,before,after)
   SELECT tenant_id,actor_id,node_id,'node.work_kind_migrated',before_row,
    after_row || jsonb_build_object('legacy_kind_slug',legacy_kind_slug,'migration','AEON-649','rollback','verified_backup')
   FROM aeon_work_migration_events WHERE tenant_id=$1::uuid ORDER BY node_id`, id)
		return err
	})
}

func workTenantPages(ctx context.Context, tx pgx.Tx, visit func(string) error) error {
	after := "00000000-0000-0000-0000-000000000000"
	for {
		rows, err := tx.Query(ctx, `SELECT id::text FROM tenants WHERE id>$1::uuid ORDER BY id LIMIT 100`, after)
		if err != nil {
			return err
		}
		ids := []string{}
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
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := visit(id); err != nil {
				return err
			}
		}
		after = ids[len(ids)-1]
	}
}

func migrateTenantWorkNodes(ctx context.Context, tx pgx.Tx, tenantID string) error {
	rows, err := tx.Query(ctx, `SELECT slug,
 CASE WHEN octet_length(field_schema::text)<=1048576 THEN field_schema ELSE NULL END FROM node_kinds
  WHERE tenant_id=$1 AND slug IN ('epic','ticket','task','work') ORDER BY slug FOR UPDATE`, tenantID)
	if err != nil {
		return err
	}
	schemas := map[string]json.RawMessage{}
	for rows.Next() {
		var slug string
		var raw []byte
		if err := rows.Scan(&slug, &raw); err != nil {
			rows.Close()
			return err
		}
		if raw == nil {
			rows.Close()
			return fmt.Errorf("tenant %s: %s field schema exceeds 1 MiB; reconcile before migration", tenantID, slug)
		}
		schemas[slug] = raw
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	schema, err := mergeWorkSchemas(schemas)
	if err != nil {
		return fmt.Errorf("tenant %s: %w", tenantID, err)
	}
	// A reserved slug must never silently absorb a custom, non-work kind.
	if _, exists := schemas["work"]; exists {
		return fmt.Errorf("tenant %s: work kind already exists; reconcile reserved slug before migration", tenantID)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,field_schema)
  VALUES($1,'work','Work item','TKT','ticket',$2::jsonb)`, tenantID, string(schema)); err != nil {
		return err
	}
	// Schema-extension triggers add server properties to work. They must not
	// silently overwrite a legacy tenant definition, including on tenants that
	// had removed ticket/task. Validate against the actual stored schema.
	var overwritten bool
	var storedSchema []byte
	if err := tx.QueryRow(ctx, `SELECT field_schema,
 EXISTS(SELECT 1 FROM jsonb_each($2::jsonb->'properties') p
  WHERE field_schema->'properties'->p.key IS DISTINCT FROM p.value)
 FROM node_kinds WHERE tenant_id=$1 AND slug='work'`, tenantID, string(schema)).Scan(&storedSchema, &overwritten); err != nil {
		return err
	}
	if overwritten {
		return fmt.Errorf("tenant %s: work schema extension would overwrite a legacy property; reconcile before migration", tenantID)
	}
	if err := validateWorkMigrationFields(ctx, tx, tenantID, storedSchema); err != nil {
		return err
	}
	// Other service kinds (including desk kinds) retain IDs and field schemas.
	// The desk guard requires its scoped service flag when its child references
	// need rewriting; restore the previous flag immediately after that rewrite.
	var priorDesk string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.desk_write',true),'')`).Scan(&priorDesk); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.desk_write','on',true)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE node_kinds k SET allowed_child_kinds=ARRAY(
  SELECT DISTINCT CASE WHEN child IN ('epic','ticket','task') THEN 'work' ELSE child END
  FROM unnest(k.allowed_child_kinds) child ORDER BY 1)
  WHERE k.tenant_id=$1 AND k.allowed_child_kinds && ARRAY['epic','ticket','task']`, tenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.desk_write',$1,true)`, priorDesk); err != nil {
		return err
	}
	// Work permits nested work, while keeping every non-work child policy that
	// the legacy definitions allowed. NULL remains unrestricted.
	if _, err := tx.Exec(ctx, `UPDATE node_kinds SET allowed_child_kinds=CASE
  WHEN EXISTS(SELECT 1 FROM node_kinds WHERE slug IN ('epic','ticket','task') AND allowed_child_kinds IS NULL)
   OR NOT EXISTS(SELECT 1 FROM node_kinds WHERE slug IN ('epic','ticket','task')) THEN NULL
  ELSE ARRAY(SELECT DISTINCT child FROM (
   SELECT unnest(allowed_child_kinds) child FROM node_kinds WHERE slug IN ('epic','ticket','task')
   UNION ALL SELECT 'work') children ORDER BY child) END WHERE tenant_id=$1 AND slug='work'`, tenantID); err != nil {
		return err
	}
	// Only direct, live work children make a parent. A work order, knowledge
	// entry or Decision Desk child alone does not trigger the busy-leaf guard.
	parents, err := busyWorkParents(ctx, tx, tenantID, "00000000-0000-0000-0000-000000000000", 101)
	if err != nil {
		return err
	}
	if len(parents) > 0 {
		blocked := &BusyWorkParentsError{Parents: parents, Truncated: len(parents) > 100}
		if blocked.Truncated {
			blocked.Parents = parents[:100]
		}
		return blocked
	}
	// One keyless migration actor per tenant. It is not a dispatcher identity.
	var actor string
	if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','AEON-649 work-kind migration') RETURNING id::text`, tenantID).Scan(&actor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO aeon_work_migration_events(tenant_id,actor_id,node_id,before_row,legacy_kind_slug)
  SELECT n.tenant_id,$2::uuid,n.id,to_jsonb(n),k.slug FROM nodes n
   JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.tenant_id=$1 AND k.slug IN ('epic','ticket','task')`, tenantID, actor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE nodes n SET kind_id=w.id FROM node_kinds old,node_kinds w
  WHERE n.tenant_id=$1 AND old.tenant_id=n.tenant_id AND old.id=n.kind_id AND old.slug IN ('epic','ticket','task')
   AND w.tenant_id=n.tenant_id AND w.slug='work'`, tenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE aeon_work_migration_events e SET after_row=to_jsonb(n)
  FROM nodes n WHERE e.tenant_id=$1 AND n.tenant_id=e.tenant_id AND n.id=e.node_id`, tenantID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM node_kinds WHERE tenant_id=$1 AND slug IN ('epic','ticket','task')`, tenantID)
	return err
}

// Merge properties and workflow catalogs without converting or overwriting
// values. Conflicting validation rules stop rollout for explicit reconciliation.
// Required sets must agree (including absent sets); making a formerly optional
// field required on another kind would invalidate historical nodes.
func mergeWorkSchemas(schemas map[string]json.RawMessage) ([]byte, error) {
	merged := map[string]any{}
	properties := map[string]any{}
	states := map[string]any{}
	stateSeparator := regexp.MustCompile(`[[:space:]-]+`)
	var required any
	first := true
	slugs := make([]string, 0, len(schemas))
	for slug := range schemas {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		raw := schemas[slug]
		if len(raw) > 1<<20 {
			return nil, fmt.Errorf("%s field schema exceeds 1 MiB", slug)
		}
		var schema map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&schema); err != nil {
			return nil, err
		}
		req := schema["required"]
		if req == nil {
			req = []any{}
		}
		if list, ok := req.([]any); ok {
			sort.Slice(list, func(i, j int) bool { return fmt.Sprint(list[i]) < fmt.Sprint(list[j]) })
		}
		if !first && !reflect.DeepEqual(required, req) {
			return nil, fmt.Errorf("%s: incompatible required fields; reconcile schemas before migration", slug)
		}
		required = req
		first = false
		for key, value := range schema {
			switch key {
			case "properties":
				props, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s: properties must be an object", slug)
				}
				for name, prop := range props {
					if prior, exists := properties[name]; exists && !reflect.DeepEqual(prior, prop) {
						return nil, fmt.Errorf("%s: incompatible property %q; reconcile schemas before migration", slug, name)
					}
					properties[name] = prop
				}
			case "states":
				list, ok := value.([]any)
				if !ok {
					return nil, fmt.Errorf("%s: states must be an array", slug)
				}
				for _, item := range list {
					obj, ok := item.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("%s: invalid state", slug)
					}
					name, ok := obj["state"].(string)
					if !ok {
						return nil, fmt.Errorf("%s: state name missing", slug)
					}
					name = stateSeparator.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
					if prior, exists := states[name]; exists && !reflect.DeepEqual(prior, item) {
						return nil, fmt.Errorf("%s: incompatible state %q; reconcile schemas before migration", slug, name)
					}
					states[name] = item
				}
			case "required":
			case "issue_family":
				if value != true {
					return nil, fmt.Errorf("%s: work kind explicitly excluded from issue family; reconcile before migration", slug)
				}
			default:
				if prior, exists := merged[key]; exists && !reflect.DeepEqual(prior, value) {
					return nil, fmt.Errorf("%s: incompatible schema constraint %q; reconcile schemas before migration", slug, key)
				}
				merged[key] = value
			}
		}
	}
	merged["properties"] = properties
	if list, ok := required.([]any); ok && len(list) > 0 {
		merged["required"] = list
	}
	if len(states) > 0 {
		names := make([]string, 0, len(states))
		for name := range states {
			names = append(names, name)
		}
		sort.Strings(names)
		list := []any{}
		for _, name := range names {
			list = append(list, states[name])
		}
		merged["states"] = list
	}
	merged["issue_family"] = true
	return json.Marshal(merged)
}

// Check historical fields against the reconciled constraints before changing
// anything. This catches a strict schema on one kind that would reject fields
// from another kind. Read bounded pages and reject oversized payloads in SQL,
// before materializing them. Values are validated, never converted or stored.
// Use the application's compiler and json.Number/big.Rat comparisons so valid
// large integers and precise decimals cannot be rounded into a schema violation.
func validateWorkMigrationFields(ctx context.Context, tx pgx.Tx, tenantID string, raw []byte) error {
	resolved, err := fieldschema.Compile(raw)
	if err != nil {
		return fmt.Errorf("tenant %s: invalid merged work schema", tenantID)
	}
	after := "00000000-0000-0000-0000-000000000000"
	for {
		rows, err := tx.Query(ctx, `SELECT n.id::text,n.key,
 CASE WHEN octet_length(n.fields::text)<=1048576 THEN n.fields ELSE NULL END
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND k.slug IN ('epic','ticket','task') AND n.id>$2::uuid
 ORDER BY n.id LIMIT 50`, tenantID, after)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var id, key string
			var fields []byte
			if err := rows.Scan(&id, &key, &fields); err != nil {
				rows.Close()
				return err
			}
			if fields == nil {
				rows.Close()
				return fmt.Errorf("tenant %s: %s fields exceed migration validation bound (1 MiB); reconcile before migration", tenantID, key)
			}
			value, err := fieldschema.Decode(fields)
			if err != nil {
				rows.Close()
				return err
			}
			if err := resolved.Validate(value); err != nil {
				rows.Close()
				// The validator can include values; report the key, never content.
				return fmt.Errorf("tenant %s: %s fields do not satisfy the merged work schema; reconcile before migration", tenantID, key)
			}
			after = id
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
	}
}
