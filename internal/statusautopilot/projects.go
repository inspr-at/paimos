// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

type projectSetting struct {
	ID       string   `json:"id"`
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Override Override `json:"override"`
}
type projectSettingsPage struct {
	Items     []projectSetting `json:"items"`
	Next      *string          `json:"next_cursor"`
	Inherited int              `json:"inherited_count"`
}

func (m *Module) projects(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	mode, after, search := q.Get("mode"), q.Get("after"), strings.TrimSpace(q.Get("q"))
	if mode == "" {
		mode = "overrides"
	}
	if mode != "overrides" && mode != "inherit" || after != "" && !uuid.MatchString(after) || len(search) > 200 {
		httpapi.WriteError(w, 400, "invalid project filter or cursor")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := projectSettingsPage{Items: []projectSetting{}}
	err := db.InTenant(db.WithReadStatementTimeout(ctx, 8*time.Second), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		s, err := Load(ctx, tx)
		if err != nil {
			return err
		}
		const from = ` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 LEFT JOIN status_autopilot_projects o ON o.tenant_id=n.tenant_id AND o.project_id=n.id
 WHERE k.slug='project' AND n.deleted_at IS NULL`
		if err = tx.QueryRow(ctx, `SELECT count(*)`+from+` AND coalesce(o.mode,'inherit')='inherit'`).Scan(&out.Inherited); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT n.id::text,n.key,n.title,coalesce(o.mode,'inherit'),coalesce(o.revision,0)`+from+`
 AND ((coalesce(o.mode,'inherit')='inherit')=($1='inherit'))
 AND ($2='' OR n.id>nullif($2,'')::uuid)
 AND ($3='' OR strpos(lower(n.key||' '||n.title),lower($3))>0)
 ORDER BY n.id LIMIT 51`, mode, after, search)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item projectSetting
			if err = rows.Scan(&item.ID, &item.Key, &item.Title, &item.Override.Mode, &item.Override.Revision); err != nil {
				return err
			}
			item.Override.Effective = item.Override.Mode == "on" || item.Override.Mode == "inherit" && s.Enabled
			out.Items = append(out.Items, item)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > 50 {
			out.Items = out.Items[:50]
			last := out.Items[49].ID
			out.Next = &last
		}
		return nil
	})
	respond(w, out, err)
}
