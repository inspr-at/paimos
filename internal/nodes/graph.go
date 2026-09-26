// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ticketGraphNodeLimit is the most nodes one answer returns, newest first.
const ticketGraphNodeLimit = 1500

// TicketGraph is a body-free projection of one project's tickets and epics.
// New mounts it on the existing nodes module; there is no plugin manifest.
// The read runs inside db.InTenant and writes no event (AEON-196).
type TicketGraph struct {
	Nodes     []TicketGraphNode `json:"nodes"`
	Links     []TicketGraphLink `json:"links"`
	Truncated bool              `json:"truncated"`
}

// TicketGraphNode is one ticket or epic. ParentID is set only when that parent
// is a visible ticket or epic; an invisible parent's id is never returned.
// ReleaseID is the journey release when that release node is visible.
type TicketGraphNode struct {
	ID             string    `json:"id"`
	Key            string    `json:"key"`
	Title          string    `json:"title"`
	Type           string    `json:"type"`
	Status         string    `json:"status"`
	StatusCategory string    `json:"status_category"`
	Priority       *string   `json:"priority"`
	ParentID       *string   `json:"parent_id"`
	ReleaseID      *string   `json:"release_id"`
	UpdatedAt      time.Time `json:"updated_at"`
	LinkCount      int       `json:"link_count"`
}

// TicketGraphLink is one edge. blocks, relates, implements and duplicates keep
// the direction stored on node_relations (relates already has the smaller id
// as source). parent points from the child to its parent epic or ticket.
type TicketGraphLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

// Doing states match the list and the ticket UI (active is in-progress).
// Done states are the closed ones, including cancelled and archived.
const ticketGraphCategorySQL = `CASE
 WHEN n.state IN ('in_progress','in-progress','active','qa') THEN 'doing'
 WHEN n.state IN ('accepted','delivered','done','cancelled','canceled','archived') THEN 'done'
 ELSE 'open' END`

func (m *Module) handleTicketGraph(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	projectID, ok := parseUUID(r.URL.Query().Get("project_id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return
	}
	includeClosed, err := queryBool(r, "include_closed")
	if err != nil {
		writeErr(w, err)
		return
	}
	var g TicketGraph
	err = m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var loadErr error
		g, loadErr = loadTicketGraph(ctx, tx, p.TenantID, projectID, includeClosed)
		return loadErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func loadTicketGraph(ctx context.Context, tx pgx.Tx, tenantID, projectID string, includeClosed bool) (TicketGraph, error) {
	g := TicketGraph{Nodes: []TicketGraphNode{}, Links: []TicketGraphLink{}}
	var one int
	err := tx.QueryRow(ctx, `SELECT /* ticket-graph-project */ 1
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL AND k.slug='project'`, tenantID, projectID).Scan(&one)
	if err != nil {
		return g, err
	}
	// One set query. Bodies are not selected. Parent and release ids come from
	// a join, so a row the caller cannot see contributes no id.
	rows, err := tx.Query(ctx, `SELECT /* ticket-graph-nodes */ n.id::text, n.key, n.title, k.slug, n.state, `+ticketGraphCategorySQL+`,
 NULLIF(n.fields->>'priority',''),
 CASE WHEN parent_kind.slug IS NOT NULL THEN parent_node.id::text END,
 CASE WHEN release_node.id IS NOT NULL THEN release_node.id::text END,
 n.updated_at
 FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 LEFT JOIN nodes parent_node ON parent_node.tenant_id=n.tenant_id AND parent_node.id=n.parent_id AND parent_node.deleted_at IS NULL
 LEFT JOIN node_kinds parent_kind ON parent_kind.tenant_id=parent_node.tenant_id AND parent_kind.id=parent_node.kind_id
   AND parent_kind.slug IN ('ticket','epic')
 LEFT JOIN journey_tickets jt ON jt.tenant_id=n.tenant_id AND jt.ticket_node_id=n.id AND jt.project_node_id=$2::uuid
 LEFT JOIN nodes release_node ON release_node.tenant_id=n.tenant_id AND release_node.id=jt.release_node_id AND release_node.deleted_at IS NULL
 WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND n.project_id=$2::uuid
   AND k.slug IN ('ticket','epic')
   AND ($3::bool OR (`+ticketGraphCategorySQL+`) <> 'done')
 ORDER BY n.updated_at DESC, n.id
 LIMIT $4`, tenantID, projectID, includeClosed, ticketGraphNodeLimit+1)
	if err != nil {
		return g, err
	}
	for rows.Next() {
		var n TicketGraphNode
		if err = rows.Scan(&n.ID, &n.Key, &n.Title, &n.Type, &n.Status, &n.StatusCategory, &n.Priority, &n.ParentID, &n.ReleaseID, &n.UpdatedAt); err != nil {
			rows.Close()
			return g, err
		}
		if len(g.Nodes) == ticketGraphNodeLimit {
			g.Truncated = true
			break
		}
		g.Nodes = append(g.Nodes, n)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return g, err
	}
	rows.Close()
	if len(g.Nodes) == 0 {
		return g, nil
	}
	ids := make([]string, len(g.Nodes))
	members := make(map[string]bool, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[i] = n.ID
		members[n.ID] = true
	}
	// Both ends must be in the returned set. Row-level security already hides a
	// relation when either end is in a project the caller cannot see, so that
	// end's id never arrives here.
	rows, err = tx.Query(ctx, `SELECT /* ticket-graph-links */ source_node_id::text, target_node_id::text, type
 FROM node_relations
 WHERE tenant_id=$1 AND type IN ('blocks','relates','implements','duplicates')
   AND source_node_id=ANY($2::uuid[]) AND target_node_id=ANY($2::uuid[])
 ORDER BY source_node_id, target_node_id, type`, tenantID, ids)
	if err != nil {
		return g, err
	}
	for rows.Next() {
		var link TicketGraphLink
		if err = rows.Scan(&link.Source, &link.Target, &link.Kind); err != nil {
			rows.Close()
			return g, err
		}
		if link.Source == link.Target || !members[link.Source] || !members[link.Target] {
			continue
		}
		g.Links = append(g.Links, link)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return g, err
	}
	rows.Close()
	for _, n := range g.Nodes {
		if n.ParentID == nil || *n.ParentID == n.ID || !members[*n.ParentID] {
			continue
		}
		g.Links = append(g.Links, TicketGraphLink{Source: n.ID, Target: *n.ParentID, Kind: "parent"})
	}
	slices.SortFunc(g.Links, func(a, b TicketGraphLink) int {
		if c := strings.Compare(a.Source, b.Source); c != 0 {
			return c
		}
		if c := strings.Compare(a.Target, b.Target); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	counts := map[string]int{}
	for _, link := range g.Links {
		counts[link.Source]++
		counts[link.Target]++
	}
	for i := range g.Nodes {
		g.Nodes[i].LinkCount = counts[g.Nodes[i].ID]
	}
	return g, nil
}
