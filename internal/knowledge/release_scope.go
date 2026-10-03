// SPDX-License-Identifier: AGPL-3.0-only
package knowledge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workquery"
	"github.com/jackc/pgx/v5"
)

type contextCursor struct {
	Hash, ID, Value string
	Updated         time.Time
}

func scopedKnowledge(ctx context.Context, tx pgx.Tx, p tenant.Principal, q listQuery) (ListPage, error) {
	out := ListPage{Items: []Item{}, Counts: map[string]map[string]int{"type": {}, "status": {}}}
	for _, permission := range []string{"knowledge.read", "nodes.read", "releases.read"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: q.ProjectID}); err != nil {
			return out, err
		}
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery d JOIN nodes pn ON pn.tenant_id=d.tenant_id AND pn.id=d.project_node_id AND pn.deleted_at IS NULL WHERE d.tenant_id=$1 AND d.project_node_id=$2)`, p.TenantID, q.ProjectID).Scan(&exists); err != nil {
		return out, err
	}
	if !exists {
		return out, errNotFound
	}
	if q.ShipsIn != "none" {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL WHERE r.tenant_id=$1 AND r.project_node_id=$2 AND r.release_node_id=$3)`, p.TenantID, q.ProjectID, q.ShipsIn).Scan(&exists); err != nil {
			return out, err
		}
		if !exists {
			return out, errNotFound
		}

	}
	sortBy := q.Sort
	if sortBy == "" {
		sortBy = "updated"
		if q.Q != "" {
			sortBy = "relevance"
		}
	}
	identity := q
	identity.Cursor = ""
	identity.Limit = 0
	identity.Sort = sortBy
	hash := workquery.Fingerprint(struct {
		Principal tenant.Principal
		Query     listQuery
	}{p, identity})
	c := contextCursor{Hash: hash}
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return out, fail(400, "invalid_request", "invalid cursor")
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Hash != hash || !validUUID(c.ID) {
			return out, fail(400, "invalid_request", "cursor does not match this query")
		}
	}
	words := queryWords(q.Q)
	patterns := []string{}
	for _, word := range words {
		patterns = append(patterns, likePattern(word))
	}
	types := q.Types
	if types == nil {
		types = []string{}
	}
	statuses := []string{}
	for _, status := range q.Statuses {
		state, _ := stateFor(status)
		statuses = append(statuses, state)
	}
	prefix := `WITH terms AS (SELECT websearch_to_tsquery('german',$3)||websearch_to_tsquery('english',$3) q), context_entries AS NOT MATERIALIZED (
 SELECT n.id,k.slug kind,n.state,n.updated_at,n.created_at,lower(n.title) title,lower(coalesce(n.fields->>'slug','')) slug,
 CASE WHEN $3::text='' THEN 0 ELSE (CASE WHEN lower(coalesce(n.fields->>'slug',''))=lower($3) OR lower(n.key)=lower($3) THEN 100 ELSE 0 END)+(CASE WHEN n.title ILIKE $5 ESCAPE '\' THEN 40 ELSE 0 END)+(CASE WHEN NOT EXISTS(SELECT 1 FROM unnest($4::text[]) w WHERE NOT(n.title ILIKE w ESCAPE '\')) AND cardinality($4::text[])>0 THEN 25 ELSE 0 END)+(CASE WHEN NOT EXISTS(SELECT 1 FROM unnest($4::text[]) w WHERE NOT(coalesce(n.fields->>'slug','') ILIKE w ESCAPE '\')) AND cardinality($4::text[])>0 THEN 15 ELSE 0 END)+10*ts_rank_cd(n.search_document,t.q) END::float8 score
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id CROSS JOIN terms t
 WHERE n.tenant_id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug=ANY($6::text[])
 AND (cardinality($7::text[])=0 OR k.slug=ANY($7::text[])) AND (cardinality($8::text[])=0 OR n.state=ANY($8::text[]))
 AND ($3::text='' OR n.search_document@@t.q OR n.title ILIKE $5 ESCAPE '\' OR coalesce(n.fields->>'slug','') ILIKE $5 ESCAPE '\' OR (cardinality($4::text[])>0 AND NOT EXISTS(SELECT 1 FROM unnest($4::text[]) w WHERE NOT(n.title ILIKE w ESCAPE '\' OR coalesce(n.fields->>'slug','') ILIKE w ESCAPE '\' OR n.key ILIKE w ESCAPE '\' OR n.body ILIKE w ESCAPE '\'))))
 AND EXISTS(SELECT 1 FROM node_relations link JOIN nodes peer ON peer.tenant_id=link.tenant_id AND peer.id=CASE WHEN link.source_node_id=n.id THEN link.target_node_id ELSE link.source_node_id END AND peer.deleted_at IS NULL AND peer.project_id=$2 WHERE link.tenant_id=n.tenant_id AND link.type='relates' AND (link.source_node_id=n.id OR link.target_node_id=n.id) AND (( $9::uuid IS NOT NULL AND peer.id=$9::uuid) OR EXISTS(SELECT 1 FROM ` + delivery.Effective + ` e WHERE e.tenant_id=peer.tenant_id AND e.item_node_id=peer.id AND e.project_node_id=$2 AND e.kind='ticket' AND e.release_node_id IS NOT DISTINCT FROM $9::uuid))))`
	args := []any{p.TenantID, q.ProjectID, q.Q, patterns, likePattern(strings.TrimSpace(q.Q))[1:], kindSlugs(), types, statuses, nullableScopeID(strings.Replace(q.ShipsIn, "none", "", 1))}
	rows, err := tx.Query(ctx, prefix+` SELECT kind,state,count(*) FROM (SELECT kind,state FROM context_entries LIMIT 10001) bounded GROUP BY kind,state`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind, state string
		var count int
		if err = rows.Scan(&kind, &state, &count); err != nil {
			rows.Close()
			return out, err
		}
		out.Total += count
		spec, _ := specFor(kind)
		out.Counts["type"][spec.Type] += count
		out.Counts["status"][statusOf(state)] += count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	out.CountsIncomplete = out.Total > 10000
	out.Truncated = out.CountsIncomplete
	// Seek by normalized query order with immutable UUID tie-break; never offset or page fan-out.
	expr, cast, dir := "ce.updated_at", "timestamptz", "DESC"
	switch sortBy {
	case "created":
		expr = "ce.created_at"
	case "title":
		expr = "ce.title"
		cast = "text"
		dir = "ASC"
	case "slug":
		expr = "ce.slug"
		cast = "text"
		dir = "ASC"
	case "type":
		expr = "CASE ce.kind WHEN 'runbook' THEN 0 WHEN 'guideline' THEN 1 WHEN 'memory' THEN 2 WHEN 'external_system' THEN 3 ELSE 4 END"
		cast = "int"
		dir = "ASC"
	case "relevance":
		expr = "ce.score"
		cast = "float8"
	}
	op := "<"
	if dir == "ASC" {
		op = ">"
	}
	var value any
	if q.Cursor != "" {
		switch cast {
		case "timestamptz":
			v, err := time.Parse(time.RFC3339Nano, c.Value)
			if err != nil {
				return out, fail(400, "invalid_request", "invalid cursor")
			}
			value = v
		case "int":
			v, err := strconv.Atoi(c.Value)
			if err != nil {
				return out, fail(400, "invalid_request", "invalid cursor")
			}
			value = v
		case "float8":
			v, err := strconv.ParseFloat(c.Value, 64)
			if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
				return out, fail(400, "invalid_request", "invalid cursor")
			}
			value = v
		default:
			value = c.Value
		}
	}
	args = append(args, q.Cursor == "", value, c.Updated, nullableScopeID(c.ID), q.Limit+1, longest(words))
	orderExpr := expr + "::text"
	if cast == "timestamptz" {
		// Scan timestamps as times; PostgreSQL text depends on session formatting.
		orderExpr = expr
	}
	pageSQL := prefix + `, selected AS MATERIALIZED (SELECT ce.*,` + orderExpr + ` order_value FROM context_entries ce WHERE ($10::bool OR ` + expr + op + `$11::` + cast + ` OR (` + expr + `=$11::` + cast + ` AND (ce.updated_at,ce.id)<($12,$13::uuid))) ORDER BY ` + expr + ` ` + dir + `,ce.updated_at DESC,ce.id DESC LIMIT $14)
 SELECT ` + itemColumns + `,CASE WHEN $15::text<>'' AND strpos(lower(n.body),lower($15))>0 THEN substr(n.body,greatest(1,strpos(lower(n.body),lower($15))-300),900) ELSE left(n.body,900) END,$15::text<>'' AND strpos(lower(n.body),lower($15))>300,ce.order_value FROM selected ce JOIN nodes n ON n.tenant_id=$1 AND n.id=ce.id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id ` + nearestProject + "\n" + lastWrite + ` ORDER BY ` + expr + ` ` + dir + `,ce.updated_at DESC,ce.id DESC`
	rows, err = tx.Query(ctx, pageSQL, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var it Item
		var head, orderValue string
		var cut bool
		var orderTime time.Time
		var orderTarget any = &orderValue
		if cast == "timestamptz" {
			orderTarget = &orderTime
		}
		if err = scanItem(rows, &it, &head, &cut, orderTarget); err != nil {
			return out, err
		}
		if cast == "timestamptz" {
			orderValue = orderTime.UTC().Format(time.RFC3339Nano)
		}
		it.Excerpt = excerpt(head, it.Title, words, cut)
		out.Items = append(out.Items, it)
		values = append(values, orderValue)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > q.Limit {
		out.Items = out.Items[:q.Limit]
		last := out.Items[q.Limit-1]
		raw, _ := json.Marshal(contextCursor{Hash: hash, ID: last.ID, Value: values[q.Limit-1], Updated: last.UpdatedAt})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}
func nullableScopeID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (m *module) readScopedKnowledge(ctx context.Context, p tenant.Principal, q listQuery) (ListPage, error) {
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	var out ListPage
	err := db.ReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('statement_timeout','15s',true) `); err != nil {
			return err
		}
		var err error
		out, err = scopedKnowledge(ctx, tx, p, q)
		return err
	})
	return out, err
}
