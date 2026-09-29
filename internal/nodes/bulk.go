// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
)

// POST /api/nodes/bulk applies one change to many nodes in one tenant
// transaction: a state (archive is the state "archived"), a priority, an
// assignee, tags to add and remove, and a new parent inside the same project.
// Each changed node gets its own node.updated and node.moved events, so every
// ticket's history stays whole, and the batch gets one node.bulk_changed event
// (node_id null) whose id the response returns: POST /api/events/{id}/undo
// restores every node of the batch, or none when any of them changed since.
// A node that cannot take the change is skipped with a reason; nothing else
// about the batch fails for it. if_unmodified_since names the revision (the
// updated_at) the caller last saw per node: a node changed since is skipped
// with code conflict and left as it is (AEON-326).
const (
	evNodeBulkChanged = "node.bulk_changed"
	maxBulkNodes      = 500
	maxBulkTags       = 20
)

type bulkTag struct {
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// A tag is a name or {"name", "color"}.
func (t *bulkTag) UnmarshalJSON(raw []byte) error {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		t.Name = name
		return nil
	}
	type plain bulkTag
	var p plain
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	*t = bulkTag(p)
	return nil
}

type bulkRequest struct {
	IDs        []string        `json:"ids"`
	State      *string         `json:"state"`
	Priority   json.RawMessage `json:"priority"`
	Assignee   json.RawMessage `json:"assignee"`
	TagsAdd    []bulkTag       `json:"tags_add"`
	TagsRemove []string        `json:"tags_remove"`
	ParentID   *string         `json:"parent_id"`
	// Per node: the updated_at the caller last saw.
	IfUnmodifiedSince map[string]string `json:"if_unmodified_since"`
}

// codeConflict marks a node skipped because it changed since the caller read it.
const codeConflict = "conflict"

// bulkSnap is the part of a node a batch changes and its undo restores.
type bulkSnap struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	State     string          `json:"state"`
	Fields    json.RawMessage `json:"fields"`
	ParentID  *string         `json:"parent_id"`
	Position  string          `json:"position"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type bulkBatch struct {
	Items []bulkSnap `json:"items"`
}

type bulkSkip struct {
	ID     string `json:"id"`
	Key    string `json:"key,omitempty"`
	Reason string `json:"reason"`
	Code   string `json:"code,omitempty"`
}

type bulkResult struct {
	EventID   *int64     `json:"event_id"`
	Items     []nodeJSON `json:"items"`
	Unchanged []string   `json:"unchanged"`
	Skipped   []bulkSkip `json:"skipped"`
}

func snapOf(n nodeJSON) bulkSnap {
	return bulkSnap{ID: n.ID, Key: n.Key, State: n.State, Fields: n.Fields, ParentID: n.ParentID, Position: n.Position, UpdatedAt: n.UpdatedAt}
}

func (m *Module) handleBulk(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var in bulkRequest
	if err := decodeJSON(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	plan, err := parseBulk(in)
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := m.applyBulk(r.Context(), p, plan)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type bulkPlan struct {
	ids                    []string
	state                  *string
	setPriority, setAssign bool
	priority, assignee     *string
	tagsAdd                []bulkTag
	tagsRemove             map[string]bool
	parentID               *string
	expected               map[string]time.Time
}

func (b bulkPlan) changesFields() bool {
	return b.setPriority || b.setAssign || len(b.tagsAdd) > 0 || len(b.tagsRemove) > 0
}

func parseBulk(in bulkRequest) (bulkPlan, error) {
	var plan bulkPlan
	if len(in.IDs) == 0 || len(in.IDs) > maxBulkNodes {
		return plan, badRequest("ids must list 1 to 500 nodes")
	}
	seen := map[string]bool{}
	for _, raw := range in.IDs {
		id, ok := parseUUID(raw)
		if !ok {
			return plan, badRequest("invalid ids")
		}
		if !seen[id] {
			seen[id] = true
			plan.ids = append(plan.ids, id)
		}
	}
	sort.Strings(plan.ids) // lock in one order
	if in.State != nil {
		s := strings.TrimSpace(*in.State)
		if s == "" || len(s) > 64 {
			return plan, badRequest("invalid state")
		}
		plan.state = &s
	}
	nullableString := func(raw json.RawMessage, name string, max int) (bool, *string, error) {
		if len(raw) == 0 {
			return false, nil, nil
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true, nil, nil
		}
		var s string
		if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" || len(s) > max {
			return false, nil, badRequest("invalid " + name)
		}
		s = strings.TrimSpace(s)
		return true, &s, nil
	}
	var err error
	if plan.setPriority, plan.priority, err = nullableString(in.Priority, "priority", 32); err != nil {
		return plan, err
	}
	if plan.setAssign, plan.assignee, err = nullableString(in.Assignee, "assignee", 64); err != nil {
		return plan, err
	}
	if plan.assignee != nil {
		id, ok := parseUUID(*plan.assignee)
		if !ok {
			return plan, badRequest("invalid assignee")
		}
		plan.assignee = &id
	}
	if len(in.TagsAdd) > maxBulkTags || len(in.TagsRemove) > maxBulkTags {
		return plan, badRequest("too many tags")
	}
	for _, tag := range in.TagsAdd {
		tag.Name = strings.TrimSpace(tag.Name)
		if tag.Name == "" || len(tag.Name) > 100 || len(tag.Color) > 32 {
			return plan, badRequest("invalid tags_add")
		}
		plan.tagsAdd = append(plan.tagsAdd, tag)
	}
	for _, name := range in.TagsRemove {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || len(name) > 100 {
			return plan, badRequest("invalid tags_remove")
		}
		if plan.tagsRemove == nil {
			plan.tagsRemove = map[string]bool{}
		}
		plan.tagsRemove[name] = true
	}
	if in.ParentID != nil {
		id, ok := parseUUID(*in.ParentID)
		if !ok {
			return plan, badRequest("invalid parent_id")
		}
		plan.parentID = &id
	}
	if len(in.IfUnmodifiedSince) > maxBulkNodes {
		return plan, badRequest("invalid if_unmodified_since")
	}
	for raw, at := range in.IfUnmodifiedSince {
		id, ok := parseUUID(raw)
		if !ok {
			return plan, badRequest("invalid if_unmodified_since")
		}
		when, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return plan, badRequest("invalid if_unmodified_since")
		}
		if plan.expected == nil {
			plan.expected = map[string]time.Time{}
		}
		plan.expected[id] = when
	}
	if plan.state == nil && !plan.changesFields() && plan.parentID == nil {
		return plan, badRequest("nothing to change")
	}
	return plan, nil
}

// nextFields applies the plan's field changes to a node's fields.
func nextFields(current json.RawMessage, plan bulkPlan) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(current) > 0 {
		if err := json.Unmarshal(current, &fields); err != nil || fields == nil {
			fields = map[string]json.RawMessage{}
		}
	}
	if plan.setPriority {
		// An explicit null clears the priority, also over an imported value.
		fields["priority"], _ = json.Marshal(plan.priority)
	}
	if plan.setAssign {
		fields["assignee"], _ = json.Marshal(plan.assignee)
	}
	if len(plan.tagsAdd) > 0 || len(plan.tagsRemove) > 0 {
		var tags []json.RawMessage
		_ = json.Unmarshal(fields["tags"], &tags)
		name := func(raw json.RawMessage) string {
			var tag bulkTag
			if json.Unmarshal(raw, &tag) != nil {
				return ""
			}
			return strings.ToLower(strings.TrimSpace(tag.Name))
		}
		kept := make([]json.RawMessage, 0, len(tags)+len(plan.tagsAdd))
		has := map[string]bool{}
		for _, tag := range tags {
			n := name(tag)
			if plan.tagsRemove[n] {
				continue
			}
			kept = append(kept, tag)
			has[n] = true
		}
		for _, tag := range plan.tagsAdd {
			n := strings.ToLower(tag.Name)
			if has[n] || plan.tagsRemove[n] {
				continue
			}
			raw, _ := json.Marshal(tag)
			kept = append(kept, raw)
			has[n] = true
		}
		fields["tags"], _ = json.Marshal(kept)
	}
	return json.Marshal(fields)
}

type bulkTarget struct {
	node      nodeJSON
	kindSlug  string
	projectID *string
}

func (m *Module) applyBulk(ctx context.Context, p tenant.Principal, plan bulkPlan) (bulkResult, error) {
	result := bulkResult{Items: []nodeJSON{}, Unchanged: []string{}, Skipped: []bulkSkip{}}
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := armPortalModeration(ctx, tx, p); err != nil {
			return err
		}
		if plan.parentID != nil {
			if err := lockTree(ctx, tx); err != nil {
				return err
			}
		}
		if plan.setAssign && plan.assignee != nil {
			canonical, _, err := principallink.Resolve(ctx, tx, p.TenantID, *plan.assignee)
			if errors.Is(err, pgx.ErrNoRows) {
				return badRequest("assignee not found in tenant")
			}
			if err != nil {
				return err
			}
			plan.assignee = &canonical
		}
		targets, err := loadBulkTargets(ctx, tx, plan.ids)
		if err != nil {
			return err
		}
		var parent *bulkTarget
		var parentAncestors map[string]bool
		var parentAllowed []string
		if plan.parentID != nil {
			loaded, err := loadBulkTargets(ctx, tx, []string{*plan.parentID})
			if err != nil {
				return err
			}
			if len(loaded) == 0 {
				return notFound("parent not found")
			}
			parent = &loaded[0]
			if parentAncestors, err = ancestorIDs(ctx, tx, parent.node.ID); err != nil {
				return err
			}
			kind, _, err := loadKind(ctx, tx, parent.node.KindID)
			if err != nil {
				return err
			}
			parentAllowed = kind.AllowedChildKinds
		}
		found := map[string]bool{}
		var before, after []bulkSnap
		for _, target := range targets {
			found[target.node.ID] = true
			current := target.node
			skip := func(reason string) {
				result.Skipped = append(result.Skipped, bulkSkip{ID: current.ID, Key: current.Key, Reason: reason})
			}
			// Compared under the row lock, exactly like PATCH's If-Unmodified-Since.
			if expected, ok := plan.expected[current.ID]; ok && !current.UpdatedAt.Equal(expected) {
				result.Skipped = append(result.Skipped, bulkSkip{ID: current.ID, Key: current.Key, Reason: "changed since you loaded it", Code: codeConflict})
				continue
			}
			move := plan.parentID != nil && !sameString(current.ParentID, plan.parentID)
			if move {
				switch {
				case parentAncestors[current.ID]:
					skip("cannot move under itself")
					continue
				case !sameString(target.projectID, parent.projectID):
					skip("belongs to another project")
					continue
				case !childAllowed(parentAllowed, target.kindSlug):
					skip(article(target.kindSlug) + " cannot sit under " + article(parent.kindSlug))
					continue
				}
			}
			fields := current.Fields
			if plan.changesFields() {
				next, err := nextFields(current.Fields, plan)
				if err != nil {
					return err
				}
				_, schema, err := loadKind(ctx, tx, current.KindID)
				if err != nil {
					return err
				}
				if next, err = validateFields(schema, next); err == nil {
					next, err = canonicalAssignments(ctx, tx, p.TenantID, next)
				}
				if err != nil {
					skip("its fields do not allow this change")
					continue
				}
				fields = next
			}
			state := current.State
			if plan.state != nil {
				state = *plan.state
			}
			if issues := ticketbenefits.Transition(target.kindSlug, current.State, state, fields); len(issues) > 0 {
				result.Skipped = append(result.Skipped, bulkSkip{
					ID: current.ID, Key: current.Key,
					Reason: "before done: " + strings.Join(issues, "; "),
					Code:   ticketbenefits.RequiredCode,
				})
				continue
			}
			edit := state != current.State || !sameJSON(fields, current.Fields)
			if !edit && !move {
				result.Unchanged = append(result.Unchanged, current.ID)
				continue
			}
			latest := current
			if edit {
				latest, err = scanNode(tx.QueryRow(ctx, `UPDATE nodes SET state=$2, fields=$3::jsonb,
				 updated_at=greatest(clock_timestamp(), updated_at + interval '1 microsecond')
				 WHERE id=$1::uuid AND deleted_at IS NULL RETURNING `+nodeReturning, current.ID, state, string(fields)))
				if err != nil {
					return dbErr("bulk update", err)
				}
				if err := m.record(ctx, tx, p.ID, &latest.ID, evNodeUpdated, current, latest); err != nil {
					return err
				}
			}
			if move {
				moved, err := placeUnder(ctx, tx, latest, *plan.parentID)
				if err != nil {
					return err
				}
				if err := m.record(ctx, tx, p.ID, &moved.ID, evNodeMoved, latest, moved); err != nil {
					return err
				}
				latest = moved
			}
			before = append(before, snapOf(current))
			after = append(after, snapOf(latest))
			result.Items = append(result.Items, latest)
		}
		for _, id := range plan.ids {
			if !found[id] {
				result.Skipped = append(result.Skipped, bulkSkip{ID: id, Reason: "not found"})
			}
		}
		if len(after) == 0 {
			return nil
		}
		actor, _, err := principallink.Resolve(ctx, tx, p.TenantID, p.ID)
		if err != nil {
			return err
		}
		e, err := events.Append(ctx, tx, tenant.Principal{TenantID: p.TenantID, ID: actor}, events.Change{
			Type: evNodeBulkChanged, Before: bulkBatch{before}, After: bulkBatch{after},
		})
		if err != nil {
			return err
		}
		result.EventID = &e.ID
		return nil
	})
	return result, err
}

func loadBulkTargets(ctx context.Context, tx pgx.Tx, ids []string) ([]bulkTarget, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+nodeCols+`, k.slug, (
			WITH RECURSIVE up AS (
				SELECT a.id, a.parent_id, a.kind_id, 0 AS depth FROM nodes a WHERE a.id = n.id
				UNION ALL SELECT b.id, b.parent_id, b.kind_id, up.depth + 1 FROM nodes b JOIN up ON b.id = up.parent_id
				WHERE up.depth < 64
			) SELECT up.id::text FROM up JOIN node_kinds uk ON uk.id = up.kind_id WHERE uk.slug = 'project' ORDER BY up.depth LIMIT 1
		)
		FROM nodes n JOIN node_kinds k ON k.id = n.kind_id AND k.tenant_id = n.tenant_id
		WHERE n.id = ANY($1::uuid[]) AND n.deleted_at IS NULL
		ORDER BY n.id
		FOR UPDATE OF n`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bulkTarget
	for rows.Next() {
		var t bulkTarget
		var fields, position string
		n := &t.node
		if err := rows.Scan(&n.ID, &n.Key, &n.KindID, &n.Title, &n.Body, &fields, &n.State, &n.ParentID, &position,
			&n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &t.kindSlug, &t.projectID); err != nil {
			return nil, err
		}
		if fields == "" {
			fields = "{}"
		}
		n.Fields = json.RawMessage(fields)
		n.Position = trimDecimal(position)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ancestorIDs is id and every node above it.
func ancestorIDs(ctx context.Context, tx pgx.Tx, id string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE up AS (
			SELECT id, parent_id, 0 AS depth FROM nodes WHERE id = $1::uuid
			UNION ALL SELECT n.id, n.parent_id, up.depth + 1 FROM nodes n JOIN up ON n.id = up.parent_id WHERE up.depth < 64
		) SELECT id::text FROM up`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var ancestor string
		if err := rows.Scan(&ancestor); err != nil {
			return nil, err
		}
		out[ancestor] = true
	}
	return out, rows.Err()
}

// placeUnder appends node as the last child of parentID. The caller holds the
// tree lock and has checked kinds, cycles and project.
func placeUnder(ctx context.Context, tx pgx.Tx, node nodeJSON, parentID string) (nodeJSON, error) {
	siblings, err := loadSiblings(ctx, tx, &parentID)
	if err != nil {
		return nodeJSON{}, err
	}
	position, updates, err := Place(siblings, node.ID, nil)
	if err != nil {
		return nodeJSON{}, err
	}
	if err := applyPositions(ctx, tx, updates); err != nil {
		return nodeJSON{}, err
	}
	moved, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET parent_id=$1::uuid, position=$2::numeric,
	 updated_at=greatest(clock_timestamp(), date_trunc('second', updated_at) + interval '1 second')
	 WHERE id=$3::uuid AND deleted_at IS NULL RETURNING `+nodeReturning, parentID, position, node.ID))
	if err != nil {
		return nodeJSON{}, dbErr("bulk move", err)
	}
	return moved, nil
}

// article puts "a" or "an" before a kind slug ("an epic", "a task").
func article(slug string) string {
	word := strings.ReplaceAll(slug, "_", " ")
	if word != "" && strings.ContainsRune("aeiou", rune(word[0])) {
		return "an " + word
	}
	return "a " + word
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

// undoBulk restores every node of a batch to its state before the batch, or
// none when any of them changed since. Like the batch itself it records one
// node.updated or node.moved per restored node, so ticket histories show the
// undo; the events module appends the compensating batch event.
func undoBulk(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	var before, after bulkBatch
	if json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil ||
		len(before.Items) == 0 || len(before.Items) != len(after.Items) {
		return events.Change{}, events.ErrConflict
	}
	if err := lockTree(ctx, tx); err != nil {
		return events.Change{}, err
	}
	if err := armPortalModeration(ctx, tx, p); err != nil {
		return events.Change{}, err
	}
	ids := make([]string, len(after.Items))
	for i, item := range after.Items {
		if before.Items[i].ID != item.ID {
			return events.Change{}, events.ErrConflict
		}
		ids[i] = item.ID
	}
	loaded, err := loadBulkTargets(ctx, tx, ids)
	if err != nil {
		return events.Change{}, err
	}
	current := map[string]nodeJSON{}
	for _, t := range loaded {
		current[t.node.ID] = t.node
	}
	// Decide the whole batch before writing anything: an edit needs
	// nodes.write in the node's project, a move nodes.move in every project it
	// changes (ADR-003 P2).
	projects := map[string]string{}
	kinds := map[string]string{}
	for _, t := range loaded {
		projects[t.node.ID] = deref(t.projectID)
		kinds[t.node.ID] = t.kindSlug
	}
	for i, want := range after.Items {
		now, ok := current[want.ID]
		if !ok || !now.UpdatedAt.Equal(want.UpdatedAt) {
			return events.Change{}, events.ErrConflict
		}
		old := before.Items[i]
		if len(ticketbenefits.Transition(kinds[now.ID], now.State, old.State, old.Fields)) > 0 {
			return events.Change{}, events.ErrConflict
		}
		if old.State != now.State || !sameJSON(old.Fields, now.Fields) {
			if err := authz.RequireInProjects(ctx, tx, p, "nodes.write", projects[now.ID]); err != nil {
				return events.Change{}, events.ErrForbidden
			}
		}
		if !sameString(old.ParentID, now.ParentID) {
			if err := requireMove(ctx, tx, p, now.ID, old.ParentID); err != nil {
				if errors.Is(err, errMoveForbidden) {
					return events.Change{}, events.ErrForbidden
				}
				if errors.Is(err, pgx.ErrNoRows) {
					return events.Change{}, events.ErrConflict
				}
				return events.Change{}, err
			}
		}
	}
	var was, restored []bulkSnap
	for i, want := range after.Items {
		now := current[want.ID]
		old := before.Items[i]
		latest := now
		if old.State != now.State || !sameJSON(old.Fields, now.Fields) {
			latest, err = scanNode(tx.QueryRow(ctx, `UPDATE nodes SET state=$2, fields=$3::jsonb,
			 updated_at=greatest(clock_timestamp(), updated_at + interval '1 microsecond')
			 WHERE id=$1::uuid RETURNING `+nodeReturning, now.ID, old.State, string(old.Fields)))
			if err != nil {
				return events.Change{}, undoWriteErr(err)
			}
			if _, err := events.Append(ctx, tx, p, events.Change{NodeID: &latest.ID, Type: evNodeUpdated, Before: now, After: latest}); err != nil {
				return events.Change{}, err
			}
		}
		if !sameString(old.ParentID, now.ParentID) {
			if old.ParentID != nil {
				if err := parentExists(ctx, tx, *old.ParentID); err != nil {
					return events.Change{}, events.ErrConflict
				}
			}
			moved, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET parent_id=$1::uuid, position=$2::numeric,
			 updated_at=greatest(clock_timestamp(), date_trunc('second', updated_at) + interval '1 second')
			 WHERE id=$3::uuid RETURNING `+nodeReturning, old.ParentID, old.Position, now.ID))
			if err != nil {
				return events.Change{}, undoWriteErr(err)
			}
			if _, err := events.Append(ctx, tx, p, events.Change{NodeID: &moved.ID, Type: evNodeMoved, Before: latest, After: moved}); err != nil {
				return events.Change{}, err
			}
			latest = moved
		}
		was = append(was, snapOf(now))
		restored = append(restored, snapOf(latest))
	}
	return events.Change{Type: evNodeBulkChanged, Before: bulkBatch{was}, After: bulkBatch{restored}}, nil
}
