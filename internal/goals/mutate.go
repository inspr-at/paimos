// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func updateTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, identity, key string, in Update, now time.Time) (WriteResult, error) {
	var out WriteResult
	if err := lockWrite(ctx, tx, p); err != nil {
		return out, err
	}
	target, err := resolveNode(ctx, tx, identity, false)
	if err != nil {
		return out, err
	}
	goals := map[string]storedNode{target.ID: target}
	// Resolve immutable identities first; the tree fence prevents a concurrent
	// move/delete between resolution and the sorted row locks.
	if in.Items.Value != nil {
		for _, move := range in.Items.Value.Move {
			n, e := resolveNode(ctx, tx, move.ToGoal, false)
			if e != nil {
				return out, e
			}
			goals[n.ID] = n
		}
	}
	ids := make([]string, 0, len(goals))
	for id := range goals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n, e := resolveNode(ctx, tx, id, true)
		if e != nil {
			return out, e
		}
		if e = authorizeGoal(ctx, tx, p, n); e != nil {
			return out, e
		}
		goals[id] = n
	}
	target = goals[target.ID]
	hash, err := in.Digest()
	if err != nil {
		return out, err
	}
	r, err := loadReceipt(ctx, tx, p, key)
	if err != nil {
		return out, err
	}
	if r != nil {
		return replay(r, http.MethodPatch, target.ID, hash)
	}
	if in.Items.Value != nil && in.Items.Value.Mode == "replace" {
		var latest int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM events WHERE node_id=$1 AND type LIKE 'goal.%'`, target.ID).Scan(&latest); err != nil {
			return out, err
		}
		if in.IfReadThrough.Value == nil || *in.IfReadThrough.Value != latest {
			return out, fail(409, "if_read_through: goal changed")
		}
	}
	capture, err := CaptureGoalETAs(ctx, tx, ids)
	if err != nil {
		return out, err
	}
	pending := []events.Change{}
	before := cloneNode(target)
	setETA(&target, in.ETA, p, now)
	setField(target.Fields, "steps", in.Steps)
	if in.ProgressOverride.Set {
		if in.ProgressOverride.Value == nil {
			for _, k := range []string{"progress_override_pct", "progress_override_note", "progress_override_by", "progress_override_at"} {
				delete(target.Fields, k)
			}
		} else {
			value := in.ProgressOverride.Value
			if !reflect.DeepEqual(target.Fields["progress_override_pct"], float64(*value.Pct)) || fieldString(target, "progress_override_note") != value.Note {
				target.Fields["progress_override_pct"] = float64(*value.Pct)
				target.Fields["progress_override_note"] = value.Note
				target.Fields["progress_override_by"] = p.ID
				target.Fields["progress_override_at"] = now.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	goals[target.ID] = target
	if in.Items.Value != nil {
		if err := mutateItems(ctx, tx, p, target.ID, goals, *in.Items.Value, now, &pending); err != nil {
			return out, err
		}
	}
	for _, id := range ids {
		n := goals[id]
		// Source retired refs may have changed while processing moves.
		original := before
		if id != target.ID {
			original, err = resolveNode(ctx, tx, id, false)
			if err != nil {
				return out, err
			}
		}
		if !reflect.DeepEqual(original.Fields, n.Fields) {
			if err := saveNode(ctx, tx, n); err != nil {
				return out, err
			}
		}
	}
	target = goals[target.ID]
	if !reflect.DeepEqual(ownGoalFields(before), ownGoalFields(target)) {
		pending = append(pending, events.Change{NodeID: &target.ID, Type: "goal.updated", Before: ownGoalFields(before), After: ownGoalFields(target)})
	}
	if !reflect.DeepEqual(etaSnapshot(before), etaSnapshot(target)) {
		pending = append(pending, events.Change{NodeID: &target.ID, Type: "goal.eta_changed", Before: etaSnapshot(before), After: etaSnapshot(target)})
	}
	etaChanges, err := capture.Changes(ctx, tx)
	if err != nil {
		return out, err
	}
	pending = append(pending, etaChanges...)
	return finishWrite(ctx, tx, p, key, http.MethodPatch, target, hash, pending)
}
func ownGoalFields(n storedNode) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"steps", "progress_override_pct", "progress_override_note", "progress_override_by", "progress_override_at"} {
		if v, ok := n.Fields[k]; ok {
			out[k] = v
		}
	}
	return out
}
func childNodes(ctx context.Context, tx pgx.Tx, goal string) ([]storedNode, error) {
	rows, err := tx.Query(ctx, `SELECT `+nodeColumns+` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.parent_id=$1 AND k.slug='outcome' AND n.deleted_at IS NULL ORDER BY n.id LIMIT 201`, goal)
	if err != nil {
		return nil, err
	}
	children := []storedNode{}
	for rows.Next() {
		n, e := scanNode(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		children = append(children, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(children) > MaxOutcomes {
		return nil, invalid("items", "at most 200 live outcomes")
	}
	for i := range children {
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(target_node_id::text ORDER BY target_node_id),'{}'::text[]) FROM (SELECT target_node_id FROM node_relations WHERE source_node_id=$1 AND type='serves' LIMIT 21) r`, children[i].ID).Scan(&children[i].Targets); err != nil {
			return nil, err
		}
		if len(children[i].Targets) > MaxTargets {
			return nil, invalid("items.keys", "at most 20 serves targets")
		}
	}
	return children, nil
}

// A replace, move or handed-over write must not silently operate on only the
// subset of children that relation visibility exposed. Probe counts only, then
// restore visibility before reading data. No hidden counts leave this function.
func requireAllChildren(ctx context.Context, tx pgx.Tx, goal string, visible int) error {
	var previous string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&previous); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return err
	}
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT n.id FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.parent_id=$1 AND k.slug='outcome' AND n.deleted_at IS NULL LIMIT 201) s`, goal).Scan(&count)
	if _, e := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, previous); e != nil {
		return e
	}
	if err != nil {
		return err
	}
	if count != visible {
		return authz.ErrForbidden
	}
	return nil
}
func retired(n storedNode) []string {
	raw, _ := json.Marshal(n.Fields["retired_refs"])
	var refs []string
	json.Unmarshal(raw, &refs)
	return refs
}
func retire(goals map[string]storedNode, goal, ref string) error {
	n := goals[goal]
	refs := retired(n)
	if !slices.Contains(refs, ref) {
		refs = append(refs, ref)
	}
	if len(refs) > MaxRetiredRefs {
		return invalid("items", "retired ref limit reached; create another goal")
	}
	n.Fields["retired_refs"] = refs
	goals[goal] = n
	return nil
}
func mutateItems(ctx context.Context, tx pgx.Tx, p tenant.Principal, target string, goals map[string]storedNode, in ItemsUpdate, now time.Time, pending *[]events.Change) error {
	children := map[string]map[string]storedNode{}
	ids := []string{}
	for id := range goals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rows, err := childNodes(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = requireAllChildren(ctx, tx, id, len(rows)); err != nil {
			return err
		}
		children[id] = map[string]storedNode{}
		for _, n := range rows {
			children[id][fieldString(n, "outcome_ref")] = n
		}
	}
	// Lock the entire touched outcome batch by immutable id, across goals.
	outcomeIDs := []string{}
	for _, rows := range children {
		for _, n := range rows {
			outcomeIDs = append(outcomeIDs, n.ID)
		}
	}
	rows, err := tx.Query(ctx, `SELECT id FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR NO KEY UPDATE`, outcomeIDs)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	source := children[target]
	remove := map[string]bool{}
	for _, ref := range in.Remove {
		remove[ref] = true
	}
	listed := map[string]bool{}
	for _, up := range in.Upsert {
		listed[up.Ref] = true
	}
	if in.Mode == "replace" {
		for ref := range source {
			if !listed[ref] {
				remove[ref] = true
			}
		}
	}
	for ref := range remove {
		n, ok := source[ref]
		if !ok {
			return pgx.ErrNoRows
		}
		old := cloneNode(n)
		n.Deleted = true
		if err := retire(goals, target, ref); err != nil {
			return err
		}
		if err := saveNode(ctx, tx, n); err != nil {
			return err
		}
		addChanges(pending, &old, n)
		membership(pending, target, &old, n)
		delete(source, ref)
	}
	kind, err := readKind(ctx, tx, p.TenantID, OutcomeKind)
	if err != nil {
		return err
	}
	for _, up := range in.Upsert {
		n, exists := source[up.Ref]
		var old *storedNode
		if exists {
			copy := cloneNode(n)
			old = &copy
		} else {
			if slices.Contains(retired(goals[target]), up.Ref) {
				return fail(409, "items.upsert.ref: retired ref")
			}
			// Deleted rows keep refs even if a legacy writer never retired them.
			var used bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE parent_id=$1 AND fields->>'outcome_ref'=$2)`, target, up.Ref).Scan(&used); err != nil {
				return err
			}
			if used {
				return fail(409, "items.upsert.ref: ref is unavailable")
			}
			if up.Label.Value == nil {
				return invalid("items.upsert.label", "required for a new outcome")
			}
			n = storedNode{Parent: target, Project: goals[target].Project, Slug: OutcomeKind, Title: *up.Label.Value, Position: "0", Fields: map[string]any{"outcome_ref": up.Ref}, Targets: []string{}}
		}
		if up.Label.Value != nil {
			n.Title = *up.Label.Value
		}
		setField(n.Fields, "note", up.Note)
		setField(n.Fields, "state_set", up.State)
		setETA(&n, up.ETA, p, now)
		if up.Position.Set {
			n.Position = "0"
			if up.Position.Value != nil {
				raw, _ := up.Position.Value.MarshalJSON()
				n.Position = string(raw)
			}
		}
		if up.Keys.Set {
			n.Targets = []string{}
			if up.Keys.Value != nil {
				for _, identity := range *up.Keys.Value {
					t, err := resolveNode(ctx, tx, identity, false)
					if err != nil {
						return err
					}
					if IsGoalKind(t.Slug) || t.Slug == OutcomeKind {
						return invalid("items.upsert.keys", "goals and outcomes cannot be serves targets")
					}
					n.Targets = append(n.Targets, t.ID)
				}
			}
			sort.Strings(n.Targets)
			if len(slices.Compact(n.Targets)) != len(n.Targets) {
				return invalid("items.upsert.keys", "duplicate resolved target")
			}
		}
		if exists && reflect.DeepEqual(*old, n) {
			continue
		}
		if !exists {
			if len(source) >= MaxOutcomes {
				return invalid("items", "at most 200 live outcomes")
			}
			created, e := insertNode(ctx, tx, p, kind, n.Title, target, n.Fields, n.Position)
			if e != nil {
				return e
			}
			created.Targets = n.Targets
			n = created
		} else if err := saveNode(ctx, tx, n); err != nil {
			return err
		}
		if up.Keys.Set {
			if err := replaceTargets(ctx, tx, p, n); err != nil {
				return err
			}
		}
		source[up.Ref] = n
		addChanges(pending, old, n)
		membership(pending, target, old, n)
	}
	for _, move := range in.Move {
		var n storedNode
		ref := ""
		for r, v := range source {
			if v.ID == move.OutcomeID {
				n = v
				ref = r
				break
			}
		}
		if ref == "" {
			return pgx.ErrNoRows
		}
		if listed[ref] || remove[ref] {
			return invalid("items.move", "cannot move an upserted or removed outcome")
		}
		dest, err := resolveNode(ctx, tx, move.ToGoal, false)
		if err != nil {
			return err
		}
		if dest.ID == target {
			return invalid("items.move.to_goal", "must be a different goal")
		}
		newRef := move.ToRef
		if newRef == "" {
			newRef = ref
		}
		if _, ok := children[dest.ID][newRef]; ok {
			return fail(409, "items.move.to_ref: ref is unavailable")
		}
		if slices.Contains(retired(goals[dest.ID]), newRef) {
			return fail(409, "items.move.to_ref: retired ref")
		}
		if len(children[dest.ID]) >= MaxOutcomes {
			return invalid("items.move.to_goal", "at most 200 live outcomes")
		}
		old := cloneNode(n)
		n.Parent = dest.ID
		n.Project = dest.Project
		n.Fields["outcome_ref"] = newRef
		if err := retire(goals, target, ref); err != nil {
			return err
		}
		if err := saveNode(ctx, tx, n); err != nil {
			return err
		}
		addChanges(pending, &old, n)
		membership(pending, target, &old, n)
		membership(pending, dest.ID, &old, n)
		delete(source, ref)
		children[dest.ID][newRef] = n
	}
	if in.HandedOver.Set {
		desired := ""
		detail := ""
		if in.HandedOver.Value != nil {
			desired = in.HandedOver.Value.Ref
			detail = in.HandedOver.Value.Detail
			if _, ok := source[desired]; !ok {
				return pgx.ErrNoRows
			}
		}
		// Clear first, preserving the one-live-handover unique index throughout.
		for ref, n := range source {
			if fieldString(n, "handed_over_at") == "" {
				continue
			}
			if ref == desired && fieldString(n, "handed_over_detail") == detail {
				continue
			}
			old := cloneNode(n)
			for _, k := range []string{"handed_over_at", "handed_over_by", "handed_over_detail"} {
				delete(n.Fields, k)
			}
			if err := saveNode(ctx, tx, n); err != nil {
				return err
			}
			source[ref] = n
			addChanges(pending, &old, n)
			membership(pending, target, &old, n)
		}
		if desired != "" {
			n := source[desired]
			if fieldString(n, "handed_over_at") == "" {
				old := cloneNode(n)
				n.Fields["handed_over_at"] = now.UTC().Format(time.RFC3339Nano)
				n.Fields["handed_over_by"] = p.ID
				n.Fields["handed_over_detail"] = detail
				if err := saveNode(ctx, tx, n); err != nil {
					return err
				}
				source[desired] = n
				addChanges(pending, &old, n)
				membership(pending, target, &old, n)
			}
		}
	}
	return nil
}
func replaceTargets(ctx context.Context, tx pgx.Tx, p tenant.Principal, n storedNode) error {
	if _, err := tx.Exec(ctx, `DELETE FROM node_relations WHERE source_node_id=$1 AND type='serves'`, n.ID); err != nil {
		return err
	}
	for _, id := range n.Targets {
		if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, p.TenantID, n.ID, id); err != nil {
			return err
		}
	}
	return nil
}
func membership(pending *[]events.Change, goal string, before *storedNode, after storedNode) {
	var b any
	if before != nil {
		b = *before
	}
	*pending = append(*pending, events.Change{NodeID: &goal, Type: "goal.items_changed", Before: b, After: after})
}
