// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Portal catalog rows are a workspace decision. One check covers every write
// that records an event: create, patch, move, bulk, delete, undo, apply,
// import, and relations. A person with settings.manage passes. Everyone else
// is refused, including an agent whose scopes name that permission.
//
// Indirect writes never appear on that event: sibling renumbering and the
// project cascade update portal rows the caller did not name. The database
// trigger nodes_portal_row_guard refuses those unless this transaction armed
// aeon.portal_moderation after the same person check. Position is included.
// Public catalog order is that column.
//
// Public intake and voting append their own event types and do not go through
// the node API. Comments and attachments do not change the catalog row.

func init() {
	events.SetMutationGuard(guardAppendedMutation)
}

var errPortalDenied = errors.New("portal moderation required")

func portalKind(slug string) bool {
	switch slug {
	case "portal_product", "portal_feature", "portal_wish":
		return true
	default:
		return false
	}
}

func portalModerator(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	return authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{})
}

// armPortalModeration lets later statements in this transaction update or
// delete portal catalog rows. The flag is set only after a person passes
// settings.manage. Everyone else, including an agent whose scopes name that
// permission, leaves it unset so the database trigger refuses the write.
func armPortalModeration(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	actor, err := principalForPortal(ctx, tx, p)
	if err != nil {
		if errors.Is(err, errPortalDenied) || errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if err := portalModerator(ctx, tx, actor); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return nil
		}
		return err
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.portal_moderation', 'on', true)`)
	return err
}

func portalPublicEvent(eventType string) bool {
	switch eventType {
	case "portal.wish_submitted", "portal.vote_cast":
		return true
	default:
		return strings.HasPrefix(eventType, "comment.") || strings.HasPrefix(eventType, "attachment.")
	}
}

func relationEvent(eventType string) bool {
	switch eventType {
	case "relation.created", "relation.deleted", "relation.undone", "import.relation":
		return true
	default:
		return false
	}
}

func guardAppendedMutation(ctx context.Context, tx pgx.Tx, p tenant.Principal, c events.Change) error {
	before, err := snapshot(c.Before)
	if err != nil {
		return err
	}
	after, err := snapshot(c.After)
	if err != nil {
		return err
	}
	err = authorizePortalChange(ctx, tx, p, c.NodeID, c.Type, before, after)
	if errors.Is(err, errPortalDenied) {
		return events.ErrForbidden
	}
	return err
}

func authorizePortalChange(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID *string, eventType string, before, after json.RawMessage) error {
	needs, err := portalChangeNeedsModerator(ctx, tx, nodeID, eventType, before, after)
	if err != nil || !needs {
		return err
	}
	actor, err := principalForPortal(ctx, tx, p)
	if err != nil {
		if errors.Is(err, errPortalDenied) || errors.Is(err, pgx.ErrNoRows) {
			return errPortalDenied
		}
		return err
	}
	if err := portalModerator(ctx, tx, actor); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return errPortalDenied
		}
		return err
	}
	return nil
}

// principalForPortal is the requester when the event row only stored an id.
// An empty kind must not be treated as a person: agents stay agents.
func principalForPortal(ctx context.Context, tx pgx.Tx, p tenant.Principal) (tenant.Principal, error) {
	if p.Kind != tenant.Person && p.Kind != tenant.Agent {
		if from, ok := tenant.PrincipalFrom(ctx); ok && (from.Kind == tenant.Person || from.Kind == tenant.Agent) {
			if from.TenantID == "" {
				from.TenantID = p.TenantID
			}
			p = from
		}
	}
	if p.Kind == tenant.Person || p.Kind == tenant.Agent {
		if p.TenantID == "" {
			if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&p.TenantID); err != nil {
				return tenant.Principal{}, err
			}
		}
		return p, nil
	}
	if _, ok := parseUUID(p.ID); !ok {
		return tenant.Principal{}, errPortalDenied
	}
	var kind string
	err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id'), kind FROM principals WHERE id = $1::uuid`, p.ID).Scan(&p.TenantID, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return tenant.Principal{}, errPortalDenied
	}
	if err != nil {
		return tenant.Principal{}, err
	}
	p.Kind = tenant.PrincipalKind(kind)
	return p, nil
}

func portalChangeNeedsModerator(ctx context.Context, tx pgx.Tx, nodeID *string, eventType string, before, after json.RawMessage) (bool, error) {
	if portalPublicEvent(eventType) {
		return false, nil
	}
	beforeParents := map[string]*string{}
	afterParents := map[string]*string{}
	collectNodeParents(before, beforeParents)
	collectNodeParents(after, afterParents)

	ids := map[string]struct{}{}
	add := func(id string) {
		if norm, ok := parseUUID(id); ok {
			ids[norm] = struct{}{}
		}
	}
	if nodeID != nil {
		add(*nodeID)
	}
	for id := range beforeParents {
		add(id)
	}
	for id := range afterParents {
		add(id)
	}
	var source, target string
	if relationEvent(eventType) {
		source, target = relationEndpoint(before, after)
		add(source)
		add(target)
	}
	moved := changedParentIDs(beforeParents, afterParents)
	for _, id := range moved {
		add(id)
	}
	if len(ids) == 0 {
		return false, nil
	}
	slugs, err := portalKindSlugs(ctx, tx, mapKeys(ids))
	if err != nil {
		return false, err
	}
	isPortal := func(id string) bool {
		norm, ok := parseUUID(id)
		return ok && portalKind(slugs[norm])
	}
	if nodeID != nil && isPortal(*nodeID) {
		return true, nil
	}
	for id := range beforeParents {
		if isPortal(id) {
			return true, nil
		}
	}
	for id := range afterParents {
		if isPortal(id) {
			return true, nil
		}
	}
	if isPortal(source) || isPortal(target) {
		return true, nil
	}
	for _, id := range moved {
		if isPortal(id) {
			return true, nil
		}
	}
	return false, nil
}

func changedParentIDs(before, after map[string]*string) []string {
	keys := map[string]struct{}{}
	for id := range before {
		keys[id] = struct{}{}
	}
	for id := range after {
		keys[id] = struct{}{}
	}
	var out []string
	seen := map[string]struct{}{}
	add := func(id *string) {
		if id == nil {
			return
		}
		if _, ok := seen[*id]; ok {
			return
		}
		seen[*id] = struct{}{}
		out = append(out, *id)
	}
	for id := range keys {
		old, hadOld := before[id]
		next, hadNew := after[id]
		if hadOld && hadNew && sameString(old, next) {
			continue
		}
		add(old)
		add(next)
	}
	return out
}

func collectNodeParents(raw json.RawMessage, into map[string]*string) {
	obj := jsonObject(raw)
	if obj == nil {
		return
	}
	addNodeParent(obj, into)
	if nested := jsonObject(obj["node"]); nested != nil {
		addNodeParent(nested, into)
	}
	var items []json.RawMessage
	if json.Unmarshal(obj["items"], &items) != nil {
		return
	}
	for _, item := range items {
		if nested := jsonObject(item); nested != nil {
			addNodeParent(nested, into)
		}
	}
}

func addNodeParent(obj map[string]json.RawMessage, into map[string]*string) {
	id, ok := jsonUUID(obj["id"])
	if !ok {
		return
	}
	parent, present := jsonParent(obj["parent_id"])
	if !present {
		return
	}
	into[id] = parent
}

func relationEndpoint(before, after json.RawMessage) (string, string) {
	for _, raw := range []json.RawMessage{after, before} {
		obj := jsonObject(raw)
		if obj == nil {
			continue
		}
		source, sourceOK := jsonUUID(obj["source_node_id"])
		target, targetOK := jsonUUID(obj["target_node_id"])
		if sourceOK || targetOK {
			return source, target
		}
	}
	return "", ""
}

func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	return obj
}

func jsonUUID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return parseUUID(s)
}

func jsonParent(raw json.RawMessage) (*string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	id, ok := jsonUUID(raw)
	if !ok {
		return nil, false
	}
	return &id, true
}

func portalKindSlugs(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT n.id::text, k.slug
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		if norm, ok := parseUUID(id); ok {
			out[norm] = slug
		}
	}
	return out, rows.Err()
}

func mapKeys(ids map[string]struct{}) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out
}
