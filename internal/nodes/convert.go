// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

const codeKindConversionBlocked = "kind_conversion_blocked"

// kindOffender is one direct child that cannot stay under the target kind.
type kindOffender struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Kind string `json:"kind"`
}

type convertInput struct {
	ToKind string `json:"to_kind"`
}

// seededIssueNames are the issue kinds a tenant starts with. A kind schema
// can mark issue_family instead; a custom slug that keeps one of these icons
// is in the family too. Conversion is allowed between any two family members.
func seededIssueName(name string) bool {
	switch name {
	case "epic", "ticket", "task":
		return true
	default:
		return false
	}
}

func explicitIssueFamily(raw json.RawMessage) (marked bool, ok bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false, false
	}
	value, exists := obj["issue_family"]
	if !exists {
		return false, false
	}
	flag, isBool := value.(bool)
	if !isBool {
		return false, false
	}
	return flag, true
}

func issueFamilyKind(kind kindJSON) bool {
	if marked, ok := explicitIssueFamily(kind.FieldSchema); ok {
		return marked
	}
	return seededIssueName(kind.Slug) || seededIssueName(kind.Icon)
}

func (m *Module) handleConvertNode(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, http.StatusForbidden, "permission denied")
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var in convertInput
	if err := decodeJSON(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	expected, err := parseUnmodifiedSince(r.Header)
	if err != nil {
		writeErr(w, err)
		return
	}
	node, err := m.convertNode(r.Context(), p, id, in.ToKind, expected)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (m *Module) convertNode(ctx context.Context, p tenant.Principal, id, toKind string, expected *time.Time) (nodeJSON, error) {
	if p.Kind != tenant.Person {
		return nodeJSON{}, &httpError{status: http.StatusForbidden, msg: "permission denied"}
	}
	toKind = strings.TrimSpace(toKind)
	if toKind == "" || !validSlug(toKind) {
		return nodeJSON{}, badRequest("invalid type")
	}
	var node nodeJSON
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := armPortalModeration(ctx, tx, p); err != nil {
			return err
		}
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		if err := refuseReleaseNode(ctx, tx, id); err != nil {
			return err
		}
		current, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if expected != nil && !current.UpdatedAt.Equal(*expected) {
			return &httpError{status: http.StatusPreconditionFailed, msg: "node has changed"}
		}
		currentKind, _, err := loadKind(ctx, tx, current.KindID)
		if err != nil {
			return err
		}
		target, err := loadKindBySlug(ctx, tx, toKind)
		if err != nil {
			return err
		}
		if target.ID == currentKind.ID {
			node = current
			views, err := loadEstimates(ctx, tx, []string{id})
			if err != nil {
				return err
			}
			node.Estimate = views[id]
			return nil
		}
		if !issueFamilyKind(currentKind) || !issueFamilyKind(target) {
			return conflictCoded("kind is immutable", codeKindChangeNotAllowed)
		}
		if err := requireCreateTarget(ctx, tx, p, target.Slug, current.ParentID); err != nil {
			return err
		}
		targetSchema, err := compileSchema(target.FieldSchema)
		if err != nil {
			return err
		}
		parentBlocked := false
		if current.ParentID != nil {
			err := ensureParentAllows(ctx, tx, *current.ParentID, target.Slug)
			if err != nil {
				var he *httpError
				if errors.As(err, &he) && he.status == http.StatusConflict {
					parentBlocked = true
				} else {
					return err
				}
			}
		}
		children, err := directChildren(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		blockedChildren := offenders(children, target.AllowedChildKinds)
		// A task may store roadmap_public. That is not a person approval, so a
		// kind change drops it before the target schema can keep the keys.
		prepared, cleared, err := withoutInheritedRoadmapPublication(current.Fields)
		if err != nil {
			return err
		}
		storedFields, dropped, blockedFields, err := fitKindFields(targetSchema, prepared)
		if err != nil {
			return err
		}
		if parentBlocked || len(blockedChildren) > 0 || len(blockedFields) > 0 {
			return conversionBlocked(target.Slug, parentBlocked, blockedChildren, blockedFields)
		}
		var row pgx.Row
		if len(dropped) > 0 || cleared {
			row = tx.QueryRow(ctx, `
				UPDATE nodes
				SET kind_id = $1::uuid,
				    fields = $2::jsonb,
				    updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond')
				WHERE id = $3::uuid AND deleted_at IS NULL
				RETURNING `+nodeReturning, target.ID, string(storedFields), id)
		} else {
			row = tx.QueryRow(ctx, `
				UPDATE nodes
				SET kind_id = $1::uuid,
				    updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond')
				WHERE id = $2::uuid AND deleted_at IS NULL
				RETURNING `+nodeReturning, target.ID, id)
		}
		loaded, scanErr := scanNode(row)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return notFound("node not found")
		}
		if scanErr != nil {
			return dbErr("convert node", scanErr)
		}
		meta, err := json.Marshal(map[string]string{"from": currentKind.Slug, "to": target.Slug, "by": p.ID})
		if err != nil {
			return err
		}
		before, err := snapshot(current)
		if err != nil {
			return err
		}
		after, err := snapshot(loaded)
		if err != nil {
			return err
		}
		if err := m.writeEvent(ctx, tx, Event{
			ActorPrincipalID: p.ID,
			NodeID:           &loaded.ID,
			Type:             evNodeKindChanged,
			Before:           before,
			After:            after,
			Metadata:         meta,
		}); err != nil {
			return err
		}
		node = loaded
		views, err := loadEstimates(ctx, tx, []string{id})
		if err != nil {
			return err
		}
		node.Estimate = views[id]
		return nil
	})
	return node, err
}

func conversionBlocked(target string, parent bool, children []kindOffender, fields []string) *httpError {
	var parts []string
	if parent {
		parts = append(parts, "parent does not allow "+target)
	}
	if len(children) > 0 {
		keys := make([]string, len(children))
		for i, child := range children {
			keys[i] = child.Key
		}
		parts = append(parts, "children: "+strings.Join(keys, ", "))
	}
	if len(fields) > 0 {
		parts = append(parts, "fields: "+strings.Join(fields, ", "))
	}
	if len(children) == 0 {
		children = nil
	}
	if len(fields) == 0 {
		fields = nil
	}
	return &httpError{
		status:   http.StatusConflict,
		msg:      strings.Join(parts, "; "),
		code:     codeKindConversionBlocked,
		children: children,
		fields:   fields,
	}
}

func directChildren(ctx context.Context, tx pgx.Tx, parentID string) ([]kindOffender, error) {
	rows, err := tx.Query(ctx, `
		SELECT n.id::text, n.key, k.slug
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.parent_id = $1::uuid AND n.deleted_at IS NULL
		ORDER BY n.key
		FOR UPDATE OF n`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kindOffender
	for rows.Next() {
		var child kindOffender
		if err := rows.Scan(&child.ID, &child.Key, &child.Kind); err != nil {
			return nil, err
		}
		out = append(out, child)
	}
	return out, rows.Err()
}

func offenders(children []kindOffender, allowed []string) []kindOffender {
	var out []kindOffender
	for _, child := range children {
		if !childAllowed(allowed, child.Kind) {
			out = append(out, child)
		}
	}
	return out
}

// fitKindFields keeps the fields the target schema allows. Fields it rejects as
// undeclared are removed from the live node; the caller stores them in the
// event's before snapshot. The kept object is accepted only by the same
// schema.validate PATCH uses, including root constraints.
func fitKindFields(schema *jsSchema, raw json.RawMessage) (stored json.RawMessage, dropped, violations []string, err error) {
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	v, decErr := decodeValue(raw)
	if decErr != nil {
		return nil, nil, []string{"fields"}, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, nil, []string{"fields"}, nil
	}
	fitted := obj
	if schema != nil && schema.additionalSet && !schema.additionalAllow && schema.additionalSchema == nil {
		fitted = map[string]any{}
		for name, child := range obj {
			if _, declared := schema.props[name]; declared {
				fitted[name] = child
			} else {
				dropped = append(dropped, name)
			}
		}
		sort.Strings(dropped)
	}
	if schema != nil {
		if valErr := schema.validate(fitted); valErr != nil {
			return nil, dropped, violationNames(schema, fitted, valErr), nil
		}
	}
	if len(dropped) == 0 {
		return raw, nil, nil, nil
	}
	stored, err = json.Marshal(fitted)
	if err != nil {
		return nil, dropped, nil, err
	}
	return stored, dropped, nil, nil
}

func violationNames(schema *jsSchema, obj map[string]any, valErr error) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, name := range schema.required {
		if _, ok := obj[name]; !ok {
			add(name)
		}
	}
	for name, sub := range schema.props {
		child, ok := obj[name]
		if !ok || sub == nil {
			continue
		}
		if sub.validateAt("fields."+name, child) != nil {
			add(name)
		}
	}
	if len(names) == 0 {
		msg := valErr.Error()
		if rest, ok := strings.CutPrefix(msg, "fields."); ok {
			name := rest
			if i := strings.IndexAny(rest, " .["); i > 0 {
				name = rest[:i]
			}
			if name != "" && !strings.Contains(name, " ") {
				add(name)
				sort.Strings(names)
				return names
			}
		}
		add(msg)
	}
	sort.Strings(names)
	return names
}

func undoKindChange(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if p.Kind == tenant.Agent {
		return events.Change{}, events.ErrForbidden
	}
	var before, after nodeJSON
	if e.NodeID == nil || json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil ||
		before.ID != *e.NodeID || after.ID != *e.NodeID || before.ID != after.ID {
		return events.Change{}, events.ErrConflict
	}
	if err := lockTree(ctx, tx); err != nil {
		return events.Change{}, err
	}
	if err := armPortalModeration(ctx, tx, p); err != nil {
		return events.Change{}, err
	}
	if err := refuseReleaseNode(ctx, tx, *e.NodeID); err != nil {
		return events.Change{}, events.ErrConflict
	}
	current, err := loadNode(ctx, tx, after.ID, true)
	if err != nil {
		return events.Change{}, events.ErrConflict
	}
	if current.KindID != after.KindID || current.Key != after.Key || current.Title != after.Title ||
		current.Body != after.Body || current.State != after.State || !sameString(current.ParentID, after.ParentID) ||
		!sameJSON(current.Fields, after.Fields) || !current.UpdatedAt.Equal(after.UpdatedAt) {
		return events.Change{}, events.ErrConflict
	}
	oldKind, _, err := loadKind(ctx, tx, before.KindID)
	if err != nil {
		return events.Change{}, events.ErrConflict
	}
	if err := requireCreateTarget(ctx, tx, p, oldKind.Slug, current.ParentID); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusForbidden {
			return events.Change{}, events.ErrForbidden
		}
		return events.Change{}, events.ErrConflict
	}
	if current.ParentID != nil {
		if err := ensureParentAllows(ctx, tx, *current.ParentID, oldKind.Slug); err != nil {
			return events.Change{}, events.ErrConflict
		}
	}
	children, err := directChildren(ctx, tx, current.ID)
	if err != nil {
		return events.Change{}, err
	}
	if len(offenders(children, oldKind.AllowedChildKinds)) > 0 {
		return events.Change{}, events.ErrConflict
	}
	fields := before.Fields
	if len(strings.TrimSpace(string(fields))) == 0 {
		fields = json.RawMessage(`{}`)
	}
	restored, scanErr := scanNode(tx.QueryRow(ctx, `
		UPDATE nodes
		SET kind_id = $1::uuid,
		    fields = $2::jsonb,
		    updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond')
		WHERE id = $3::uuid AND deleted_at IS NULL
		RETURNING `+nodeReturning, before.KindID, string(fields), current.ID))
	if scanErr != nil {
		return events.Change{}, undoWriteErr(scanErr)
	}
	newKind, _, err := loadKind(ctx, tx, after.KindID)
	if err != nil {
		return events.Change{}, events.ErrConflict
	}
	meta, err := json.Marshal(map[string]string{"from": newKind.Slug, "to": oldKind.Slug, "by": p.ID})
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: e.NodeID, Type: evNodeKindChanged, Before: after, After: restored, Metadata: meta}, nil
}
