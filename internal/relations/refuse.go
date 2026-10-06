// SPDX-License-Identifier: AGPL-3.0-only

package relations

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// refusal is a 409 whose message a person can read as it stands, for example
// in the relation picker. It names the items by key.
type refusal struct{ message string }

func (e refusal) Error() string { return e.message }

// acyclic lists the relation types that cannot loop: a chain of blockers that
// ends where it started never resolves, and implementation or duplication
// running in a circle has no original.
func acyclic(relType string) bool {
	switch relType {
	case "blocks", "implements", "duplicates":
		return true
	}
	return false
}

// Bounds keep the loop search cheap on imported graphs. An unexplored frontier
// is refused: exhaustion cannot prove that an acyclic link is safe.
const (
	maxLoopDepth   = 64
	maxLoopVisited = 5000
)

// verb phrases for "SRC ___ DST" in present tense, and the base form used
// after "cannot".
var verbs = map[string][2]string{
	"blocks":      {"blocks", "block"},
	"relates":     {"relates to", "relate to"},
	"implements":  {"implements", "implement"},
	"cites":       {"cites", "cite"},
	"duplicates":  {"duplicates", "duplicate"},
	"customer_of": {"is the customer of", "be the customer of"},
	"contact_for": {"is a contact for", "be a contact for"},
}

// refuse runs after both endpoints are locked. It turns an existing identical
// link, and a link that would close a loop, into a readable refusal before the
// insert would fail on a constraint or silently create a deadlock of blockers.
func refuse(ctx context.Context, tx pgx.Tx, tenantID, source, target, relType string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_relations
		WHERE tenant_id=$1 AND source_node_id=$2 AND target_node_id=$3 AND type=$4)`,
		tenantID, source, target, relType).Scan(&exists); err != nil {
		return err
	}
	if exists {
		keys, err := nodeKeys(ctx, tx, tenantID, []string{source, target})
		if err != nil {
			return err
		}
		return refusal{fmt.Sprintf("%s already %s %s.", keys[source], verbs[relType][0], keys[target])}
	}
	if !acyclic(relType) {
		return nil
	}
	path, err := loopPath(ctx, tx, tenantID, source, target, relType)
	if err != nil || path == nil {
		return err
	}
	keys, err := nodeKeys(ctx, tx, tenantID, path)
	if err != nil {
		return err
	}
	verb := verbs[relType]
	var chain strings.Builder
	if len(path) == 2 {
		fmt.Fprintf(&chain, "%s already %s %s", keys[path[0]], verb[0], keys[path[1]])
	} else {
		fmt.Fprintf(&chain, "%s %s %s", keys[path[0]], verb[0], keys[path[1]])
		for _, id := range path[2:] {
			fmt.Fprintf(&chain, ", which %s %s", verb[0], keys[id])
		}
	}
	return refusal{fmt.Sprintf("%s cannot %s %s: %s, so this link would make a loop.",
		keys[source], verb[1], keys[target], chain.String())}
}

// loopPath returns the shortest chain of relType links leading from target
// back to source, as node IDs from target to source, or nil when there is none
// among live nodes. Breadth first with one query per level.
func loopPath(ctx context.Context, tx pgx.Tx, tenantID, source, target, relType string) ([]string, error) {
	parent := map[string]string{target: ""}
	frontier := []string{target}
	for depth := 0; len(frontier) > 0 && depth < maxLoopDepth && len(parent) < maxLoopVisited; depth++ {
		next, found, err := step(ctx, tx, tenantID, relType, frontier, source, parent)
		if err != nil {
			return nil, err
		}
		if found {
			path := []string{}
			for at := source; at != ""; at = parent[at] {
				path = append(path, at)
			}
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			return path, nil
		}
		frontier = next
	}
	if len(frontier) > 0 {
		return nil, loopSearchLimit()
	}
	return nil, nil
}

func loopSearchLimit() error {
	return refusal{"Cannot prove this link is acyclic within the search limit. Shorten or simplify the relation graph before trying again."}
}

func step(ctx context.Context, tx pgx.Tx, tenantID, relType string, frontier []string, source string, parent map[string]string) ([]string, bool, error) {
	rows, err := tx.Query(ctx, `SELECT r.source_node_id::text, r.target_node_id::text
		FROM node_relations r
		JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.target_node_id AND n.deleted_at IS NULL
		WHERE r.tenant_id=$1 AND r.type=$2 AND r.source_node_id = ANY($3::uuid[])
		ORDER BY r.source_node_id, r.target_node_id LIMIT $4`, tenantID, relType, frontier, maxLoopVisited+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var next []string
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > maxLoopVisited {
			return nil, false, loopSearchLimit()
		}
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, false, err
		}
		if _, seen := parent[to]; seen {
			continue
		}
		if len(parent) >= maxLoopVisited {
			return nil, false, loopSearchLimit()
		}
		parent[to] = from
		if to == source {
			return nil, true, nil
		}
		next = append(next, to)
	}
	return next, false, rows.Err()
}

func nodeKeys(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, key FROM nodes WHERE tenant_id=$1 AND id = ANY($2::uuid[])`, tenantID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make(map[string]string, len(ids))
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		keys[id] = key
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if keys[id] == "" {
			keys[id] = "an item"
		}
	}
	return keys, nil
}
