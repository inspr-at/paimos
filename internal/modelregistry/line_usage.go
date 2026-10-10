// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

type lineUse struct {
	Column      string     `json:"column"`
	Layer       string     `json:"layer"`
	Person      *string    `json:"person,omitempty"`
	Replacement simplePick `json:"replacement"`
	project     string
}
type lineUsageDocument struct {
	UsedBy       []lineUse  `json:"used_by"`
	Replacement  simplePick `json:"replacement"`
	Revision     string     `json:"revision"`
	Incomplete   bool       `json:"incomplete"`
	LineProfiles []Profile  `json:"line_profiles"`
}

func (m *Module) lineUsage(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	h, model, err := linePath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := lineUsageDocument{UsedBy: []lineUse{}}
	err = m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		current, err := currentModelReader(r, tx, p)
		if err != nil {
			return err
		}
		person, err := currentPreferencePerson(ctx, tx, current)
		if err != nil {
			return err
		}
		manager := authz.RequireTx(ctx, tx, current, "model_prefs.manage", authz.Scope{}) == nil
		c, err := loadBoardCatalog(ctx, tx)
		if err != nil {
			return err
		}
		ps := lineProfiles(c, h, model)
		if len(ps) == 0 {
			return fail(404, "model line not found")
		}
		line := boardLineID(ps[0])
		excluded := []string{}
		for _, p := range ps {
			excluded = append(excluded, p.ID)
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT column_key,layer,person_id::text,project_id::text FROM (
   SELECT o.column_key,CASE WHEN p.scope='workspace' THEN 'default' ELSE 'person' END AS layer,p.person_id,NULL::uuid AS project_id
   FROM model_pref_orders o JOIN model_pref_profiles p ON p.tenant_id=o.tenant_id AND p.id=o.profile_id WHERE $1=ANY(o.rank)
   UNION ALL SELECT column_key,scope,NULL::uuid,project_id FROM model_rules WHERE line=$1
  ) used ORDER BY column_key,layer,person_id,project_id LIMIT 257`, line)
		if err != nil {
			return err
		}
		uses := []lineUse{}
		for rows.Next() {
			var u lineUse
			var project *string
			if err := rows.Scan(&u.Column, &u.Layer, &u.Person, &project); err != nil {
				rows.Close()
				return err
			}
			if project != nil {
				u.project = *project
			}
			uses = append(uses, u)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(uses) > 256 {
			return prefFail(413, "line_usage_limit")
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		for _, u := range uses {
			if !manager && (u.Person != nil && (person == nil || *u.Person != *person) || u.project != "") {
				out.Incomplete = true
				continue
			}
			if u.project != "" && authz.RequireTx(ctx, tx, current, "nodes.read", authz.Scope{ProjectID: u.project}) != nil {
				out.Incomplete = true
				continue
			}
			q := WorkQuery{Column: u.Column, Area: u.Column, Role: "build", Situation: "first", PersonID: u.Person, ProjectID: u.project, WorkspaceOnly: u.Layer == "default" || u.Layer == "workspace", ExplicitBoard: true, Concept: u.Column == "concept"}
			if strings.HasPrefix(u.Column, "review:") {
				q.Role = "review-gate"
				q.AuthorFamily = strings.TrimPrefix(u.Column, "review:")
			}
			resolved, err := resolveBoardWork(ctx, tx, current, q, now, excluded)
			if err != nil {
				return err
			}
			if resolved != nil {
				u.Replacement = pickProfile(resolved.Profile)
			}
			if len(out.UsedBy) == 0 {
				out.Replacement = u.Replacement
			}
			out.UsedBy = append(out.UsedBy, u)
		}
		// The profiles and the revision are one snapshot. A later list must not
		// be paired with this revision, and this revision must not be paired
		// with the list the page already held.
		out.LineProfiles = ps
		out.Revision, err = registryRevision(ctx, tx)
		return err
	})
	if err != nil {
		writeBoardError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
