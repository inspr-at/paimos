// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

type AuditPage struct {
	Items []MergeAudit `json:"items"`
	Next  *string      `json:"next_cursor"`
}

func (m *Module) auditList(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	key := q.Get("project")
	rawCursor := q.Get("after")
	flag := q.Get("flagged")
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			respondError(w, fail(400, "invalid audit limit"))
			return
		}
		limit = n
	}
	cursorRepo, cursorSHA := "", ""
	if rawCursor != "" {
		if len(rawCursor) > 768 {
			respondError(w, fail(400, "invalid audit cursor"))
			return
		}
		b, err := base64.RawURLEncoding.DecodeString(rawCursor)
		var valid bool
		cursorRepo, cursorSHA, valid = strings.Cut(string(b), "\x00")
		if err != nil || !valid || !reviewgate.ValidRepository(cursorRepo) || !reviewgate.ValidSHA(cursorSHA) {
			respondError(w, fail(400, "invalid audit cursor"))
			return
		}
	}
	if len(key) > 64 || flag != "" && flag != "true" && flag != "false" {
		respondError(w, fail(400, "invalid audit filter"))
		return
	}
	out := AuditPage{Items: []MergeAudit{}}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var project *string
		if key != "" {
			rows, err := tx.Query(r.Context(), `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE k.slug='project' AND n.deleted_at IS NULL AND (n.key=$1 OR coalesce(nullif(n.fields->>'project_key',''),nullif(n.fields->'classic'->>'key',''),split_part(n.key,'-',1))=$1) ORDER BY n.id LIMIT 2`, key)
			if err != nil {
				return err
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			rows.Close()
			if rows.Err() != nil {
				return rows.Err()
			}
			if len(ids) == 0 {
				return pgx.ErrNoRows
			}
			if len(ids) > 1 {
				return fail(400, "ambiguous project key")
			}
			project = &ids[0]
		}
		s := scope(project)
		if project == nil {
			s.AnyProject = true
		}
		if err := authz.RequireTx(r.Context(), tx, p, "delivery.read", s); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT `+auditColumns+` FROM delivery_merge_audit WHERE ($1::uuid IS NULL OR project_id=$1) AND ($2='' OR (cardinality(flags)>0)=($2='true')) AND (repository,merge_sha)>($3,$4) ORDER BY repository,merge_sha LIMIT $5`, project, flag, cursorRepo, cursorSHA, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAudit(rows)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, a)
		}
		if rows.Err() != nil {
			return rows.Err()
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			a := out.Items[limit-1]
			next := base64.RawURLEncoding.EncodeToString([]byte(a.Repository + "\x00" + a.SHA))
			out.Next = &next
		}
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
