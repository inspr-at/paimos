// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

const graphNodeLimit = 2000
const graphEdgeLimit = 8000

// Graph is a body-free projection. New(pool) mounts it with the existing knowledge
// module; no additional module or plugin registration is required (AEON-146).
type Graph struct {
	Nodes     []GraphNode `json:"nodes"`
	Edges     []GraphEdge `json:"edges"`
	Truncated bool        `json:"truncated"`
}
type GraphNode struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Type      string    `json:"type"`
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Degree    int       `json:"degree"`
	UpdatedAt time.Time `json:"updated_at"`
	Kind      string    `json:"kind"`
}
type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
}
type graphQuery struct {
	project         string
	types, statuses []string
	tickets         bool
}

func (m *module) handleGraph(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	q := graphQuery{project: v.Get("project_id"), tickets: v.Get("include") == "tickets"}
	if !validUUID(q.project) {
		writeErr(w, fail(400, "invalid_request", "project_id is required"))
		return
	}
	if s := v.Get("include"); s != "" && s != "tickets" {
		writeErr(w, fail(400, "invalid_request", "include is tickets"))
		return
	}
	for _, t := range commaValues(v["types"]) {
		s, ok := specFor(t)
		if !ok {
			writeErr(w, fail(400, "invalid_type", "unknown knowledge type"))
			return
		}
		q.types = append(q.types, s.Kind)
	}
	for _, s := range commaValues(v["status"]) {
		if _, ok := stateFor(s); !ok {
			writeErr(w, fail(400, "invalid_status", "status is active, proposed or archived"))
			return
		}
		q.statuses = append(q.statuses, s)
	}
	var g Graph
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		g, err = loadGraph(r.Context(), tx, p.TenantID, q)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, g)
}

// graphMention preserves typed local links: a link to another project is never
// silently rebound to a same-named entry here. Plain wiki/code slugs prefer the
// source's kind. Key references are tenant-unique and case-sensitive.
type graphMention struct{ Slug, Type, Project, Key string }

var graphWiki = regexp.MustCompile(`\[\[([a-z][a-z0-9_-]{0,63})(?:#[^\]|\n]+)?(?:\|[^\]\n]+)?\]\]`)
var graphMarkdown = regexp.MustCompile(`\[[^\]\n]*\]\((/p/[^\s)]+)(?:\s+"[^"\n]*")?\)`)
var graphCode = regexp.MustCompile("`([a-z][a-z0-9_-]{0,63})`")
var graphKey = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*-[0-9]+\b`)

func parseGraphMentions(body string) []graphMention {
	out := []graphMention{}
	seen := map[graphMention]bool{}
	add := func(m graphMention) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for _, m := range graphWiki.FindAllStringSubmatch(body, -1) {
		add(graphMention{Slug: m[1]})
	}
	for _, m := range graphMarkdown.FindAllStringSubmatch(body, -1) {
		u, err := url.Parse(m[1])
		if err != nil {
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 5 || parts[0] != "p" || parts[2] != "knowledge" {
			continue
		}
		s, ok := specFor(parts[3])
		if !ok || !slugPattern.MatchString(parts[4]) || len(parts[4]) > 64 {
			continue
		}
		add(graphMention{Project: parts[1], Type: s.Kind, Slug: parts[4]})
	}
	// A URL path is not itself a key mention (e.g. /p/PRJ-1/knowledge/...).
	body = graphMarkdown.ReplaceAllString(body, "")
	for _, m := range graphCode.FindAllStringSubmatch(body, -1) {
		add(graphMention{Slug: m[1]})
	}
	for _, m := range graphKey.FindAllStringIndex(body, -1) {
		if (m[0] > 0 && strings.ContainsRune("/-_", rune(body[m[0]-1]))) || (m[1] < len(body) && strings.ContainsRune("/-_", rune(body[m[1]]))) {
			continue
		}
		add(graphMention{Key: body[m[0]:m[1]]})
	}
	return out
}

type graphRef struct {
	Slug   string `json:"slug"`
	Kind   string `json:"kind"`
	Strict bool   `json:"strict"`
}

func loadGraph(ctx context.Context, tx pgx.Tx, tenantID string, q graphQuery) (Graph, error) {
	g := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	var projectKey, prefix string
	err := tx.QueryRow(ctx, `SELECT n.key,coalesce(n.fields->'classic'->>'key',n.fields->>'key_prefix','') FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL AND k.slug='project'`, tenantID, q.project).Scan(&projectKey, &prefix)
	if err != nil {
		return g, err
	}
	types := q.types
	if len(types) == 0 {
		types = kindSlugs()
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text,n.key,k.slug,coalesce(n.fields->>'slug',''),n.title,n.state,n.updated_at,n.body
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id `+nearestProject+`
 WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND proj.id=$2::uuid AND k.slug=ANY($3::text[])
 AND (coalesce(cardinality($4::text[]),0)=0 OR
  (CASE n.state WHEN 'cancelled' THEN 'archived' WHEN 'proposed' THEN 'proposed' ELSE 'active' END)=ANY($4::text[]))
 ORDER BY n.updated_at DESC,n.id LIMIT $5`, tenantID, q.project, types, q.statuses, graphNodeLimit+1)
	if err != nil {
		return g, err
	}
	mentions := map[string][]graphMention{}
	knowledgeIDs := []string{}
	refs := []graphRef{}
	refSeen := map[graphRef]bool{}
	keys := map[string]bool{}
	for rows.Next() {
		var n GraphNode
		var body, kind string
		if err = rows.Scan(&n.ID, &n.Key, &kind, &n.Slug, &n.Title, &n.Status, &n.UpdatedAt, &body); err != nil {
			rows.Close()
			return g, err
		}
		if len(g.Nodes) == graphNodeLimit {
			g.Truncated = true
			break
		}
		n.Kind = "knowledge"
		s, _ := specFor(kind)
		n.Type = s.Type
		n.Status = statusOf(n.Status)
		g.Nodes = append(g.Nodes, n)
		knowledgeIDs = append(knowledgeIDs, n.ID)
		for _, m := range parseGraphMentions(body) {
			if m.Project != "" && m.Project != projectKey && m.Project != prefix && m.Project != q.project {
				continue
			}
			mentions[n.ID] = append(mentions[n.ID], m)
			if m.Key != "" {
				keys[m.Key] = true
				continue
			}
			r := graphRef{Slug: m.Slug, Kind: kind, Strict: m.Type != ""}
			if r.Strict {
				r.Kind = m.Type
			}
			if !refSeen[r] {
				refSeen[r] = true
				refs = append(refs, r)
			}
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return g, err
	}
	if len(g.Nodes) == 0 {
		return g, nil
	}
	// Resolve against the whole live project, before testing graph membership.
	// A filtered-out same-kind entry must not redirect its mention to a different
	// kind with the same slug. Current slugs beat aliases within each kind, just
	// like resolve; history only names candidates, never grants visibility.
	resolved := map[graphRef]string{}
	if len(refs) > 0 {
		encoded, _ := json.Marshal(refs)
		rows, err = tx.Query(ctx, `WITH refs AS MATERIALIZED (
   SELECT * FROM jsonb_to_recordset($3::jsonb) AS x(slug text,kind text,strict boolean)
  ), candidates AS MATERIALIZED (
`+slugCandidatesSQL+`
  ), scoped AS MATERIALIZED (
   SELECT c.* FROM candidates c JOIN nodes n ON n.tenant_id=$1 AND n.id=c.id `+nearestProject+`
   WHERE proj.id=$2::uuid
  ) SELECT r.slug,r.kind,r.strict,found.id::text FROM refs r CROSS JOIN LATERAL (
   SELECT c.id FROM scoped c WHERE c.lookup=r.slug AND (NOT r.strict OR c.kind=r.kind)
   ORDER BY (c.kind=r.kind) DESC,`+slugCandidateOrder+` LIMIT 1
  ) found`, tenantID, q.project, string(encoded), kindSlugs())
		if err != nil {
			return g, err
		}
		for rows.Next() {
			var ref graphRef
			var id string
			if err = rows.Scan(&ref.Slug, &ref.Kind, &ref.Strict, &id); err != nil {
				rows.Close()
				return g, err
			}
			resolved[ref] = id
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return g, err
		}
	}
	if q.tickets {
		keyList := make([]string, 0, len(keys))
		for k := range keys {
			keyList = append(keyList, k)
		}
		sort.Strings(keyList)
		// Only ticket metadata: neither SELECT nor the mention parser reads its body.
		rows, err = tx.Query(ctx, `SELECT n.id::text,n.key,n.title,n.state,n.updated_at FROM nodes n
   JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug='ticket'
   WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND (n.key=ANY($3::text[]) OR EXISTS (
    SELECT 1 FROM node_relations r WHERE r.tenant_id=$1 AND
     ((r.source_node_id=ANY($2::uuid[]) AND r.target_node_id=n.id) OR (r.target_node_id=ANY($2::uuid[]) AND r.source_node_id=n.id))))
   ORDER BY n.updated_at DESC,n.id LIMIT $4`, tenantID, knowledgeIDs, keyList, graphNodeLimit-len(g.Nodes)+1)
		if err != nil {
			return g, err
		}
		for rows.Next() {
			var n GraphNode
			if err = rows.Scan(&n.ID, &n.Key, &n.Title, &n.Status, &n.UpdatedAt); err != nil {
				rows.Close()
				return g, err
			}
			if len(g.Nodes) == graphNodeLimit {
				g.Truncated = true
				break
			}
			n.Kind = "ticket"
			n.Type = "ticket"
			g.Nodes = append(g.Nodes, n)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return g, err
		}
	}
	ids := make([]string, 0, len(g.Nodes))
	members := map[string]bool{}
	byKey := map[string]string{}
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
		members[n.ID] = true
		byKey[n.Key] = n.ID
	}
	pairs := map[[2]string]bool{}
	add := func(e GraphEdge) {
		pair := [2]string{e.Source, e.Target}
		if e.Source == e.Target || !members[e.Source] || !members[e.Target] || pairs[pair] {
			return
		}
		if len(g.Edges) == graphEdgeLimit {
			g.Truncated = true
			return
		}
		pairs[pair] = true
		g.Edges = append(g.Edges, e)
	}
	rows, err = tx.Query(ctx, `SELECT source_node_id::text,target_node_id::text,string_agg(DISTINCT type,', ' ORDER BY type)
 FROM node_relations WHERE tenant_id=$1 AND source_node_id=ANY($2::uuid[]) AND target_node_id=ANY($2::uuid[])
 AND source_node_id<>target_node_id
 GROUP BY source_node_id,target_node_id ORDER BY source_node_id,target_node_id LIMIT $3`, tenantID, ids, graphEdgeLimit+1)
	if err != nil {
		return g, err
	}
	for rows.Next() {
		e := GraphEdge{Kind: "relation"}
		if err = rows.Scan(&e.Source, &e.Target, &e.Label); err != nil {
			rows.Close()
			return g, err
		}
		add(e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return g, err
	}
	for _, n := range g.Nodes {
		if n.Kind == "ticket" {
			continue
		}
		s, _ := specFor(n.Type)
		for _, m := range mentions[n.ID] {
			id := byKey[m.Key]
			if m.Key == "" {
				r := graphRef{Slug: m.Slug, Kind: s.Kind, Strict: m.Type != ""}
				if r.Strict {
					r.Kind = m.Type
				}
				id = resolved[r]
			}
			add(GraphEdge{Source: n.ID, Target: id, Kind: "mention", Label: "mentions"})
		}
	}
	neighbours := map[string]map[string]bool{}
	for _, e := range g.Edges {
		if neighbours[e.Source] == nil {
			neighbours[e.Source] = map[string]bool{}
		}
		if neighbours[e.Target] == nil {
			neighbours[e.Target] = map[string]bool{}
		}
		neighbours[e.Source][e.Target] = true
		neighbours[e.Target][e.Source] = true
	}
	for i := range g.Nodes {
		g.Nodes[i].Degree = len(neighbours[g.Nodes[i].ID])
	}
	return g, nil
}
