// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

// PostgresWriter targets the R1 nodes, relations and events schema.
type PostgresWriter struct{ Pool *pgxpool.Pool }

func (w PostgresWriter) Write(ctx context.Context, s Snapshot, tenantSlug string) (Report, error) {
	r := Analyze(s)
	if w.Pool == nil {
		return r, errors.New("database pool is required")
	}
	if s.SourceID == "" {
		return r, errors.New("source identity is required")
	}
	tenantID, err := tenantbootstrap.ResolveSlug(ctx, w.Pool, tenantSlug)
	if err != nil {
		return r, fmt.Errorf("resolve tenant: %w", err)
	}
	err = db.InTenant(db.AllProjects(ctx, "classic importer"), w.Pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,42))`, tenantID+":"+s.SourceID); err != nil {
			return err
		}
		actor, users, err := importUsers(ctx, tx, tenantID, s, &r.Conflicts)
		if err != nil {
			return err
		}
		kinds := map[string]string{}
		kindID := func(slug string) (string, error) {
			if id := kinds[slug]; id != "" {
				return id, nil
			}
			var id string
			prefix := map[string]string{"project": "PRJ", "work": "TKT", "release": "REL", "sprint": "SPR", "cost_unit": "CU", "memory": "MEM", "runbook": "RUN", "guideline": "GUI", "external_system": "EXT", "related_project": "RPR"}[slug]
			if prefix == "" {
				return "", fmt.Errorf("unsupported classic issue type %q", slug)
			}
			tag, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,slug) DO NOTHING`, tenantID, slug, strings.ReplaceAll(slug, "_", " "), prefix, slug)
			if err != nil {
				return "", err
			}
			if err := tx.QueryRow(ctx, `SELECT id FROM node_kinds WHERE tenant_id=$1 AND slug=$2`, tenantID, slug).Scan(&id); err != nil {
				return "", err
			}
			if tag.RowsAffected() != 0 {
				if _, err := events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{
					Type: "import.kind_created", After: map[string]any{"id": id, "slug": slug, "short_prefix": prefix, "source_id": s.SourceID},
				}); err != nil {
					return "", err
				}
			}
			kinds[slug] = id
			return id, nil
		}
		projectIDs := map[int64]string{}
		issueIDs := map[int64]string{}
		issueRows := map[int64]Record{}
		for _, p := range s.Projects {
			pid, _ := intField(p.Record, "id")
			kind, err := kindID("project")
			if err != nil {
				return err
			}
			key := "PRJ-" + strconv.FormatInt(pid, 10)
			id, created, updated, err := upsertNode(ctx, tx, tenantID, s.SourceID, kind, key, stringField(p.Record, "name"), stringField(p.Record, "description"), stringField(p.Record, "status"), "", p.Record, principalRefs(p.Record, users, "product_owner"), actor, true, &r.Conflicts)
			if err != nil {
				return fmt.Errorf("project %d: %w", pid, err)
			}
			projectIDs[pid] = id
			if created {
				r.Created++
			}
			if updated {
				r.Updated++
			}
		}
		for _, item := range s.Orphans {
			iid, ok := intField(item, "id")
			if !ok {
				return errors.New("orphan issue missing id")
			}
			key := stringField(item, "issue_key")
			if key == "" {
				if stringField(item, "type") != "sprint" {
					return fmt.Errorf("orphan issue %d missing issue_key", iid)
				}
				key = "SPRINT-" + strconv.FormatInt(iid, 10)
			}
			kind, err := kindID(canonicalType(stringField(item, "type")))
			if err != nil {
				return err
			}
			id, created, updated, err := upsertNode(ctx, tx, tenantID, s.SourceID, kind, key, stringField(item, "title"), issueBody(item), stringField(item, "status"), "", item, principalRefs(item, users, "assignee_id", "created_by", "accepted_by", "deleted_by"), actor, false, &r.Conflicts)
			if err != nil {
				return fmt.Errorf("orphan issue %d: %w", iid, err)
			}
			issueIDs[iid] = id
			issueRows[iid] = item
			if created {
				r.Created++
			}
			if updated {
				r.Updated++
			}
		}
		for _, p := range s.Projects {
			pid, _ := intField(p.Record, "id")
			for _, item := range p.Issues {
				iid, ok := intField(item, "id")
				if !ok {
					return errors.New("issue missing id")
				}
				key := stringField(item, "issue_key")
				if key == "" {
					return fmt.Errorf("issue %d missing issue_key", iid)
				}
				kind, err := kindID(canonicalType(stringField(item, "type")))
				if err != nil {
					return err
				}
				id, created, updated, err := upsertNode(ctx, tx, tenantID, s.SourceID, kind, key, stringField(item, "title"), issueBody(item), stringField(item, "status"), projectIDs[pid], item, principalRefs(item, users, "assignee_id", "created_by", "accepted_by", "deleted_by"), actor, false, &r.Conflicts)
				if err != nil {
					return fmt.Errorf("issue %s: %w", key, err)
				}
				issueIDs[iid] = id
				issueRows[iid] = item
				if created {
					r.Created++
				}
				if updated {
					r.Updated++
				}
			}
		}
		// Resolve classic issue parents after every node exists. Project parent is
		// the fallback for missing or out-of-scope ancestors.
		for iid, item := range issueRows {
			if importKindConflict(r.Conflicts, stringField(item, "issue_key")) {
				continue
			}
			parentSource, ok := intField(item, "parent_id")
			if !ok {
				continue
			}
			parentID := issueIDs[parentSource]
			if parentID == "" {
				continue
			}
			diverged, err := importNodeDiverged(ctx, tx, tenantID, issueIDs[iid])
			if err != nil {
				return err
			}
			if diverged {
				r.Conflicts = appendConflict(r.Conflicts, ImportConflict{ClassicID: iid, Key: stringField(item, "issue_key"), Reason: "Aeon node changed since last import"})
				continue
			}
			if _, err := setParent(ctx, tx, tenantID, actor, issueIDs[iid], parentID); err != nil {
				return fmt.Errorf("parent for %d: %w", iid, err)
			}
		}
		for iid, d := range s.Details {
			nodeID := issueIDs[iid]
			if nodeID == "" {
				continue
			}
			for _, rel := range d.Relations {
				sourceID, _ := intField(rel, "source_id")
				targetID, _ := intField(rel, "target_id")
				// A recorded kind conflict leaves that node untouched. Skip a
				// relation listed on it or aimed at it, including parent_id.
				if relationTouchesKindConflict(r.Conflicts, issueRows, iid, sourceID, targetID) {
					continue
				}
				typ := stringField(rel, "type")
				ref, err := importEvent(ctx, tx, tenantID, actor, nodeID, "import.relation", s.SourceID, rel, "id", typ+":"+strconv.FormatInt(sourceID, 10)+":"+strconv.FormatInt(targetID, 10))
				if err != nil {
					return err
				}
				if typ == "parent" && issueIDs[targetID] != "" {
					diverged, err := importNodeDiverged(ctx, tx, tenantID, issueIDs[targetID])
					if err != nil {
						return err
					}
					if diverged {
						r.Conflicts = appendConflict(r.Conflicts, ImportConflict{ClassicID: targetID, Key: stringField(issueRows[targetID], "issue_key"), Reason: "Aeon node changed since last import"})
						continue
					}
				}
				wrote, err := applyClassicRelation(ctx, tx, tenantID, actor, classicRelation{
					Type:         typ,
					SourceNodeID: issueIDs[sourceID],
					TargetNodeID: issueIDs[targetID],
					ClassicRef:   ref,
				})
				if errors.Is(err, errReleasesModeSkipped) {
					r.Counts["release_links_skipped"]++
					continue
				}
				if err != nil {
					return fmt.Errorf("relation %s %d→%d: %w", typ, sourceID, targetID, err)
				}
				r.Writes += wrote
			}
			for _, group := range []struct {
				typ  string
				rows []Record
			}{{"import.comment", d.Comments}, {"import.history", d.History}, {"import.attachment", d.Attachments}} {
				for _, record := range group.rows {
					if _, err := importEvent(ctx, tx, tenantID, actor, nodeID, group.typ, s.SourceID, record, "id", ""); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	// ANALYZE uses the owning role after the write transaction commits. It is
	// database-wide maintenance, not a tenant data query, and cannot see the
	// just-written rows while the import transaction is still open.
	if _, err := w.Pool.Exec(ctx, `ANALYZE nodes, node_kinds, node_relations, node_key_counters,
            principals, identities, events, event_counters,
            journey_projects, journey_releases, journey_tickets`); err != nil {
		return r, fmt.Errorf("analyze imported tables: %w", err)
	}
	return r, nil
}

func importUsers(ctx context.Context, tx pgx.Tx, tenantID string, s Snapshot, conflicts *[]ImportConflict) (string, map[int64]string, error) {
	// Imports later write tree rows and bindings. Match project writes and
	// invite/link enrollment: tree, tenant, alias, then principal/resource rows.
	if err := authz.LockProjectMutation(ctx, tx, tenantID); err != nil {
		return "", nil, err
	}
	// Share the invite/link lock before reading or inserting people. Row locks
	// alone cannot stop a new matching email from making a candidate ambiguous.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,532))`, tenantID); err != nil {
		return "", nil, err
	}
	users, err := storedUserRefs(ctx, tx, tenantID, s.SourceID)
	if err != nil {
		return "", nil, err
	}
	actor, err := ensureImportActor(ctx, tx, tenantID)
	if err != nil {
		return "", nil, err
	}
	for _, u := range s.Users {
		id, ok := intField(u, "id")
		if !ok {
			return "", nil, errors.New("user missing id")
		}
		subject := s.SourceID + ":" + strconv.FormatInt(id, 10)
		name := userDisplayName(u)
		if name == "" {
			return "", nil, fmt.Errorf("user %d missing name", id)
		}
		var identityID, principalID string
		var before []byte
		err := tx.QueryRow(ctx, `SELECT jsonb_build_object('identity',to_jsonb(i),'principal',to_jsonb(p))
			FROM identities i LEFT JOIN principals p ON p.identity_id=i.id AND p.tenant_id=$1::uuid
			WHERE i.issuer='paimos-classic' AND i.subject=$2`, tenantID, subject).Scan(&before)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", nil, err
		}
		if len(before) != 0 {
			var existing struct {
				Principal struct {
					ID string `json:"id"`
				} `json:"principal"`
			}
			if err := json.Unmarshal(before, &existing); err != nil {
				return "", nil, err
			}
			if existing.Principal.ID != "" {
				var previous []byte
				err = tx.QueryRow(ctx, `SELECT after FROM events WHERE tenant_id=$1
			 AND type IN ('import.user_created','import.user_updated')
			 AND after->'principal'->>'id'=$2
			 ORDER BY id DESC LIMIT 1`, tenantID, existing.Principal.ID).Scan(&previous)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return "", nil, err
				}
				diverged, err := userImportDiverged(before, previous)
				if err != nil {
					return "", nil, err
				}
				if diverged {
					*conflicts = appendConflict(*conflicts, ImportConflict{ClassicID: id, Key: "user:" + strconv.FormatInt(id, 10), Reason: "Aeon principal changed since last import"})
					continue
				}
			}
		}
		createdAt := parseClassicTime(stringField(u, "created_at"))
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email,display_name,created_at) VALUES('paimos-classic',$1,$2,$3,coalesce($4::timestamptz,now())) ON CONFLICT(issuer,subject) DO UPDATE SET email=coalesce(EXCLUDED.email,identities.email),display_name=EXCLUDED.display_name RETURNING id`, subject, nullString(stringField(u, "email")), name, createdAt).Scan(&identityID); err != nil {
			return "", nil, err
		}
		roles := []string{stringField(u, "role")}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,identity_id,name,roles,created_at,email) VALUES($1,'person',$2,$3,$4,coalesce($5::timestamptz,now()),$6) ON CONFLICT(tenant_id,identity_id) WHERE identity_id IS NOT NULL DO UPDATE SET name=EXCLUDED.name,email=coalesce(EXCLUDED.email,principals.email) RETURNING id`, tenantID, identityID, name, roles, createdAt, nullString(stringField(u, "email"))).Scan(&principalID); err != nil {
			return "", nil, err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_uninvited($1::uuid,$2::uuid)`, tenantID, principalID); err != nil {
			return "", nil, err
		}
		var after []byte
		if err := tx.QueryRow(ctx, `SELECT jsonb_build_object('identity',to_jsonb(i),'principal',to_jsonb(p))
			FROM identities i JOIN principals p ON p.identity_id=i.id AND p.tenant_id=$1::uuid
			WHERE i.id=$2::uuid`, tenantID, identityID).Scan(&after); err != nil {
			return "", nil, err
		}
		changed := !bytes.Equal(before, after)
		var snapshot Record
		if err := json.Unmarshal(after, &snapshot); err != nil {
			return "", nil, err
		}
		snapshot["classic"] = storedUser(u, s.SourceID)
		after, err = json.Marshal(snapshot)
		if err != nil {
			return "", nil, err
		}
		// Include the latest retained user metadata in the idempotency check.
		var previous []byte
		err = tx.QueryRow(ctx, `SELECT after FROM events WHERE tenant_id=$1
		 AND type IN ('import.user_created','import.user_updated')
		 AND after->'principal'->>'id'=$2 ORDER BY id DESC LIMIT 1`, tenantID, principalID).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", nil, err
		}
		var prev, next any
		_ = json.Unmarshal(previous, &prev)
		_ = json.Unmarshal(after, &next)
		if changed || !reflect.DeepEqual(prev, next) {
			typ := "import.user_updated"
			if len(before) == 0 {
				typ = "import.user_created"
			}
			if _, err := events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{
				Type: typ, Before: rawSnapshot(before), After: json.RawMessage(after),
			}); err != nil {
				return "", nil, err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE tenant_id=$1 AND id=$2`, tenantID, principalID).Scan(&principalID); err != nil {
			return "", nil, err
		}
		users[id] = principalID
	}
	return actor, users, nil
}

func userImportDiverged(current, imported []byte) (bool, error) {
	if len(imported) == 0 {
		return true, nil
	}
	var have, baseline map[string]map[string]any
	if err := json.Unmarshal(current, &have); err != nil {
		return false, err
	}
	if err := json.Unmarshal(imported, &baseline); err != nil {
		return false, err
	}
	for section, fields := range map[string][]string{
		"identity":  {"email", "display_name"},
		"principal": {"name", "email", "roles"},
	} {
		for _, field := range fields {
			if !reflect.DeepEqual(have[section][field], baseline[section][field]) {
				return true, nil
			}
		}
	}
	return false, nil
}

func ensureImportActor(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var actor string
	err := tx.QueryRow(ctx, `SELECT id FROM principals WHERE tenant_id=$1 AND kind='agent' AND name='Classic Paimos importer' ORDER BY created_at LIMIT 1`, tenantID).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent','Classic Paimos importer',ARRAY['importer']) RETURNING id`, tenantID).Scan(&actor)
		if err == nil {
			_, err = events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{
				Type: "import.actor_created", After: map[string]any{"principal_id": actor},
			})
		}
	}
	return actor, err
}

func upsertNode(ctx context.Context, tx pgx.Tx, tenantID, sourceID, kindID, key, title, body, state, parentID string, original, refs Record, actor string, project bool, conflicts *[]ImportConflict) (string, bool, bool, error) {
	if title == "" {
		return "", false, false, errors.New("title is empty")
	}
	state = canonicalState(state)
	fields := mappedFields(original, refs, sourceID, project)
	bodyJSON, err := jsonValue(fields)
	if err != nil {
		return "", false, false, err
	}
	var id, oldTitle, oldBody, oldState, oldKindID, oldKind string
	var oldFields, beforeJSON []byte
	err = tx.QueryRow(ctx, `SELECT n.id,n.title,n.body,n.state,n.fields,to_jsonb(n),n.kind_id::text,k.slug
		FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=$1 AND n.key=$2`, tenantID, key).Scan(&id, &oldTitle, &oldBody, &oldState, &oldFields, &beforeJSON, &oldKindID, &oldKind)
	created := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !created {
		return "", false, false, err
	}
	if !created {
		var old map[string]any
		if err := decodeExactJSON(oldFields, &old); err != nil {
			return "", false, false, err
		}
		classic, _ := old["classic"].(map[string]any)
		if classic["source_id"] != sourceID {
			return "", false, false, fmt.Errorf("key %s belongs to another source", key)
		}
		if !strings.EqualFold(oldKindID, kindID) {
			var requested string
			if err := tx.QueryRow(ctx, `SELECT slug FROM node_kinds WHERE tenant_id=$1 AND id=$2`, tenantID, kindID).Scan(&requested); err != nil {
				return "", false, false, err
			}
			classicID, _ := intField(original, "id")
			*conflicts = appendConflict(*conflicts, ImportConflict{
				ClassicID: classicID, Key: key, Reason: "kind_change_not_allowed",
				CurrentKind: oldKind, RequestedKind: requested,
			})
			return id, false, false, nil
		}
		parent, err := db.WorkStatusParentTx(ctx, tx, id)
		if err != nil {
			return "", false, false, err
		}
		if parent && state != oldState {
			classicID, _ := intField(original, "id")
			*conflicts = appendConflict(*conflicts, ImportConflict{ClassicID: classicID, Key: key, Reason: "parent_status_derived"})
			// Preserve the canonical state while still importing permitted
			// content. The source status remains in its classic provenance.
			state = oldState
		}
		var now any
		var prior any
		if err := decodeExactJSON(bodyJSON, &now); err != nil {
			return "", false, false, err
		}
		if err := decodeExactJSON(oldFields, &prior); err != nil {
			return "", false, false, err
		}
		if oldTitle == title && oldBody == body && oldState == state && reflect.DeepEqual(prior, now) {
			return id, false, false, nil
		}
		diverged, err := importNodeDiverged(ctx, tx, tenantID, id)
		if err != nil {
			return "", false, false, err
		}
		if diverged {
			classicID, _ := intField(original, "id")
			*conflicts = appendConflict(*conflicts, ImportConflict{ClassicID: classicID, Key: key, Reason: "Aeon node changed since last import"})
			return id, false, false, nil
		}
		if err = delivery.RefuseReleaseNodes(ctx, tx, []string{id}); errors.Is(err, delivery.ErrReleaseAPI) {
			classicID, _ := intField(original, "id")
			*conflicts = appendConflict(*conflicts, ImportConflict{ClassicID: classicID, Key: key, Reason: "release_api"})
			return id, false, false, nil
		} else if err != nil {
			return "", false, false, err
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET title=$3,body=$4,state=$5,fields=$6::jsonb,updated_at=coalesce($7::timestamptz,now()) WHERE tenant_id=$1 AND id=$2`, tenantID, id, title, body, state, string(bodyJSON), parseClassicTime(stringField(original, "updated_at")))
		if err != nil {
			return "", false, false, forbidPortal(err)
		}
	} else {
		createdAt := parseClassicTime(stringField(original, "created_at"))
		updatedAt := parseClassicTime(stringField(original, "updated_at"))
		err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,body,state,parent_id,fields,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,coalesce($9::timestamptz,now()),coalesce($10::timestamptz,now())) RETURNING id`, tenantID, key, kindID, title, body, state, nullString(parentID), string(bodyJSON), createdAt, updatedAt).Scan(&id)
		if err != nil {
			return "", false, false, err
		}
	}
	var afterJSON []byte
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&afterJSON); err != nil {
		return "", false, false, err
	}
	_, err = events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{
		NodeID: &id, Type: map[bool]string{true: "import.node_created", false: "import.node_updated"}[created],
		Before: rawSnapshot(beforeJSON), After: json.RawMessage(afterJSON),
	})
	return id, created, !created, err
}

func importKindConflict(conflicts []ImportConflict, key string) bool {
	for _, item := range conflicts {
		if item.Key == key && item.Reason == "kind_change_not_allowed" {
			return true
		}
	}
	return false
}

func relationTouchesKindConflict(conflicts []ImportConflict, rows map[int64]Record, ids ...int64) bool {
	for _, id := range ids {
		key := stringField(rows[id], "issue_key")
		if key != "" && importKindConflict(conflicts, key) {
			return true
		}
	}
	return false
}

func appendConflict(existing []ImportConflict, next ImportConflict) []ImportConflict {
	for _, item := range existing {
		if item.Key == next.Key && item.Reason == next.Reason {
			return existing
		}
	}
	return append(existing, next)
}

// The last importer event is the write baseline. Compare only fields an import
// can change; timestamps and sibling position are owned by other workflows.
func importNodeDiverged(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) (bool, error) {
	var current, imported []byte
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE tenant_id=$1 AND id=$2`, tenantID, nodeID).Scan(&current); err != nil {
		return false, err
	}
	err := tx.QueryRow(ctx, `SELECT after FROM events WHERE tenant_id=$1 AND node_id=$2 AND type IN ('import.node_created','import.node_updated','import.parent_changed') ORDER BY id DESC LIMIT 1`, tenantID, nodeID).Scan(&imported)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	parent, err := db.WorkStatusParentTx(ctx, tx, nodeID)
	if err != nil {
		return false, err
	}
	var have, baseline map[string]any
	if err := json.Unmarshal(current, &have); err != nil {
		return false, err
	}
	if err := json.Unmarshal(imported, &baseline); err != nil {
		return false, err
	}
	if parent {
		baseline["state"] = have["state"]
	}
	// The work-kind migration changes kind_id alone, retaining the original
	// importer snapshot and fields.classic.type. Normalize only the exact
	// recorded substitution; all person edits still compare to the old baseline.
	if !reflect.DeepEqual(have["kind_id"], baseline["kind_id"]) {
		var migrated bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events
		 WHERE tenant_id=$1 AND node_id=$2 AND type='node.work_kind_migrated'
		 AND after->>'migration'='AEON-649' AND before->>'kind_id'=$3
		 AND after->>'kind_id'=$4)`, tenantID, nodeID, baseline["kind_id"], have["kind_id"]).Scan(&migrated); err != nil {
			return false, err
		}
		if migrated {
			baseline["kind_id"] = have["kind_id"]
		}
	}
	for _, field := range []string{"title", "body", "fields", "kind_id", "parent_id", "deleted_at"} {
		if !reflect.DeepEqual(have[field], baseline[field]) {
			return true, nil
		}
	}
	return canonicalState(fmt.Sprint(have["state"])) != canonicalState(fmt.Sprint(baseline["state"])), nil
}

func rawSnapshot(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

func setParent(ctx context.Context, tx pgx.Tx, tenantID, actor, childID, parentID string) (bool, error) {
	if err := delivery.RefuseReleaseNodes(ctx, tx, []string{childID}); errors.Is(err, delivery.ErrReleaseAPI) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var before, after []byte
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2`, tenantID, childID).Scan(&before); err != nil {
		return false, err
	}
	changed, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$3 WHERE tenant_id=$1 AND id=$2 AND parent_id IS DISTINCT FROM $3`, tenantID, childID, parentID)
	if err != nil {
		return false, forbidPortal(err)
	}
	if changed.RowsAffected() == 0 {
		return false, nil
	}
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(nodes) FROM nodes WHERE tenant_id=$1 AND id=$2`, tenantID, childID).Scan(&after); err != nil {
		return false, err
	}
	_, err = events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{
		NodeID: &childID, Type: "import.parent_changed",
		Before: json.RawMessage(before), After: json.RawMessage(after),
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func importEvent(ctx context.Context, tx pgx.Tx, tenantID, actor, nodeID, typ, sourceID string, record Record, idField, suffix string) (string, error) {
	id, ok := intField(record, idField)
	ref := sourceID + ":" + typ + ":" + strconv.FormatInt(id, 10)
	if !ok {
		if suffix == "" {
			return "", fmt.Errorf("%s record missing id", typ)
		}
		ref = sourceID + ":" + typ + ":" + suffix
	}
	// Classic comments and attachment metadata can be edited. Record each
	// distinct revision once while leaving Aeon's event log append-only.
	if typ == "import.comment" || typ == "import.attachment" {
		canonical, err := jsonValue(record)
		if err != nil {
			return "", err
		}
		hash := sha256.Sum256(canonical)
		ref += fmt.Sprintf(":%x", hash[:16])
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE tenant_id=$1 AND type=$2 AND after ? 'classic_ref' AND after->>'classic_ref'=$3)`, tenantID, typ, ref).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return ref, nil
	}
	payload := Record{"classic_ref": ref, "record": record}
	// The mapped Aeon type is not a substitute for the classic type. Keep both
	// the verbatim record and an explicit copy so a later replay can see it
	// without interpreting the mapped link.
	if typ == "import.relation" {
		if classic := stringField(record, "type"); classic != "" {
			payload["classic_type"] = classic
		}
	}
	at := classicTime(stringField(record, "created_at"))
	if at == nil {
		at = classicTime(stringField(record, "changed_at"))
	}
	_, err := events.Append(ctx, tx, tenant.Principal{TenantID: tenantID, ID: actor}, events.Change{
		NodeID: &nodeID, Type: typ, After: payload, At: at,
	})
	return ref, err
}
func issueBody(r Record) string {
	if v := stringField(r, "description"); v != "" {
		return v
	}
	return stringField(r, "body")
}
func principalRefs(r Record, users map[int64]string, fields ...string) Record {
	refs := Record{}
	for _, field := range fields {
		if id, ok := intField(r, field); ok && users[id] != "" {
			refs[field] = users[id]
		}
	}
	return refs
}
func forbidPortal(err error) error {
	if events.PortalCatalogDenied(err) {
		return events.ErrForbidden
	}
	return err
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func parseClassicTime(s string) any {
	return classicTime(s)
}
func classicTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
