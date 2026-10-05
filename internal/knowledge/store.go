// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// nearestProject is a LATERAL subquery naming the closest live project above
// n (n itself is never its own project). Callers alias it "proj".
const nearestProject = `LEFT JOIN LATERAL (
    WITH RECURSIVE up AS (
        SELECT a.id, a.parent_id, a.kind_id, a.key, a.title, 1 AS depth
        FROM nodes a WHERE a.tenant_id=n.tenant_id AND a.id=n.parent_id AND a.deleted_at IS NULL
        UNION ALL
        SELECT a.id, a.parent_id, a.kind_id, a.key, a.title, up.depth+1 FROM up
        JOIN node_kinds uk ON uk.tenant_id=n.tenant_id AND uk.id=up.kind_id AND uk.slug<>'project'
        JOIN nodes a ON a.tenant_id=n.tenant_id AND a.id=up.parent_id AND a.deleted_at IS NULL
        WHERE up.depth<32
    ) SELECT u.id, u.key, u.title FROM up u
      JOIN node_kinds pk ON pk.tenant_id=n.tenant_id AND pk.id=u.kind_id
      WHERE pk.slug='project' ORDER BY u.depth LIMIT 1
) proj ON true`

// lastWrite is the newest content write on n and who made it (linked principals resolve).
const lastWrite = `LEFT JOIN LATERAL (
    SELECT e.actor_principal_id AS actor, e.type FROM events e
    WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id
      AND e.type IN ('knowledge.created','knowledge.updated','knowledge.learning_accepted','node.created','node.updated','import.node_created','import.node_updated')
    ORDER BY e.id DESC LIMIT 1
) lw ON true
LEFT JOIN principals lwp ON lwp.tenant_id=n.tenant_id AND lwp.id=lw.actor
LEFT JOIN principals lwt ON lwt.tenant_id=n.tenant_id AND lwt.id=lwp.linked_to`

const linkCount = `(SELECT count(*)::int FROM node_relations r
    JOIN nodes o ON o.tenant_id=r.tenant_id AND o.deleted_at IS NULL
     AND o.id = CASE WHEN r.source_node_id=n.id THEN r.target_node_id ELSE r.source_node_id END
    WHERE r.tenant_id=n.tenant_id AND (r.source_node_id=n.id OR r.target_node_id=n.id))`

const itemColumns = `n.id::text, n.key, k.slug, coalesce(n.fields->>'slug',''), n.title, n.state,
    proj.id::text, proj.key, proj.title, n.created_at, n.updated_at,
    coalesce(lwt.id, lwp.id)::text, coalesce(lwt.name, lwp.name), coalesce(lw.type,''), ` + linkCount

type listQuery struct {
	ShipsIn   string
	Cursor    string
	ProjectID string
	Types     []string // kind slugs; empty = all
	Statuses  []string
	Q         string
	Sort      string // relevance, updated, created, title, slug, type
	Limit     int
}

// ListPage is GET /api/knowledge.
type ListPage struct {
	NextCursor       string                    `json:"next_cursor,omitempty"`
	CountsIncomplete bool                      `json:"counts_incomplete,omitempty"`
	Items            []Item                    `json:"items"`
	Total            int                       `json:"total"`
	Truncated        bool                      `json:"truncated"`
	Counts           map[string]map[string]int `json:"counts"`
}

// scanLimit bounds the rows a list reads; knowledge is small next to work.
const scanLimit = 5000

type scoredItem struct {
	Item
	score float64
}

func list(ctx context.Context, tx pgx.Tx, tenantID string, q listQuery) (ListPage, error) {
	words := queryWords(q.Q)
	patterns := make([]string, 0, len(words))
	for _, w := range words {
		patterns = append(patterns, likePattern(w))
	}
	whole := strings.TrimSpace(q.Q)
	var project any
	if q.ProjectID != "" {
		project = q.ProjectID
	}
	rows, err := tx.Query(ctx, `
WITH kinds AS (SELECT id, slug FROM node_kinds WHERE tenant_id=$1 AND slug = ANY($2::text[])),
terms AS (SELECT websearch_to_tsquery('german', $3) || websearch_to_tsquery('english', $3) AS q)
SELECT `+itemColumns+`,
    CASE WHEN $6::text<>'' AND strpos(lower(n.body), lower($6))>0
         THEN substr(n.body, greatest(1, strpos(lower(n.body), lower($6))-300), 900)
         ELSE left(n.body, 900) END,
    $6::text<>'' AND strpos(lower(n.body), lower($6))>300,
    CASE WHEN $3::text='' THEN 0 ELSE
        (CASE WHEN lower(coalesce(n.fields->>'slug',''))=lower($3) OR lower(n.key)=lower($3) THEN 100 ELSE 0 END)
      + (CASE WHEN n.title ILIKE $7 ESCAPE '\' THEN 40 ELSE 0 END)
      + (CASE WHEN NOT EXISTS (SELECT 1 FROM unnest($4::text[]) w WHERE NOT (n.title ILIKE w ESCAPE '\')) AND cardinality($4::text[])>0 THEN 25 ELSE 0 END)
      + (CASE WHEN NOT EXISTS (SELECT 1 FROM unnest($4::text[]) w WHERE NOT (coalesce(n.fields->>'slug','') ILIKE w ESCAPE '\')) AND cardinality($4::text[])>0 THEN 15 ELSE 0 END)
      + 10 * ts_rank_cd(n.search_document, t.q)
    END::float8
FROM nodes n
JOIN kinds k ON k.id=n.kind_id
CROSS JOIN terms t
`+nearestProject+`
`+lastWrite+`
WHERE n.tenant_id=$1 AND n.deleted_at IS NULL
 AND (k.slug<>'decision' OR coalesce(n.fields->>'slug','')<>'')
  AND ($5::uuid IS NULL OR proj.id=$5::uuid)
  AND ($3::text='' OR n.search_document @@ t.q
       OR n.title ILIKE $7 ESCAPE '\' OR coalesce(n.fields->>'slug','') ILIKE $7 ESCAPE '\'
       OR (cardinality($4::text[])>0 AND NOT EXISTS (SELECT 1 FROM unnest($4::text[]) w WHERE NOT (
            n.title ILIKE w ESCAPE '\' OR coalesce(n.fields->>'slug','') ILIKE w ESCAPE '\'
            OR n.key ILIKE w ESCAPE '\' OR n.body ILIKE w ESCAPE '\'))))
ORDER BY n.updated_at DESC, n.id
LIMIT $8`, tenantID, kindSlugs(), whole, patterns, project, longest(words), likePattern(whole)[1:], scanLimit+1)
	if err != nil {
		return ListPage{}, err
	}
	defer rows.Close()
	page := ListPage{Items: []Item{}, Counts: map[string]map[string]int{"type": {}, "status": {}}}
	var all []scoredItem
	for rows.Next() {
		var it scoredItem
		var head string
		var cut bool
		if err := scanItem(rows, &it.Item, &head, &cut, &it.score); err != nil {
			return ListPage{}, err
		}
		it.Excerpt = excerpt(head, it.Title, words, cut)
		all = append(all, it)
	}
	if err := rows.Err(); err != nil {
		return ListPage{}, err
	}
	if len(all) > scanLimit {
		all = all[:scanLimit]
		page.Truncated = true
	}
	types := setOf(q.Types)
	statuses := setOf(q.Statuses)
	kept := all[:0]
	for _, it := range all {
		page.Counts["type"][it.Type]++
		page.Counts["status"][it.Status]++
		if (len(types) == 0 || types[it.Kind]) && (len(statuses) == 0 || statuses[it.Status]) {
			kept = append(kept, it)
		}
	}
	sortItems(kept, q.Sort, whole != "")
	page.Total = len(kept)
	if q.Limit > 0 && len(kept) > q.Limit {
		kept = kept[:q.Limit]
		page.Truncated = true
	}
	for _, it := range kept {
		page.Items = append(page.Items, it.Item)
	}
	return page, nil
}

func setOf(values []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		out[v] = true
	}
	return out
}

func typeOrder(kind string) int {
	for i, s := range specs {
		if s.Kind == kind {
			return i
		}
	}
	return len(specs)
}

func sortItems(items []scoredItem, by string, searching bool) {
	if by == "" {
		by = "updated"
		if searching {
			by = "relevance"
		}
	}
	less := func(a, b scoredItem) int {
		switch by {
		case "relevance":
			if a.score != b.score {
				if a.score > b.score {
					return -1
				}
				return 1
			}
		case "created":
			if !a.CreatedAt.Equal(b.CreatedAt) {
				if a.CreatedAt.After(b.CreatedAt) {
					return -1
				}
				return 1
			}
		case "title":
			if c := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
				return c
			}
		case "slug":
			if c := strings.Compare(a.Slug, b.Slug); c != 0 {
				return c
			}
		case "type":
			if d := typeOrder(a.Kind) - typeOrder(b.Kind); d != 0 {
				return d
			}
			if c := strings.Compare(a.Slug, b.Slug); c != 0 {
				return c
			}
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			if a.UpdatedAt.After(b.UpdatedAt) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	}
	sort.SliceStable(items, func(i, j int) bool { return less(items[i], items[j]) < 0 })
}

// scanItem reads itemColumns plus the extra destinations.
func scanItem(row pgx.Row, it *Item, extra ...any) error {
	var projID, projKey, projTitle, whoID, whoName pgtype.Text
	var lastType string
	dest := []any{&it.ID, &it.Key, &it.Kind, &it.Slug, &it.Title, &it.State, &projID, &projKey, &projTitle,
		&it.CreatedAt, &it.UpdatedAt, &whoID, &whoName, &lastType, &it.LinkCount}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	if s, ok := specFor(it.Kind); ok {
		it.Type = s.Type
	}
	it.Status = statusOf(it.State)
	if projID.Valid {
		it.Project = &ProjectRef{ID: projID.String, Key: projKey.String, Title: projTitle.String}
	}
	it.Imported = strings.HasPrefix(lastType, "import.")
	if whoID.Valid && !it.Imported {
		it.UpdatedBy = &Person{ID: whoID.String, Name: whoName.String}
	}
	return nil
}

// ---------- One entry ----------

var errNotFound = errors.New("knowledge entry not found")

// loadEntry reads a live entry (or a deleted one when deleted is true) with
// its body, author and links.
func loadEntry(ctx context.Context, tx pgx.Tx, tenantID, id string, deleted bool) (Entry, error) {
	var e Entry
	var fields []byte
	err := scanItem(tx.QueryRow(ctx, `
SELECT `+itemColumns+`, n.body, n.fields
FROM nodes n
JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug = ANY($3::text[]) AND (k.slug<>'decision' OR coalesce(n.fields->>'slug','')<>'')
`+nearestProject+`
`+lastWrite+`
WHERE n.tenant_id=$1 AND n.id=$2::uuid AND (n.deleted_at IS NULL OR $4::bool)`, tenantID, id, kindSlugs(), deleted), &e.Item, &e.Body, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, errNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	var f map[string]any
	_ = json.Unmarshal(fields, &f)
	e.Metadata, _ = f["metadata"].(map[string]any)
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}
	e.Excerpt = excerpt(e.Body, e.Title, nil, false)
	if e.Author, err = author(ctx, tx, tenantID, id, f); err != nil {
		return Entry{}, err
	}
	if e.Links, err = links(ctx, tx, tenantID, id); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// author: the classic creator when the import kept one, otherwise whoever created it here.
func author(ctx context.Context, tx pgx.Tx, tenantID, id string, fields map[string]any) (*Person, error) {
	if created, _ := fields["created_by"].(string); validUUID(created) {
		if p, err := person(ctx, tx, tenantID, created); err != nil || p != nil {
			return p, err
		}
	}
	var typ, actor string
	err := tx.QueryRow(ctx, `SELECT type, actor_principal_id::text FROM events
	  WHERE tenant_id=$1 AND node_id=$2::uuid AND type IN ('knowledge.created','node.created','import.node_created')
	  ORDER BY id LIMIT 1`, tenantID, id).Scan(&typ, &actor)
	if errors.Is(err, pgx.ErrNoRows) || typ == "import.node_created" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return person(ctx, tx, tenantID, actor)
}

func person(ctx context.Context, tx pgx.Tx, tenantID, id string) (*Person, error) {
	var p Person
	err := tx.QueryRow(ctx, `SELECT coalesce(t.id, p.id)::text, coalesce(t.name, p.name)
	  FROM principals p LEFT JOIN principals t ON t.tenant_id=p.tenant_id AND t.id=p.linked_to
	  WHERE p.tenant_id=$1 AND p.id=$2::uuid`, tenantID, id).Scan(&p.ID, &p.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func links(ctx context.Context, tx pgx.Tx, tenantID, id string) ([]Link, error) {
	rows, err := tx.Query(ctx, `
SELECT r.id::text, r.type, CASE WHEN r.source_node_id=$2::uuid THEN 'out' ELSE 'in' END,
       n.id::text, n.key, n.title, n.state, k.slug, proj.id::text, coalesce(n.fields->>'slug','')
FROM node_relations r
JOIN nodes n ON n.tenant_id=r.tenant_id AND n.deleted_at IS NULL
 AND n.id = CASE WHEN r.source_node_id=$2::uuid THEN r.target_node_id ELSE r.source_node_id END
JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
`+nearestProject+`
WHERE r.tenant_id=$1 AND (r.source_node_id=$2::uuid OR r.target_node_id=$2::uuid)
ORDER BY n.key, r.type`, tenantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		var project pgtype.Text
		var slug string
		if err := rows.Scan(&l.RelationID, &l.Type, &l.Direction, &l.Node.ID, &l.Node.Key, &l.Node.Title, &l.Node.State, &l.Node.Kind, &project, &slug); err != nil {
			return nil, err
		}
		if project.Valid {
			l.Node.ProjectID = &project.String
		}
		if s, ok := specFor(l.Node.Kind); ok {
			l.Node.Type = s.Type
			l.Node.Slug = slug
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// slugCandidatesSQL is shared by URL resolution and graph mentions. refs
// names the requested slugs, $1 the tenant and $4 the eligible knowledge kinds.
// History provides evidence of a rename, never current visibility or authority.
const slugCandidatesSQL = `   SELECT n.id,k.slug AS kind,n.fields->>'slug' AS lookup,n.updated_at,true AS current,0::bigint AS rename_id
   FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND k.slug=ANY($4::text[])
    AND n.fields ? 'slug' AND n.fields->>'slug' IN (SELECT slug FROM refs)
   UNION ALL
   SELECT n.id,k.slug,e.before->'fields'->>'slug',n.updated_at,false,e.id AS rename_id
   FROM events e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id
    JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   WHERE e.tenant_id=$1 AND n.deleted_at IS NULL AND k.slug=ANY($4::text[])
    AND e.type IN ('knowledge.updated','node.updated') AND e.undo_of IS NULL
    AND e.before->'fields' ? 'slug' AND e.before->'fields'->>'slug' IN (SELECT slug FROM refs)
    AND e.before->>'kind_id'=n.kind_id::text
    AND (e.after->'fields'->>'slug') IS DISTINCT FROM (e.before->'fields'->>'slug')`

// Current slugs win; aliases are ordered by immutable rename events. A body
// edit cannot change an alias destination. id is a stable final tie-breaker.
const slugCandidateOrder = `current DESC,rename_id DESC,updated_at DESC,id`

// resolve finds a live entry by slug, then the newest visible rename away from it.
func resolve(ctx context.Context, tx pgx.Tx, tenantID, projectID string, s spec, slug string) (Entry, error) {
	var id string
	var current bool
	err := tx.QueryRow(ctx, `WITH refs AS (SELECT $3::text AS slug), candidates AS (
 `+slugCandidatesSQL+`
 ), scoped AS (
 SELECT c.* FROM candidates c JOIN nodes n ON n.tenant_id=$1 AND n.id=c.id `+nearestProject+`
 WHERE proj.id=$2::uuid
 ) SELECT id::text,current FROM scoped ORDER BY `+slugCandidateOrder+` LIMIT 1`, tenantID, projectID, slug, []string{s.Kind}).Scan(&id, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, errNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	entry, err := loadEntry(ctx, tx, tenantID, id, false)
	if err == nil && !current {
		entry.RenamedFrom = slug
	}
	return entry, err
}

// ---------- Writes ----------

const nodeReturning = `id::text, key, kind_id::text, title, body, fields, state, parent_id::text, position::text, created_at, updated_at, deleted_at`

func scanSnap(row pgx.Row) (nodeSnap, error) {
	var n nodeSnap
	var fields []byte
	if err := row.Scan(&n.ID, &n.Key, &n.KindID, &n.Title, &n.Body, &fields, &n.State, &n.ParentID, &n.Position, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt); err != nil {
		return nodeSnap{}, err
	}
	if len(fields) == 0 {
		fields = []byte(`{}`)
	}
	n.Fields = fields
	n.Position = trimDecimal(n.Position)
	return n, nil
}

// trimDecimal matches the nodes package's position text ("3.000" -> "3").
func trimDecimal(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// lockNode reads a knowledge node for update; deleted selects soft-deleted rows too.
func lockNode(ctx context.Context, tx pgx.Tx, tenantID, id string, deleted bool) (nodeSnap, string, error) {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  WHERE n.tenant_id=$1 AND n.id=$2::uuid AND k.slug = ANY($3::text[]) AND (k.slug<>'decision' OR coalesce(n.fields->>'slug','')<>'') AND (n.deleted_at IS NULL OR $4::bool) FOR UPDATE OF n`,
		tenantID, id, kindSlugs(), deleted).Scan(&kind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nodeSnap{}, "", errNotFound
		}
		return nodeSnap{}, "", err
	}
	if kind == "decision" {
		return nodeSnap{}, "", fail(403, "decision_service_required", "Correct Decisions through the Decision Desk to preserve their history.")
	}
	snap, err := scanSnap(tx.QueryRow(ctx, `SELECT `+nodeReturning+` FROM nodes WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, id))
	return snap, kind, err
}

// projectOf is the nearest project above a node, or "" for none.
func projectOf(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, error) {
	var project pgtype.Text
	err := tx.QueryRow(ctx, `SELECT proj.id::text FROM nodes n `+nearestProject+` WHERE n.tenant_id=$1 AND n.id=$2::uuid`, tenantID, id).Scan(&project)
	if err != nil {
		return "", err
	}
	return project.String, nil
}

// lockSlugs serializes slug checks for one project and kind.
func lockSlugs(ctx context.Context, tx pgx.Tx, tenantID, projectID, kindID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 126))`, tenantID+":knowledge:"+projectID+":"+kindID)
	return err
}

// slugTaken returns the live entry of this project and kind that holds slug, other than self.
func slugTaken(ctx context.Context, tx pgx.Tx, tenantID, projectID, kindID, slug, self string) (*Item, error) {
	var it Item
	var project any
	if projectID != "" {
		project = projectID
	}
	var selfArg any
	if self != "" {
		selfArg = self
	}
	err := tx.QueryRow(ctx, `SELECT n.id::text, n.key, n.title FROM nodes n `+nearestProject+`
	  WHERE n.tenant_id=$1 AND n.kind_id=$2::uuid AND n.deleted_at IS NULL AND n.fields->>'slug'=$3
	    AND ($4::uuid IS NULL OR n.id<>$4::uuid) AND proj.id IS NOT DISTINCT FROM $5::uuid
	  LIMIT 1`, tenantID, kindID, slug, selfArg, project).Scan(&it.ID, &it.Key, &it.Title)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	it.Slug = slug
	return &it, nil
}

func validUUID(s string) bool {
	var u pgtype.UUID
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' && u.Scan(s) == nil && u.Valid
}

// bumpUpdated is the updated_at expression every write uses: strictly later
// than the prior value, so If-Unmodified-Since can never match twice.
const bumpUpdated = `greatest(clock_timestamp(), updated_at + interval '1 microsecond')`
