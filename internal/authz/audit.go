// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

const auditPage = 50

// accessEventNames is the single classification of current and historical
// authority events. Both the query allowlist and response mapping derive from it.
var accessEventNames = map[string]string{
	"role.created": "role.created", "role.updated": "role.updated", "role.deleted": "role.deleted",
	"binding.set": "binding.set", "binding.removed": "binding.removed",
	"invite.created": "invite.created", "invite.revoked": "invite.revoked", "invite.accepted": "invite.accepted",
	"principal.agent_created": "principal.agent_created", "principal.deactivated": "principal.deactivated", "principal.reactivated": "principal.reactivated",
	"principal.alias_linked": "principal.alias_linked", "principal.alias_unlinked": "principal.alias_unlinked",
	"agent_key.created": "agent_key.created", "agent_key.revoked": "agent_key.revoked", "agent_key.scopes_extended": "agent_key.scopes_extended", "agent_key.scopes_changed": "agent_key.scopes_changed",
	"authz.role_created": "role.created", "authz.role_updated": "role.updated", "authz.role_deleted": "role.deleted",
	"authz.workspace_role_changed": "binding.set", "authz.binding_reassigned": "binding.set", "authz.binding_migrated": "binding.set",
	"authz.agent_binding_created": "binding.set", "authz.agent_binding_migrated": "binding.set", "authz.owner_fallback": "binding.set",
	"authz.agent_permission_granted": "role.updated",
	"authz.key_created":              "agent_key.created", "authz.key_revoked": "agent_key.revoked",
	"principal.linked": "principal.alias_linked", "principal.unlinked": "principal.alias_unlinked",
}

var accessEventTypes = func() []string {
	types := make([]string, 0, len(accessEventNames))
	for name := range accessEventNames {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}()

type auditItem struct {
	ID      int64          `json:"id"`
	Type    string         `json:"type"`
	At      time.Time      `json:"at"`
	Actor   PrincipalRef   `json:"actor"`
	Subject *PrincipalRef  `json:"subject"`
	Project *auditProject  `json:"project"`
	Data    map[string]any `json:"data"`
}

type auditProject struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

func (m *Module) audit(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	if r.URL.Query().Get("category") != "access" {
		apiFail(w, 400, "invalid", "category", "Category must be access")
		return
	}
	var after *int64
	if raw := r.URL.Query().Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			apiFail(w, 400, "invalid", "after", "After must be an event id")
			return
		}
		after = &n
	}
	var items []auditItem
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT e.id, e.type, e.at, e.actor_principal_id::text, actor.name, e.before, e.after, e.node_id::text
			FROM events e
			JOIN principals actor ON actor.tenant_id=e.tenant_id AND actor.id=e.actor_principal_id
			WHERE e.tenant_id=$1::uuid AND e.type = ANY($2::text[])
			  AND ($3::bigint IS NULL OR e.id > $3::bigint)
			ORDER BY e.id
			LIMIT $4`, p.TenantID, accessEventTypes, after, auditPage+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		rawItems := []auditItem{}
		subjectIDs := []string{}
		projectIDs := []string{}
		for rows.Next() {
			var item auditItem
			var before, afterRaw []byte
			var nodeID *string
			if err := rows.Scan(&item.ID, &item.Type, &item.At, &item.Actor.PrincipalID, &item.Actor.Name, &before, &afterRaw, &nodeID); err != nil {
				return err
			}
			decodedBefore := decodeJSON(before)
			decodedAfter := decodeJSON(afterRaw)
			mapped, ok := mapAuditType(item.Type, decodedAfter)
			if !ok {
				continue
			}
			item.Type = mapped
			item.Data = map[string]any{"before": decodedBefore, "after": decodedAfter}
			if id := subjectID(mapped, decodedBefore, decodedAfter); id != "" {
				subjectIDs = append(subjectIDs, id)
				item.Subject = &PrincipalRef{PrincipalID: id}
			}
			if nodeID != nil && uuidPattern.MatchString(*nodeID) {
				projectIDs = append(projectIDs, *nodeID)
				item.Project = &auditProject{ID: *nodeID}
			} else if id := projectID(decodedBefore, decodedAfter); id != "" {
				projectIDs = append(projectIDs, id)
				item.Project = &auditProject{ID: id}
			}
			rawItems = append(rawItems, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		names, err := principalNames(r.Context(), tx, p.TenantID, subjectIDs)
		if err != nil {
			return err
		}
		keys, err := projectKeys(r.Context(), tx, p.TenantID, projectIDs)
		if err != nil {
			return err
		}
		for i := range rawItems {
			if rawItems[i].Subject != nil {
				rawItems[i].Subject.Name = names[rawItems[i].Subject.PrincipalID]
			}
			if rawItems[i].Project != nil {
				rawItems[i].Project.Key = keys[rawItems[i].Project.ID]
			}
		}
		items = rawItems
		return nil
	})
	if err != nil {
		internalFail(w, err)
		return
	}
	var next any
	if len(items) > auditPage {
		next = items[auditPage-1].ID
		items = items[:auditPage]
	}
	if items == nil {
		items = []auditItem{}
	}
	reply(w, http.StatusOK, map[string]any{"items": items, "next_after": next})
}

func decodeJSON(raw []byte) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func mapAuditType(stored string, after any) (string, bool) {
	if stored == "authz.workspace_role_changed" && jsonField(after, "role_id") == "" && hasJSONField(after, "role_id") {
		return "binding.removed", true
	}
	mapped, ok := accessEventNames[stored]
	return mapped, ok
}

func subjectID(typ string, before, after any) string {
	for _, v := range []any{after, before} {
		if id := jsonField(v, "principal_id"); uuidPattern.MatchString(id) {
			return id
		}
	}
	if strings.Contains(typ, "alias") {
		for _, v := range []any{after, before} {
			if id := jsonField(v, "linked_to"); uuidPattern.MatchString(id) {
				return id
			}
		}
	}
	return ""
}

func projectID(before, after any) string {
	for _, v := range []any{after, before} {
		if id := jsonField(v, "project_id"); uuidPattern.MatchString(id) {
			return id
		}
		scope := jsonField(v, "scope")
		if scope == "" {
			scope = jsonField(v, "scope_type")
		}
		if scope == "project" {
			if id := jsonField(v, "scope_id"); uuidPattern.MatchString(id) {
				return id
			}
		}
	}
	return ""
}

func jsonField(v any, key string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func hasJSONField(v any, key string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m[key]
	return ok
}

func principalNames(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) (map[string]string, error) {
	return lookupText(ctx, tx, `SELECT id::text, name FROM principals WHERE tenant_id=$1::uuid AND id::text = ANY($2::text[])`, tenantID, ids)
}

func projectKeys(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) (map[string]string, error) {
	return lookupText(ctx, tx, `SELECT id::text, key FROM nodes WHERE tenant_id=$1::uuid AND id::text = ANY($2::text[])`, tenantID, ids)
}

func lookupText(ctx context.Context, tx pgx.Tx, query, tenantID string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, query, tenantID, unique(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil {
			return nil, err
		}
		out[id] = value
	}
	return out, rows.Err()
}
