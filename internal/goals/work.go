// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// People's goal writes (AEON-1618): link and unlink work on the goal's
// implicit outcome, and delete a goal with Undo. Agents keep PATCH.

const maxWorkBody = 16 << 10

type workInput struct {
	NodeIDs []string `json:"node_ids"`
}

// WorkResult names the goal and its people-linked work after the call.
type WorkResult struct {
	ID      string   `json:"id"`
	Key     string   `json:"key"`
	Changed bool     `json:"changed"`
	NodeIDs []string `json:"node_ids"`
	EventID *int64   `json:"event_id"`
	Watermark
}

type workSnapshot struct {
	GoalID  string   `json:"goal_id"`
	NodeIDs []string `json:"node_ids"`
}

func (m *module) linkWork(w http.ResponseWriter, r *http.Request)   { m.work(w, r, true) }
func (m *module) unlinkWork(w http.ResponseWriter, r *http.Request) { m.work(w, r, false) }

func (m *module) work(w http.ResponseWriter, r *http.Request, link bool) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	if !nodeIdentity(r.PathValue("goalId")) {
		respond(w, nil, invalid("goalId", "must be a goal key or UUID"))
		return
	}
	raw, err := httpapi.ReadRequestBody(w, r, maxWorkBody, 10*time.Second)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var in workInput
	if err = uniqueObjectKeys(raw); err == nil {
		err = decodeStrict(raw, &in)
	}
	if err != nil {
		respond(w, nil, invalid("body", err.Error()))
		return
	}
	ids, err := cleanWorkIDs(in.NodeIDs)
	if err != nil {
		respond(w, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	now := m.now()
	var out WorkResult
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var e error
		out, e = workTx(ctx, tx, p, r.PathValue("goalId"), ids, link, now)
		return e
	})
	respond(w, out, err)
}

func cleanWorkIDs(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > MaxLinked {
		return nil, invalid("node_ids", "name 1–20 work items")
	}
	out := []string{}
	for _, value := range values {
		if !nodeIdentity(value) {
			return nil, invalid("node_ids", "must be work item keys or UUIDs")
		}
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out, nil
}

// implicitOutcome finds the goal's people-links outcome, also when it serves
// work outside the caller's projects; callers then refuse writes they could
// not fully read. Its row is locked after the goal's.
func implicitOutcome(ctx context.Context, tx pgx.Tx, goal string) (*storedNode, error) {
	var found *storedNode
	err := withAllGoalProjects(ctx, tx, func() error {
		n, err := scanNode(tx.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.parent_id=$1 AND k.slug='outcome' AND n.deleted_at IS NULL AND n.fields @> '{"linked_work":true}'::jsonb ORDER BY n.id LIMIT 1 FOR NO KEY UPDATE OF n`, goal))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(target_node_id::text ORDER BY target_node_id),'{}'::text[]) FROM (SELECT target_node_id FROM node_relations WHERE source_node_id=$1 AND type='serves' LIMIT 21) r`, n.ID).Scan(&n.Targets); err != nil {
			return err
		}
		found = &n
		return nil
	})
	if err != nil || found == nil {
		return found, err
	}
	// Outcome visibility fails closed when any end is hidden from the caller.
	if _, err := resolveNode(ctx, tx, found.ID, false); errors.Is(err, pgx.ErrNoRows) {
		return nil, authz.ErrForbidden
	} else if err != nil {
		return nil, err
	}
	return found, nil
}

// linkTarget resolves one work item the caller may read. A hidden and a
// missing item are refused alike, so the answer reveals nothing.
func linkTarget(ctx context.Context, tx pgx.Tx, p tenant.Principal, goal storedNode, identity string) (storedNode, error) {
	n, err := resolveNode(ctx, tx, identity, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, fail(403, "node_ids: "+identity+" is not a work item you can read")
	}
	if err != nil {
		return n, err
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: n.Project}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return n, fail(403, "node_ids: "+identity+" is not a work item you can read")
		}
		return n, err
	}
	if !isWorkKind(n.Slug) {
		return n, invalid("node_ids", identity+" is not a ticket or epic")
	}
	if goal.Project != "" && n.Project != goal.Project {
		return n, invalid("node_ids", identity+" belongs to another project than the goal")
	}
	return n, nil
}

func workTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, identity string, ids []string, link bool, now time.Time) (WorkResult, error) {
	var out WorkResult
	if err := lockWrite(ctx, tx, p); err != nil {
		return out, err
	}
	goal, err := resolveNode(ctx, tx, identity, true)
	if err != nil {
		return out, err
	}
	if err := authorizeGoal(ctx, tx, p, goal); err != nil {
		return out, err
	}
	if goal.Slug != GoalKind {
		return out, invalid("goalId", "link work to a release through its release")
	}
	implicit, err := implicitOutcome(ctx, tx, goal.ID)
	if err != nil {
		return out, err
	}
	before := []string{}
	if implicit != nil {
		before = slices.Clone(implicit.Targets)
	}
	named := []string{}
	for _, identity := range ids {
		if link {
			n, err := linkTarget(ctx, tx, p, goal, identity)
			if err != nil {
				return out, err
			}
			named = append(named, n.ID)
			continue
		}
		// Unlinking names only what is linked; anything else is already done.
		n, err := resolveNode(ctx, tx, identity, false)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		named = append(named, n.ID)
	}
	after := slices.Clone(before)
	for _, id := range named {
		if link && !slices.Contains(after, id) {
			after = append(after, id)
		}
		if !link {
			after = slices.DeleteFunc(after, func(v string) bool { return v == id })
		}
	}
	sort.Strings(after)
	out = WorkResult{ID: goal.ID, Key: goal.Key, NodeIDs: after}
	if slices.Equal(before, after) {
		out.NodeIDs = before
		out.Watermark, err = watermark(ctx, tx)
		return out, err
	}
	if len(after) > MaxLinked {
		return out, invalid("node_ids", "a goal holds at most 20 linked items; link an epic to bring in its tickets")
	}
	if link {
		count, err := allChildCount(ctx, tx, goal.ID)
		if err != nil {
			return out, err
		}
		if implicit != nil {
			count--
		}
		if count+len(after) > MaxOutcomes {
			return out, invalid("node_ids", "a goal holds at most 200 items")
		}
	}
	pending := []events.Change{}
	var outcomeKind kindSnapshot
	if implicit == nil {
		kinds, changes, err := ensureKinds(ctx, tx, p.TenantID)
		if err != nil {
			return out, err
		}
		pending = append(pending, kindEvents(changes)...)
		outcomeKind = kinds[OutcomeKind]
	}
	capture, err := CaptureGoalETAs(ctx, tx, []string{goal.ID})
	if err != nil {
		return out, err
	}
	var old *storedNode
	if implicit == nil {
		created, err := insertNode(ctx, tx, p, outcomeKind, "Linked work", goal.ID, map[string]any{"outcome_ref": LinkedRef, linkedField: true}, linkedPosition)
		if err != nil {
			return out, err
		}
		implicit = &created
	} else {
		snapshot := cloneNode(*implicit)
		old = &snapshot
	}
	for _, id := range after {
		if !slices.Contains(before, id) {
			if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, p.TenantID, implicit.ID, id); err != nil {
				return out, err
			}
		}
	}
	removed := slices.DeleteFunc(slices.Clone(before), func(v string) bool { return slices.Contains(after, v) })
	if len(removed) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM node_relations WHERE source_node_id=$1 AND type='serves' AND target_node_id=ANY($2::uuid[])`, implicit.ID, removed); err != nil {
			return out, err
		}
	}
	current := cloneNode(*implicit)
	current.Targets = after
	// The outcome snapshots keep goal history and series exact.
	addChanges(&pending, old, current)
	membership(&pending, goal.ID, old, current)
	etaChanges, err := capture.Changes(ctx, tx)
	if err != nil {
		return out, err
	}
	pending = append(pending, etaChanges...)
	typ := "goal.work_unlinked"
	if link {
		typ = "goal.work_linked"
	}
	work := events.Change{NodeID: &goal.ID, Type: typ, Before: workSnapshot{GoalID: goal.ID, NodeIDs: before}, After: workSnapshot{GoalID: goal.ID, NodeIDs: after}}
	pending = append(pending, work)
	for i, change := range pending {
		e, err := events.Append(ctx, tx, p, change)
		if err != nil {
			return out, err
		}
		if i == len(pending)-1 {
			out.EventID = &e.ID
		}
	}
	out.Changed = true
	out.Watermark, err = watermark(ctx, tx)
	return out, err
}

// ---------- Delete with Undo ----------

type deletedGoal struct {
	ID         string   `json:"id"`
	Key        string   `json:"key"`
	Title      string   `json:"title"`
	ProjectID  string   `json:"project_id,omitempty"`
	OutcomeIDs []string `json:"outcome_ids"`
}

func (m *module) deleteGoal(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	if !nodeIdentity(r.PathValue("goalId")) {
		respond(w, nil, invalid("goalId", "must be a goal key or UUID"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out WorkResult
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var e error
		out, e = deleteTx(ctx, tx, p, r.PathValue("goalId"))
		return e
	})
	respond(w, out, err)
}

// deleteTx soft-deletes a goal with its outcomes. Linked work stays as it
// is; its serves relations remain for Undo and count nowhere meanwhile.
func deleteTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, identity string) (WorkResult, error) {
	var out WorkResult
	if err := lockWrite(ctx, tx, p); err != nil {
		return out, err
	}
	goal, err := resolveNode(ctx, tx, identity, true)
	if err != nil {
		return out, err
	}
	if err := authorizeGoal(ctx, tx, p, goal); err != nil {
		return out, err
	}
	if goal.Slug != GoalKind {
		return out, invalid("goalId", "delete a release in its release settings")
	}
	// Deleting every outcome is a whole-list edit: it needs complete visibility.
	all, err := allChildCount(ctx, tx, goal.ID)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.parent_id=$1 AND k.slug='outcome' AND n.deleted_at IS NULL ORDER BY n.id LIMIT 201 FOR NO KEY UPDATE OF n`, goal.ID)
	if err != nil {
		return out, err
	}
	outcomes := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		outcomes = append(outcomes, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(outcomes) != all {
		return out, authz.ErrForbidden
	}
	ids := append([]string{goal.ID}, outcomes...)
	if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=ANY($1::uuid[]) AND deleted_at IS NULL`, ids); err != nil {
		return out, err
	}
	snapshot := deletedGoal{ID: goal.ID, Key: goal.Key, Title: goal.Title, ProjectID: goal.Project, OutcomeIDs: outcomes}
	e, err := events.Append(ctx, tx, p, events.Change{NodeID: &goal.ID, Type: "goal.deleted", Before: snapshot})
	if err != nil {
		return out, err
	}
	out = WorkResult{ID: goal.ID, Key: goal.Key, Changed: true, NodeIDs: []string{}, EventID: &e.ID}
	out.Watermark, err = watermark(ctx, tx)
	return out, err
}

// UndoHandlers restores a deleted goal with the outcomes deleted with it, as
// long as it is still deleted and its project is still there.
func UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{"goal.deleted": undoDelete}
}

func undoDelete(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	var before deletedGoal
	if e.NodeID == nil || json.Unmarshal(e.Before, &before) != nil || before.ID != *e.NodeID || len(before.OutcomeIDs) > MaxOutcomes {
		return events.Change{}, events.ErrConflict
	}
	if err := lockWrite(ctx, tx, p); err != nil {
		return events.Change{}, err
	}
	var project string
	var deleted bool
	err := tx.QueryRow(ctx, `SELECT coalesce(n.project_id::text,''),n.deleted_at IS NOT NULL FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='goal' FOR NO KEY UPDATE OF n`, before.ID).Scan(&project, &deleted)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!deleted || project != before.ProjectID) {
		return events.Change{}, events.ErrConflict
	}
	if err != nil {
		return events.Change{}, err
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: project}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return events.Change{}, events.ErrForbidden
		}
		return events.Change{}, err
	}
	if project != "" {
		if _, err := resolveNode(ctx, tx, project, false); err != nil {
			return events.Change{}, events.ErrConflict
		}
	}
	ids := append([]string{before.ID}, before.OutcomeIDs...)
	sort.Strings(ids)
	tag, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL,updated_at=clock_timestamp() WHERE id=ANY($1::uuid[]) AND deleted_at IS NOT NULL`, ids)
	if err != nil {
		return events.Change{}, events.ErrConflict
	}
	if tag.RowsAffected() != int64(len(ids)) {
		return events.Change{}, events.ErrConflict
	}
	return events.Change{NodeID: &before.ID, Type: "goal.restored", After: before}, nil
}
