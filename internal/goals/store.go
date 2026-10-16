// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type storedNode struct {
	ID       string         `json:"outcome_node_id"`
	Key      string         `json:"key"`
	Title    string         `json:"label"`
	Parent   string         `json:"goal_node_id"`
	Project  string         `json:"-"`
	Slug     string         `json:"-"`
	Position string         `json:"position"`
	Fields   map[string]any `json:"fields"`
	Targets  []string       `json:"ticket_node_ids"`
	Deleted  bool           `json:"deleted"`
}

const nodeColumns = `n.id::text,n.key,n.title,coalesce(n.parent_id::text,''),coalesce(n.project_id::text,''),k.slug,n.position::text,n.fields,n.deleted_at IS NOT NULL`

func scanNode(row pgx.Row) (storedNode, error) {
	n := storedNode{Targets: []string{}}
	var raw []byte
	err := row.Scan(&n.ID, &n.Key, &n.Title, &n.Parent, &n.Project, &n.Slug, &n.Position, &raw, &n.Deleted)
	if strings.Contains(n.Position, ".") {
		n.Position = strings.TrimRight(strings.TrimRight(n.Position, "0"), ".")
	}
	if err == nil {
		err = json.Unmarshal(raw, &n.Fields)
	}
	return n, err
}
func resolveNode(ctx context.Context, tx pgx.Tx, identity string, lock bool) (storedNode, error) {
	query := `SELECT ` + nodeColumns + ` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE (n.id::text=lower($1) OR n.key=$1) AND n.deleted_at IS NULL`
	if lock {
		query += ` FOR NO KEY UPDATE OF n`
	}
	return scanNode(tx.QueryRow(ctx, query, identity))
}
func lockWrite(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
		return err
	}
	var creator any
	if p.KeyCreatorID != "" {
		creator = p.KeyCreatorID
	}
	if _, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, p.TenantID, p.ID, creator); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`)
	return err
}
func authorizeGoal(ctx context.Context, tx pgx.Tx, p tenant.Principal, n storedNode) error {
	if n.Deleted || !IsGoalKind(n.Slug) {
		return pgx.ErrNoRows
	}
	return authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: n.Project})
}

type receipt struct {
	Method, Target string
	Hash           []byte
	Response       []byte
}

func loadReceipt(ctx context.Context, tx pgx.Tx, p tenant.Principal, key string) (*receipt, error) {
	var r receipt
	err := tx.QueryRow(ctx, `SELECT method,target_node_id::text,request_sha256,response FROM goal_update_receipts WHERE tenant_id=$1 AND actor_principal_id=$2 AND idempotency_key=$3`, p.TenantID, p.ID, key).Scan(&r.Method, &r.Target, &r.Hash, &r.Response)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}
func replay(r *receipt, method, target string, hash [32]byte) (WriteResult, error) {
	var out WriteResult
	if r.Method != method || r.Target != target || !slices.Equal(r.Hash, hash[:]) {
		return out, fail(409, "Idempotency-Key: method, target or body conflicts")
	}
	err := json.Unmarshal(r.Response, &out)
	return out, err
}
func watermark(ctx context.Context, tx pgx.Tx) (Watermark, error) {
	var w Watermark
	err := tx.QueryRow(ctx, `SELECT coalesce(max(id),0),nextval('goal_response_seq') FROM events`).Scan(&w.ReadThrough, &w.Generation)
	return w, err
}
func finishWrite(ctx context.Context, tx pgx.Tx, p tenant.Principal, key, method string, n storedNode, hash [32]byte, pending []events.Change) (WriteResult, error) {
	out := WriteResult{ID: n.ID, Key: n.Key, Changed: len(pending) > 0}
	for _, change := range pending {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return out, err
		}
	}
	var err error
	out.Watermark, err = watermark(ctx, tx)
	if err != nil {
		return out, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO goal_update_receipts(tenant_id,actor_principal_id,idempotency_key,method,target_node_id,request_sha256,response,event_id) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8)`, p.TenantID, p.ID, key, method, n.ID, hash[:], raw, out.ReadThrough)
	return out, err
}
func createTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, key string, in createInput) (WriteResult, error) {
	var out WriteResult
	if err := lockWrite(ctx, tx, p); err != nil {
		return out, err
	}
	project := ""
	if in.ProjectID != nil {
		project = strings.ToLower(*in.ProjectID)
	}
	if project != "" {
		n, err := resolveNode(ctx, tx, project, true)
		if err != nil {
			return out, err
		}
		if n.Slug != "project" {
			return out, pgx.ErrNoRows
		}
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: project}); err != nil {
		return out, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	hash := sha256.Sum256(raw)
	r, err := loadReceipt(ctx, tx, p, key)
	if err != nil {
		return out, err
	}
	if r != nil {
		n, err := resolveNode(ctx, tx, r.Target, true)
		if err != nil {
			return out, err
		}
		if err = authorizeGoal(ctx, tx, p, n); err != nil {
			return out, err
		}
		return replay(r, http.MethodPost, n.ID, hash)
	}
	kinds, changes, err := ensureKinds(ctx, tx, p.TenantID)
	if err != nil {
		return out, err
	}
	n, err := insertNode(ctx, tx, p, kinds[GoalKind], in.Title, project, map[string]any{}, "0")
	if err != nil {
		return out, err
	}
	pending := []events.Change{}
	for _, c := range changes {
		typ := "kind.created"
		if c.Before != nil {
			typ = "kind.updated"
		}
		pending = append(pending, events.Change{Type: typ, Before: c.Before, After: c.After})
	}
	pending = append(pending, events.Change{NodeID: &n.ID, Type: "goal.created", After: map[string]any{"id": n.ID, "key": n.Key, "title": n.Title}})
	return finishWrite(ctx, tx, p, key, http.MethodPost, n, hash, pending)
}
func insertNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind kindSnapshot, title, parent string, fields map[string]any, position string) (storedNode, error) {
	var key string
	if err := tx.QueryRow(ctx, `SELECT aeon_next_node_key($1::uuid,$2)`, p.TenantID, kind.Prefix).Scan(&key); err != nil {
		return storedNode{}, err
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return storedNode{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields,position) VALUES($1,$2,$3,$4,nullif($5,'')::uuid,$6::jsonb,$7::numeric) RETURNING id::text`, p.TenantID, key, kind.ID, title, parent, raw, position).Scan(&id)
	if err != nil {
		return storedNode{}, err
	}
	return resolveNode(ctx, tx, id, false)
}
func saveNode(ctx context.Context, tx pgx.Tx, n storedNode) error {
	raw, err := json.Marshal(n.Fields)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE nodes SET title=$2,parent_id=nullif($3,'')::uuid,fields=$4::jsonb,position=$5::numeric,deleted_at=CASE WHEN $6 THEN clock_timestamp() ELSE NULL END,updated_at=clock_timestamp() WHERE id=$1`, n.ID, n.Title, n.Parent, raw, n.Position, n.Deleted)
	if err == nil && tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return err
}
func cloneNode(n storedNode) storedNode {
	b, _ := json.Marshal(n.Fields)
	n.Fields = nil
	json.Unmarshal(b, &n.Fields)
	n.Targets = slices.Clone(n.Targets)
	return n
}
func fieldString(n storedNode, key string) string { s, _ := n.Fields[key].(string); return s }
func setField[T any](fields map[string]any, key string, f Field[T]) {
	if !f.Set {
		return
	}
	if f.Value == nil {
		delete(fields, key)
	} else {
		raw, _ := json.Marshal(*f.Value)
		var v any
		json.Unmarshal(raw, &v)
		fields[key] = v
	}
}
func setETA(n *storedNode, f Field[ETAInput], p tenant.Principal, now time.Time) {
	if !f.Set {
		return
	}
	if f.Value == nil {
		for _, key := range []string{"eta_text", "eta_at", "eta_set_by", "eta_set_at"} {
			delete(n.Fields, key)
		}
		return
	}
	// Repeating a supplied value is a domain no-op; attribution changes only
	// when the actual authored text/time changes.
	at := ""
	if f.Value.At != nil {
		at = f.Value.At.UTC().Format(time.RFC3339Nano)
	}
	if fieldString(*n, "eta_text") == f.Value.Text && fieldString(*n, "eta_at") == at {
		return
	}
	delete(n.Fields, "eta_text")
	delete(n.Fields, "eta_at")
	if f.Value.Text != "" {
		n.Fields["eta_text"] = f.Value.Text
	}
	if at != "" {
		n.Fields["eta_at"] = at
	}
	n.Fields["eta_set_by"] = p.ID
	n.Fields["eta_set_at"] = now.UTC().Format(time.RFC3339Nano)
}
func etaSnapshot(n storedNode) map[string]any {
	out := map[string]any{"source": "agent"}
	for _, pair := range [][2]string{{"eta_text", "text"}, {"eta_at", "at"}, {"eta_set_by", "by"}} {
		if v, ok := n.Fields[pair[0]]; ok {
			out[pair[1]] = v
		}
	}
	return out
}
func addChanges(pending *[]events.Change, before *storedNode, after storedNode) {
	var b any
	if before != nil {
		b = *before
	}
	*pending = append(*pending, events.Change{NodeID: &after.ID, Type: "goal.items_changed", Before: b, After: after})
	if before == nil || !reflect.DeepEqual(etaSnapshot(*before), etaSnapshot(after)) {
		var prior any
		if before != nil {
			prior = etaSnapshot(*before)
		}
		if prior != nil || len(etaSnapshot(after)) > 1 {
			*pending = append(*pending, events.Change{NodeID: &after.ID, Type: "goal.eta_changed", Before: prior, After: etaSnapshot(after)})
		}
	}
}
