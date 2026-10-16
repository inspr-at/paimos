// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func boundedGoalQuery(r *http.Request) error {
	if len(r.URL.RawQuery) > 8192 {
		return invalid("query", "exceeds 8192 bytes")
	}
	for _, values := range r.URL.Query() {
		if len(values) != 1 {
			return invalid("query", "repeated parameter")
		}
	}
	return nil
}

func (m *module) readSnapshot(r *http.Request, p tenant.Principal, fn func(context.Context, pgx.Tx, Watermark, time.Time) error) error {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Share the access/tree fences with other reads; mutations cannot change
		// membership or grants midway through a response. Allocate generation at
		// snapshot construction, before any potentially slow expansion.
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR SHARE`, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1::text,0))`, p.TenantID); err != nil {
			return err
		}
		var creator any
		if p.KeyCreatorID != "" {
			creator = p.KeyCreatorID
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, p.TenantID, p.ID, creator); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		now := m.now()
		w, err := watermark(ctx, tx)
		if err != nil {
			return err
		}
		return fn(ctx, tx, w, now)
	})
}
func (m *module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	if !nodeIdentity(r.PathValue("goalId")) {
		respond(w, nil, invalid("goalId", "must be a goal key or UUID"))
		return
	}
	var out GoalView
	err := m.readSnapshot(r, p, func(ctx context.Context, tx pgx.Tx, w Watermark, now time.Time) error {
		n, err := resolveNode(ctx, tx, r.PathValue("goalId"), false)
		if err != nil {
			return err
		}
		out, err = readGoal(ctx, tx, p, n, now, w)
		return err
	})
	respond(w, out, err)
}
func positiveLimit(r *http.Request, max, defaultValue int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultValue, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		return 0, invalid("limit", "outside allowed page size")
	}
	return n, nil
}

type HistoryResult struct {
	Items      []events.Event `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Watermark
}

func (m *module) history(w http.ResponseWriter, r *http.Request) {
	if err := boundedGoalQuery(r); err != nil {
		respond(w, nil, err)
		return
	}
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	limit, err := positiveLimit(r, 50, 20)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if !nodeIdentity(r.PathValue("goalId")) {
		respond(w, nil, invalid("goalId", "must be a goal key or UUID"))
		return
	}
	outcome := r.URL.Query().Get("outcome")
	if outcome != "" && !uuidPattern.MatchString(outcome) {
		respond(w, nil, invalid("outcome", "must be an outcome UUID"))
		return
	}
	before := int64(0)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			respond(w, nil, invalid("cursor", "must be an event id"))
			return
		}
	}
	out := HistoryResult{Items: []events.Event{}}
	err = m.readSnapshot(r, p, func(ctx context.Context, tx pgx.Tx, w Watermark, _ time.Time) error {
		goal, err := resolveNode(ctx, tx, r.PathValue("goalId"), false)
		if err != nil {
			return err
		}
		if !IsGoalKind(goal.Slug) {
			return pgx.ErrNoRows
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: goal.Project}); err != nil {
			return err
		}
		id := goal.ID
		if outcome != "" {
			node, err := resolveNode(ctx, tx, outcome, false)
			if err != nil {
				return err
			}
			if node.Slug != OutcomeKind {
				return pgx.ErrNoRows
			}
			if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: node.Project}); err != nil {
				return err
			}
			id = node.ID
		}
		rows, err := tx.Query(ctx, `SELECT id,actor_principal_id::text,node_id::text,type,before,after,at,undo_of FROM events WHERE node_id=$1 AND type LIKE 'goal.%' AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, id, before, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event events.Event
			if err := rows.Scan(&event.ID, &event.ActorPrincipalID, &event.NodeID, &event.Type, &event.Before, &event.After, &event.At, &event.UndoOf); err != nil {
				return err
			}
			out.Items = append(out.Items, event)
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			out.NextCursor = strconv.FormatInt(out.Items[limit-1].ID, 10)
		}
		out.Watermark = w
		return rows.Err()
	})
	respond(w, out, err)
}

type goalCursor struct {
	Key   string `json:"key"`
	ID    string `json:"id"`
	Query string `json:"query"`
	Null  bool   `json:"null"`
}

func encodeGoalCursor(c goalCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeGoalCursor(raw string) (goalCursor, error) {
	var c goalCursor
	if len(raw) > 2048 {
		return c, invalid("cursor", "too long")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err == nil {
		err = decodeStrict(b, &c)
	}
	if err != nil || !uuidPattern.MatchString(c.ID) {
		return c, invalid("cursor", "invalid cursor")
	}
	return c, nil
}
func keyForGoal(g GoalView, sortBy string) (string, bool) {
	switch sortBy {
	case "eta":
		if g.ETA == nil || g.ETA.At == nil {
			return "", true
		}
		return g.ETA.At.UTC().Format("2006-01-02T15:04:05.000000000Z"), false
	case "name":
		return strings.ToLower(g.Title), false
	case "project":
		if len(g.Projects) == 0 {
			return "", true
		}
		return strings.ToLower(g.Projects[0].Key), false
	case "progress":
		if g.Progress == nil {
			return "", true
		}
		return leftPad(g.Progress.Pct), false
	default:
		if g.States == nil {
			return "", true
		}
		n := g.States.Moving
		switch sortBy {
		case "queued":
			n = g.States.Queued
		case "done":
			n = g.States.Done
		}
		return leftPad(n), false
	}
}
func leftPad(n int) string { return strconv.Itoa(1000000 + n) }
