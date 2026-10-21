// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/jackc/pgx/v5"
)

// AEON-1618: people link work to a goal without ever naming an outcome. The
// server keeps those links on one implicit outcome per goal, marked by the
// linked_work field, under a ref that is unavailable to agents' item upserts.
// Agent item edits never see it (replace keeps it, moves and removals cannot
// reach it), and readers never show it as an outcome: each linked work item
// becomes its own item, with its own state, ETA and progress weight.
const (
	LinkedRef   = "people-links"
	linkedField = "linked_work"
	// Linked items sort after the agents' outcomes.
	linkedPosition = "100000000000000"
	// The outcome visibility policy fails closed above 20 serves ends, so one
	// implicit outcome holds at most that many linked items.
	MaxLinked = MaxTargets
)

func isLinked(n storedNode) bool { linked, _ := n.Fields[linkedField].(bool); return linked }

// isLinkedItem is one split item: its id is the work item it stands for.
func isLinkedItem(n storedNode) bool {
	return isLinked(n) && len(n.Targets) == 1 && n.Targets[0] == n.ID
}

// splitLinked replaces the implicit outcome by one item per serves end, in
// place. The result is what detail, list, revalidation and series all count.
func splitLinked(children []storedNode) []storedNode {
	out := make([]storedNode, 0, len(children))
	for _, n := range children {
		if !isLinked(n) || isLinkedItem(n) {
			out = append(out, n)
			continue
		}
		for _, target := range n.Targets {
			out = append(out, storedNode{ID: target, Parent: n.Parent, Project: n.Project, Slug: OutcomeKind, Position: n.Position,
				Fields: map[string]any{linkedField: true}, Targets: []string{target}})
		}
	}
	return out
}

// linkedETAs returns the worker ETA of each split item's live work root; the
// implicit outcome itself never carries an authored ETA.
func linkedETAs(ctx context.Context, tx pgx.Tx, children []storedNode) (map[string]EffectiveETA, error) {
	ids := []string{}
	for _, n := range children {
		if isLinkedItem(n) {
			ids = append(ids, n.ID)
		}
	}
	out := map[string]EffectiveETA{}
	if len(ids) == 0 {
		return out, nil
	}
	roots, err := filterWorkRoots(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	aggregates, err := eta.LoadAggregates(ctx, tx, roots)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		value := EffectiveETA{Source: "none"}
		a := aggregates[id]
		at, by := a.LiveAt, a.LiveBy
		if at == nil {
			at, by = a.ReadyAt, a.ReadyBy
		}
		if at != nil {
			utc := at.UTC()
			value = EffectiveETA{Source: "worker", At: &utc, By: by}
		}
		out[id] = value
	}
	return out, nil
}
