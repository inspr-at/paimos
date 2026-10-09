// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Page struct {
	Items      []ApprovalRequest `json:"items"`
	HasMore    bool              `json:"has_more"`
	NextCursor string            `json:"next_cursor,omitempty"`
}
type pageCursor struct {
	At     time.Time `json:"at"`
	ID     string    `json:"id"`
	State  string    `json:"state"`
	Tenant string    `json:"tenant"`
	Person string    `json:"person"`
}

func (m *Module) visibility(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]byte, error) {
	check, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	permissions := map[string][]string{}
	for _, target := range m.targets {
		permissions[target.Permission()] = []string{}
	}
	if p.Kind == tenant.Agent {
		permissions = map[string][]string{"approvals.request": {}}
	}
	for permission := range permissions {
		if check(permission, "") {
			permissions[permission] = append(permissions[permission], "")
		}
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='project' AND n.deleted_at IS NULL ORDER BY n.id LIMIT 1001`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		for permission := range permissions {
			if check(permission, id) {
				permissions[permission] = append(permissions[permission], id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if count > 1000 {
		return nil, fault(422, "step-up coverage exceeds 1000 projects")
	}
	return json.Marshal(permissions)
}
func (m *Module) List(ctx context.Context, p tenant.Principal, state string, limit int, rawCursor string) (Page, error) {
	out := Page{Items: []ApprovalRequest{}}
	var c pageCursor
	if limit < 1 || limit > 100 || state != "pending" && state != "decided" || len(rawCursor) > 512 {
		return out, fault(400, "invalid page")
	}
	if rawCursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(rawCursor)
		if err != nil || json.Unmarshal(raw, &c) != nil || !ValidID(c.ID) || c.At.IsZero() || c.State != state || c.Tenant != p.TenantID || c.Person != p.ID {
			return out, fault(400, "invalid cursor")
		}
	}
	err := m.transaction(ctx, p, func(tx pgx.Tx) error {
		if p.Kind == tenant.Agent {
			if err := authz.RequireTx(ctx, tx, p, "approvals.request", authz.Scope{AnyProject: true}); err != nil {
				return err
			}
		} else if p.Kind != tenant.Person || p.KeyCreatorID != "" {
			return authz.ErrForbidden
		}
		visibility, err := m.visibility(ctx, tx, p)
		if err != nil {
			return err
		}
		now := m.now().UTC()
		// Lock the complete bounded page before expiry events acquire the counter.
		query := `SELECT ` + requestColumns + ` FROM stepup_requests WHERE
   ($1::boolean AND requested_by=$2 OR NOT $1::boolean)
   AND coalesce($3::jsonb->(CASE WHEN $1::boolean THEN 'approvals.request' ELSE permission END),'[]'::jsonb) @> jsonb_build_array(coalesce(project_id::text,''))
   AND (CASE WHEN $4='pending' THEN state='pending' AND expires_at>$5 ELSE state<>'pending' OR expires_at<=$5 END)
   AND (NOT $6 OR CASE WHEN $4='pending' THEN (created_at,id)>($7::timestamptz,$8::uuid) ELSE (coalesce(decided_at,expires_at),id)<($7::timestamptz,$8::uuid) END)
   ORDER BY CASE WHEN $4='pending' THEN created_at END ASC,CASE WHEN $4='decided' THEN coalesce(decided_at,expires_at) END DESC,
   CASE WHEN $4='pending' THEN id END ASC,CASE WHEN $4='decided' THEN id END DESC LIMIT $9 FOR NO KEY UPDATE`
		rows, err := tx.Query(ctx, query, p.Kind == tenant.Agent, p.ID, visibility, state, now, rawCursor != "", c.At, nullable(c.ID), limit+1)
		if err != nil {
			return err
		}
		var candidates []ApprovalRequest
		for rows.Next() {
			var r ApprovalRequest
			if err := scan(rows, &r); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		out.HasMore = len(candidates) > limit
		if out.HasMore {
			candidates = candidates[:limit]
		}
		for i := range candidates {
			if err := decorate(ctx, tx, &candidates[i]); err != nil {
				return err
			}
		}
		if out.HasMore {
			r := candidates[len(candidates)-1]
			at := r.CreatedAt
			if state == "decided" {
				at = r.ExpiresAt
				if r.DecidedAt != nil {
					at = *r.DecidedAt
				}
			}
			raw, err := json.Marshal(pageCursor{at, r.ID, state, p.TenantID, p.ID})
			if err != nil {
				return err
			}
			out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		for i := range candidates {
			if err := m.expire(ctx, tx, p, &candidates[i]); err != nil {
				return err
			}
		}
		out.Items = candidates
		return nil
	})
	return out, err
}
