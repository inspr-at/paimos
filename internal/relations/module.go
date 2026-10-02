// SPDX-License-Identifier: AGPL-3.0-only

package relations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/business/crm"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type Relation struct {
	ID           string    `json:"id"`
	SourceNodeID string    `json:"source_node_id"`
	TargetNodeID string    `json:"target_node_id"`
	Type         string    `json:"type"`
	CreatedAt    time.Time `json:"created_at"`
}

type module struct{ pool *pgxpool.Pool }

// New returns the /api/relations module. See the package doc for undo wiring.
func New(pool *pgxpool.Pool) httpapi.Module { return &module{pool: pool} }

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/relations", m.list)
	mux.HandleFunc("POST /api/relations", m.create)
	mux.HandleFunc("DELETE /api/relations/{relationId}", m.delete)
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		writeError(w, 401, "unauthorized", "authentication required")
		return p, false
	}
	return p, true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	httpapi.WriteJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}

func failure(w http.ResponseWriter, err error) {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, events.ErrNotFound):
		writeError(w, 404, "not_found", "relation or node not found")
	case errors.As(err, new(refusal)):
		writeError(w, 409, "conflict", err.Error())
	case errors.Is(err, errGraph):
		writeError(w, 409, "conflict", "relation kind or direction is not allowed")
	case errors.Is(err, errForbiddenRelation):
		writeError(w, 403, "forbidden", "linking and unlinking need the permission in both items' projects")
	case errors.Is(err, events.ErrForbidden):
		writeError(w, 403, "forbidden", "permission denied")
	case errors.Is(err, events.ErrConflict):
		writeError(w, 409, "conflict", "relation conflicts with current state")
	case errors.As(err, &pe) && (pe.Code == "23505" || pe.Code == "40001" || pe.Code == "40P01"):
		writeError(w, 409, "conflict", "relation already exists or changed concurrently")
	default:
		writeError(w, 500, "internal_error", "internal error")
	}
}

func uuid(s string) (string, bool) {
	var u pgtype.UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || u.Scan(s) != nil || !u.Valid {
		return "", false
	}
	return strings.ToLower(s), true
}

func validType(s string) bool {
	switch s {
	case "blocks", "relates", "implements", "cites", "duplicates", crm.CustomerOf, crm.ContactFor:
		return true
	}
	return false
}

func scanRelation(row pgx.Row) (Relation, error) {
	var v Relation
	err := row.Scan(&v.ID, &v.SourceNodeID, &v.TargetNodeID, &v.Type, &v.CreatedAt)
	return v, err
}

// Lock endpoints in UUID order to serialize with soft deletion and avoid
// deadlocks for opposite directed pairs. No cross-tenant node can pass RLS.
func lockNodes(ctx context.Context, tx pgx.Tx, p tenant.Principal, source, target string) error {
	rows, err := tx.Query(ctx, `SELECT id FROM nodes WHERE tenant_id=$1 AND id IN ($2,$3)
   AND deleted_at IS NULL ORDER BY id FOR UPDATE`, p.TenantID, source, target)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 2 {
		return events.ErrNotFound
	}
	return nil
}

// Endpoint locks cannot serialize disjoint edges closing the same cycle.
// Take the graph fence before any endpoint or relation row lock.
func lockGraph(ctx context.Context, tx pgx.Tx, tenantID, typ string) error {
	if !acyclic(typ) {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+":relation-graph:"+typ)
	return err
}

func (m *module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var input struct {
		Source string `json:"source_node_id"`
		Target string `json:"target_node_id"`
		Type   string `json:"type"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if decoder.Decode(&input) != nil {
		writeError(w, 400, "invalid_request", "invalid relation body")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_request", "expected one JSON object")
		return
	}
	source, sourceOK := uuid(input.Source)
	target, targetOK := uuid(input.Target)
	if !sourceOK || !targetOK || !validType(input.Type) {
		writeError(w, 400, "invalid_request", "invalid relation endpoints or type")
		return
	}
	if source == target {
		writeError(w, 400, "invalid_request", "an item cannot be linked to itself")
		return
	}
	if input.Type == "relates" && source > target {
		source, target = target, source
	}
	var result Relation
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockGraph(r.Context(), tx, p.TenantID, input.Type); err != nil {
			return err
		}
		if err := lockNodes(r.Context(), tx, p, source, target); err != nil {
			return err
		}
		// Linking writes into both ends' projects (ADR-003 P2).
		if err := requireOnEnds(r.Context(), tx, p, "relations.write", source, target); err != nil {
			return err
		}
		if err := enforceGraph(r.Context(), tx, p.TenantID, source, target, input.Type); err != nil {
			return err
		}
		if err := refuse(r.Context(), tx, p.TenantID, source, target, input.Type); err != nil {
			return err
		}
		var err error
		result, err = scanRelation(tx.QueryRow(r.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type)
    VALUES ($1,$2,$3,$4) RETURNING id::text,source_node_id::text,target_node_id::text,type,created_at`, p.TenantID, source, target, input.Type))
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: &result.SourceNodeID, Type: "relation.created", After: result})
		return err
	})
	if err != nil {
		failure(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, result)
}

func (m *module) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := uuid(r.PathValue("relationId"))
	if !ok {
		writeError(w, 404, "not_found", "relation not found")
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var typ string
		if err := tx.QueryRow(r.Context(), `SELECT type FROM node_relations WHERE tenant_id=$1 AND id=$2`, p.TenantID, id).Scan(&typ); err != nil {
			return err
		}
		if err := lockGraph(r.Context(), tx, p.TenantID, typ); err != nil {
			return err
		}
		// Unlinking changes both ends, so it needs relations.delete in both
		// items' projects, as linking needs relations.write (ADR-003 P2).
		var source, target string
		if err := tx.QueryRow(r.Context(), `SELECT source_node_id::text,target_node_id::text FROM node_relations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, id).Scan(&source, &target); err != nil {
			return err
		}
		if err := requireOnEnds(r.Context(), tx, p, "relations.delete", source, target); err != nil {
			return err
		}
		result, err := scanRelation(tx.QueryRow(r.Context(), `DELETE FROM node_relations WHERE tenant_id=$1 AND id=$2
    RETURNING id::text,source_node_id::text,target_node_id::text,type,created_at`, p.TenantID, id))
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: &result.SourceNodeID, Type: "relation.deleted", Before: result})
		return err
	})
	if err != nil {
		failure(w, err)
		return
	}
	w.WriteHeader(204)
}

type cursor struct {
	Version int    `json:"v"`
	Tenant  string `json:"tenant"`
	Node    string `json:"node"`
	Last    string `json:"last"`
	Order   string `json:"order"`
}

type page struct {
	Items      []Relation `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

func (m *module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	node, ok := uuid(q.Get("node_id"))
	limit := 50
	var err error
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
	}
	if !ok || err != nil || limit < 1 || limit > 200 {
		writeError(w, 400, "invalid_request", "invalid node_id or limit")
		return
	}
	var after any
	if q.Has("cursor") {
		raw := q.Get("cursor")
		if len(raw) > 2048 {
			writeError(w, 400, "invalid_cursor", "invalid cursor length")
			return
		}
		var c cursor
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Tenant != p.TenantID || c.Node != node || c.Order != "id.asc" {
			writeError(w, 400, "invalid_cursor", "cursor does not match query")
			return
		}
		id, ok := uuid(c.Last)
		if !ok {
			writeError(w, 400, "invalid_cursor", "invalid cursor position")
			return
		}
		after = id
	}
	result := page{Items: make([]Relation, 0)}
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id::text,source_node_id::text,target_node_id::text,type,created_at
   FROM node_relations WHERE tenant_id=$1 AND (source_node_id=$2 OR target_node_id=$2)
    AND ($3::uuid IS NULL OR id>$3) ORDER BY id LIMIT $4`, p.TenantID, node, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanRelation(rows)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		failure(w, err)
		return
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		b, _ := json.Marshal(cursor{Version: 1, Tenant: p.TenantID, Node: node, Last: result.Items[limit-1].ID, Order: "id.asc"})
		next := base64.RawURLEncoding.EncodeToString(b)
		result.NextCursor = &next
	}
	httpapi.WriteJSON(w, 200, result)
}

var errForbiddenRelation = errors.New("relation not permitted")

func requireOnEnds(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission string, ids ...string) error {
	for _, id := range ids {
		var project *string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1::uuid`, id).Scan(&project); err != nil {
			return err
		}
		scope := authz.Scope{}
		if project != nil {
			scope.ProjectID = *project
		}
		if authz.RequireTx(ctx, tx, p, permission, scope) != nil {
			return errForbiddenRelation
		}
	}
	return nil
}
