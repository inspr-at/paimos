// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

const auditPage = 50

// accessEventTypes are the v2 audit names plus the P1 names they replace.
var accessEventTypes = []string{
	"role.created", "role.updated", "role.deleted",
	"binding.set", "binding.removed",
	"invite.created", "invite.revoked", "invite.accepted",
	"principal.agent_created", "principal.deactivated", "principal.reactivated", "principal.alias_linked", "principal.alias_unlinked",
	"agent_key.created", "agent_key.revoked", "agent_key.scopes_extended", "agent_key.scopes_changed",
	"authz.role_created", "authz.role_updated", "authz.role_deleted",
	"authz.workspace_role_changed", "authz.binding_reassigned", "authz.binding_migrated",
	"authz.agent_binding_created", "authz.agent_binding_migrated", "authz.owner_fallback",
	"authz.key_created", "authz.key_revoked",
	"principal.linked", "principal.unlinked",
}

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
	order := r.URL.Query().Get("order")
	if order == "" {
		order = "asc"
	}
	if order != "asc" && order != "desc" {
		apiFail(w, 400, "invalid", "order", "Order must be asc or desc")
		return
	}
	cursorName, comparison, direction := "after", ">", "ASC"
	if order == "desc" {
		cursorName, comparison, direction = "before", "<", "DESC"
	}
	if (order == "asc" && r.URL.Query().Has("before")) || (order == "desc" && r.URL.Query().Has("after")) {
		apiFail(w, 400, "invalid", "order", "Use after with asc or before with desc")
		return
	}
	var cursor *int64
	if raw := r.URL.Query().Get(cursorName); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			apiFail(w, 400, "invalid", cursorName, "Cursor must be an event id")
			return
		}
		cursor = &n
	}
	var items []auditItem
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT e.id, e.type, e.at, e.actor_principal_id::text, actor.name, e.before, e.after, e.node_id::text
			FROM events e
			JOIN principals actor ON actor.tenant_id=e.tenant_id AND actor.id=e.actor_principal_id
			WHERE e.tenant_id=$1::uuid AND e.type = ANY($2::text[])
		  AND ($3::bigint IS NULL OR e.id `+comparison+` $3::bigint)
		ORDER BY e.id `+direction+`
		LIMIT $4`, p.TenantID, accessEventTypes, cursor, auditPage+1)
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
	body := map[string]any{"items": items, "next_after": next}
	if order == "desc" {
		body["next_after"] = nil
		body["next_before"] = next
	}
	reply(w, http.StatusOK, body)
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
	switch stored {
	case "role.created", "role.updated", "role.deleted",
		"binding.set", "binding.removed",
		"invite.created", "invite.revoked", "invite.accepted",
		"principal.agent_created", "principal.deactivated", "principal.reactivated", "principal.alias_linked", "principal.alias_unlinked",
		"agent_key.created", "agent_key.revoked", "agent_key.scopes_extended", "agent_key.scopes_changed":
		return stored, true
	case "authz.role_created":
		return "role.created", true
	case "authz.role_updated":
		return "role.updated", true
	case "authz.role_deleted":
		return "role.deleted", true
	case "authz.workspace_role_changed":
		if jsonField(after, "role_id") == "" && hasJSONField(after, "role_id") {
			return "binding.removed", true
		}
		return "binding.set", true
	case "authz.binding_reassigned", "authz.binding_migrated", "authz.agent_binding_created", "authz.agent_binding_migrated", "authz.owner_fallback":
		return "binding.set", true
	case "authz.key_created":
		return "agent_key.created", true
	case "authz.key_revoked":
		return "agent_key.revoked", true
	case "principal.linked":
		return "principal.alias_linked", true
	case "principal.unlinked":
		return "principal.alias_unlinked", true
	default:
		return "", false
	}
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
