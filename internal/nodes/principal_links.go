// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/principallink"
)

// Service principals are reserved agents (the tenant System actor, operators,
// importers, and the other job actors). They write events; they do not own work.
const servicePrincipalRoles = `ARRAY['system','importer','operator','embedding','quote_public_service','quote_confirmation_service','portal_public_service']::text[]`

// Canonicalize explicit native assignments before persisting and recording the
// node snapshot. Classic source metadata stays verbatim for provenance.
// previous is the node's fields before this write; nil on create. An unchanged
// service assignee is left in place so a later edit of another field still
// saves. Setting a service principal, including System, is rejected.
func canonicalAssignments(ctx context.Context, tx pgx.Tx, tenantID string, raw, previous json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for _, key := range []string{"assignee", "assignee_id"} {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			continue
		}
		var id string
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &id) != nil {
			if json.Unmarshal(value, &object) != nil || json.Unmarshal(object["id"], &id) != nil {
				return nil, badRequest("invalid assignee")
			}
		}
		if _, ok := parseUUID(id); !ok {
			return nil, badRequest("invalid assignee")
		}
		canonical, name, service, err := lookupAssignee(ctx, tx, tenantID, id)
		if err != nil {
			return nil, err
		}
		if service {
			same, err := sameAssignee(ctx, tx, tenantID, previous, key, canonical)
			if err != nil {
				return nil, err
			}
			if !same {
				return nil, badRequest("service principals cannot be assignees")
			}
		}
		if object != nil {
			object["id"], _ = json.Marshal(canonical)
			if _, ok := object["name"]; ok {
				object["name"], _ = json.Marshal(name)
			}
			fields[key], _ = json.Marshal(object)
		} else {
			fields[key], _ = json.Marshal(canonical)
		}
	}
	return json.Marshal(fields)
}

// resolveAssignee is the explicit assignment path. A service principal,
// including the tenant System actor, is a 400.
func resolveAssignee(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, string, error) {
	canonical, name, service, err := lookupAssignee(ctx, tx, tenantID, id)
	if err != nil {
		return "", "", err
	}
	if service {
		return "", "", badRequest("service principals cannot be assignees")
	}
	return canonical, name, nil
}

func lookupAssignee(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, string, bool, error) {
	canonical, name, err := principallink.Resolve(ctx, tx, tenantID, id)
	if err == pgx.ErrNoRows {
		return "", "", false, badRequest("assignee not found in tenant")
	}
	if err != nil {
		return "", "", false, err
	}
	var service bool
	err = tx.QueryRow(ctx, `SELECT kind='agent' AND roles && `+servicePrincipalRoles+`
		FROM principals WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, canonical).Scan(&service)
	if err == pgx.ErrNoRows {
		return "", "", false, badRequest("assignee not found in tenant")
	}
	if err != nil {
		return "", "", false, err
	}
	return canonical, name, service, nil
}

// sameAssignee reports whether previous fields already store this canonical
// assignee under key. A missing or unresolvable previous value is not the same.
func sameAssignee(ctx context.Context, tx pgx.Tx, tenantID string, previous json.RawMessage, key, canonical string) (bool, error) {
	if len(previous) == 0 || string(previous) == "null" {
		return false, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(previous, &fields) != nil {
		return false, nil
	}
	value, ok := fields[key]
	if !ok || string(value) == "null" {
		return false, nil
	}
	var id string
	var object map[string]json.RawMessage
	if json.Unmarshal(value, &id) != nil {
		if json.Unmarshal(value, &object) != nil || json.Unmarshal(object["id"], &id) != nil {
			return false, nil
		}
	}
	if _, ok := parseUUID(id); !ok {
		return false, nil
	}
	prior, _, err := principallink.Resolve(ctx, tx, tenantID, id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return prior == canonical, nil
}
