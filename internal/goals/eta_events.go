// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"reflect"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type EffectiveETA struct {
	Source  string     `json:"source"`
	Text    string     `json:"text,omitempty"`
	At      *time.Time `json:"at,omitempty"`
	By      string     `json:"by,omitempty"`
	Partial bool       `json:"partial"`
}

func authoredETA(n storedNode) EffectiveETA {
	value := EffectiveETA{Source: "none"}
	if fieldString(n, "eta_text") == "" && fieldString(n, "eta_at") == "" {
		return value
	}
	value.Source = "agent"
	value.Text = fieldString(n, "eta_text")
	value.By = fieldString(n, "eta_set_by")
	if at, err := time.Parse(time.RFC3339Nano, fieldString(n, "eta_at")); err == nil {
		value.At = &at
	}
	return value
}
func effectiveETAs(ctx context.Context, tx pgx.Tx, nodes []storedNode) (map[string]EffectiveETA, error) {
	roots := []string{}
	byOutcome := map[string][]string{}
	ids := []string{}
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	rows, err := tx.Query(ctx, `SELECT r.source_node_id::text,r.target_node_id::text FROM node_relations r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.target_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE r.source_node_id=ANY($1::uuid[]) AND r.type='serves' AND n.deleted_at IS NULL AND k.slug IN ('work','epic','ticket','task') ORDER BY r.source_node_id,r.target_node_id LIMIT 4097`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var source, target string
		if err := rows.Scan(&source, &target); err != nil {
			rows.Close()
			return nil, err
		}
		byOutcome[source] = append(byOutcome[source], target)
		roots = append(roots, target)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(roots) > 4096 {
		return nil, fail(413, "items.keys: ETA root budget exceeded")
	}
	aggregates, err := eta.LoadAggregates(ctx, tx, roots)
	if err != nil {
		return nil, err
	}
	out := map[string]EffectiveETA{}
	for _, n := range nodes {
		value := authoredETA(n)
		if value.Source == "none" {
			for _, root := range byOutcome[n.ID] {
				a := aggregates[root]
				at, by := a.LiveAt, a.LiveBy
				if at == nil {
					at, by = a.ReadyAt, a.ReadyBy
				}
				if at != nil && (value.At == nil || at.After(*value.At)) {
					value = EffectiveETA{Source: "worker", At: at, By: by}
				}
			}
		}
		out[n.ID] = value
	}
	return out, nil
}

// ETACapture belongs to one source-write transaction. The caller holds the
// tenant/tree fence. Snapshot comparison never reads a previous ETA event.
type ETACapture struct {
	goals  []string
	before map[string]EffectiveETA
}

func withAllGoalProjects(ctx context.Context, tx pgx.Tx, fn func() error) error {
	var previous string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&previous); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		return err
	} // The failed source transaction rolls back.
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, previous)
	return err
}
func etaNodes(ctx context.Context, tx pgx.Tx, goalIDs []string) ([]storedNode, error) {
	rows, err := tx.Query(ctx, `SELECT `+nodeColumns+` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.parent_id=ANY($1::uuid[]) AND k.slug='outcome' AND n.deleted_at IS NULL ORDER BY n.id LIMIT 4097`, goalIDs)
	if err != nil {
		return nil, err
	}
	nodes := []storedNode{}
	for rows.Next() {
		n, e := scanNode(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		nodes = append(nodes, n)
	}
	err = rows.Err()
	rows.Close()
	if len(nodes) > 4096 {
		return nil, fail(413, "items: ETA outcome budget exceeded")
	}
	return nodes, err
}
func CaptureGoalETAs(ctx context.Context, tx pgx.Tx, goalIDs []string) (*ETACapture, error) {
	c := &ETACapture{goals: goalIDs}
	if len(goalIDs) == 0 {
		c.before = map[string]EffectiveETA{}
		return c, nil
	}
	err := withAllGoalProjects(ctx, tx, func() error {
		rows, err := tx.Query(ctx, `SELECT id FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR NO KEY UPDATE`, goalIDs)
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
		nodes, err := etaNodes(ctx, tx, goalIDs)
		if err != nil {
			return err
		}
		c.before, err = effectiveETAs(ctx, tx, nodes)
		return err
	})
	return c, err
}

// CaptureTicketETAs covers ancestor work roots too, when a goal serves a parent
// of the reported leaf. UNION terminates cycles and the bounded scan fails
// explicitly rather than losing a source transition.
func CaptureTicketETAs(ctx context.Context, tx pgx.Tx, ticketIDs []string) (*ETACapture, error) {
	goals := []string{}
	err := withAllGoalProjects(ctx, tx, func() error {
		rows, err := tx.Query(ctx, `WITH RECURSIVE ancestors(id,parent_id) AS (
 SELECT id,parent_id FROM nodes WHERE id=ANY($1::uuid[]) UNION SELECT n.id,n.parent_id FROM nodes n JOIN ancestors a ON n.id=a.parent_id)
 SELECT DISTINCT o.parent_id::text FROM ancestors a JOIN node_relations r ON r.target_node_id=a.id AND r.type='serves' JOIN nodes o ON o.id=r.source_node_id AND o.deleted_at IS NULL JOIN node_kinds k ON k.tenant_id=o.tenant_id AND k.id=o.kind_id AND k.slug='outcome' ORDER BY o.parent_id::text LIMIT 201`, ticketIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			goals = append(goals, id)
		}
		err = rows.Err()
		rows.Close()
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(goals) > 200 {
		return nil, fail(413, "ETA affects too many goals")
	}
	return CaptureGoalETAs(ctx, tx, goals)
}
func sameETA(a, b EffectiveETA) bool {
	a.By, b.By = "", ""
	a.Partial, b.Partial = false, false
	return reflect.DeepEqual(a, b)
}
func (c *ETACapture) Changes(ctx context.Context, tx pgx.Tx) ([]events.Change, error) {
	changes := []events.Change{}
	if len(c.goals) == 0 {
		return changes, nil
	}
	err := withAllGoalProjects(ctx, tx, func() error {
		nodes, err := etaNodes(ctx, tx, c.goals)
		if err != nil {
			return err
		}
		after, err := effectiveETAs(ctx, tx, nodes)
		if err != nil {
			return err
		}
		changedGoals := map[string]bool{}
		for _, n := range nodes {
			before, exists := c.before[n.ID]
			if !exists {
				before = EffectiveETA{Source: "none"}
			}
			if sameETA(before, after[n.ID]) {
				continue
			}
			id := n.ID
			changes = append(changes, events.Change{NodeID: &id, Type: "goal.eta_changed", Before: before, After: after[n.ID]})
			changedGoals[n.Parent] = true
		}
		ids := []string{}
		for id := range changedGoals {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			goal, err := resolveNode(ctx, tx, id, false)
			if err != nil {
				return err
			}
			if authoredETA(goal).Source != "none" {
				continue
			}
			w, err := watermark(ctx, tx)
			if err != nil {
				return err
			}
			goalID := id
			changes = append(changes, events.Change{NodeID: &goalID, Type: "goal.eta_changed", After: ChangePing{GoalID: id, Watermark: w}})
		}
		return nil
	})
	return changes, err
}
func (c *ETACapture) Append(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	changes, err := c.Changes(ctx, tx)
	if err != nil {
		return err
	}
	for _, change := range changes {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}
