// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// removalSetLimit bounds the profiles one preview may remove together: every
// level of every route of one version, with room for Cursor's per-level ids.
const removalSetLimit = 64

type lineUse struct {
	Column string `json:"column"`
	Layer  string `json:"layer"`
	// Situation is the order's own situation (first, fix, stuck, small or
	// review); a rule applies to its whole row and has none.
	Situation   string     `json:"situation,omitempty"`
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

// lineUsage previews removing a line, or with profile parameters the whole set
// the caller removes in one step (AEON-1163): every route of one version. Each
// use's fallback excludes the entire set, so a sibling route that goes in the
// same step is never offered as what takes over.
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
	asked := r.URL.Query()["profile"]
	if len(asked) > removalSetLimit {
		writeBoardError(w, prefFail(413, "removal_set_limit"))
		return
	}
	for _, id := range asked {
		if !uuidRE.MatchString(id) {
			writeBoardError(w, prefFail(400, "invalid_profile"))
			return
		}
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
		set, err := removalSet(c, ps, asked)
		if err != nil {
			return err
		}
		excluded := []string{}
		for _, p := range set {
			excluded = append(excluded, p.ID)
		}
		uses, err := removalUses(ctx, tx, usageLines(c, set))
		if err != nil {
			return err
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
			if u.Replacement, err = removalFallback(ctx, tx, current, u, now, excluded); err != nil {
				return err
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

// removalSet is the line's active profiles and every profile the caller named.
// A named profile that is gone or retired means the page shows a version that
// has changed, so the preview refuses rather than describe another removal.
func removalSet(c boardCatalog, line []Profile, asked []string) ([]Profile, error) {
	set := slices.Clone(line)
	for _, id := range asked {
		if slices.ContainsFunc(set, func(p Profile) bool { return p.ID == id }) {
			continue
		}
		found := false
		for _, ps := range c.profiles {
			if i := slices.IndexFunc(ps, func(p Profile) bool { return p.ID == id && !p.Retired }); i >= 0 {
				set, found = append(set, ps[i]), true
				break
			}
		}
		if !found {
			return nil, prefFail(409, "stale_removal_set")
		}
	}
	return set, nil
}

// usageLines are the ids a stored reference names the set by: each profile's
// Models identity and its legacy board id. A line keeps running its newest
// enabled version, so while a newer one stays, that line is no use of the set.
func usageLines(c boardCatalog, set []Profile) []string {
	removed := map[string]bool{}
	for _, p := range set {
		removed[p.ID] = true
	}
	newest := map[string]string{}
	for _, ps := range c.profiles {
		for _, p := range ps {
			line, version := modelsLine(p)
			if p.Retired || !p.Enabled || removed[p.ID] || version == "alias" {
				continue
			}
			if CompareModelVersions(version, newest[line]) > 0 {
				newest[line] = version
			}
		}
	}
	lines := []string{}
	for _, p := range set {
		line, version := modelsLine(p)
		if version != "alias" && CompareModelVersions(newest[line], version) > 0 {
			continue
		}
		for _, id := range []string{line, boardLineID(p)} {
			if !slices.Contains(lines, id) {
				lines = append(lines, id)
			}
		}
	}
	slices.Sort(lines)
	return lines
}

// removalUses reads current storage: orders of every situation, including the
// small and review situations stored apart, rules not yet moved to Models, and
// the row constraints they moved to. Never entries are not uses.
func removalUses(ctx context.Context, tx pgx.Tx, lines []string) ([]lineUse, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT column_key,situation,layer,person_id::text,project_id::text FROM (
   SELECT o.column_key,o.situation,CASE WHEN p.scope='workspace' THEN 'default' ELSE 'person' END AS layer,p.person_id,NULL::uuid AS project_id
   FROM (SELECT tenant_id,profile_id,column_key,situation,rank FROM model_pref_orders
    UNION ALL SELECT tenant_id,profile_id,column_key,situation,rank FROM model_pref_situation_orders) o
   JOIN model_pref_profiles p ON p.tenant_id=o.tenant_id AND p.id=o.profile_id WHERE o.rank && $1::text[]
   UNION ALL SELECT column_key,'',scope,NULL::uuid,project_id FROM model_rules WHERE line=ANY($1::text[]) AND lock<>'not' AND moved_to_models IS NOT TRUE
   UNION ALL SELECT column_key,'',scope,NULL::uuid,project_id FROM model_row_constraints WHERE line=ANY($1::text[]) AND kind='chain'
  ) used ORDER BY column_key,situation,layer,person_id,project_id LIMIT 257`, lines)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	uses := []lineUse{}
	for rows.Next() {
		var u lineUse
		var project *string
		if err := rows.Scan(&u.Column, &u.Situation, &u.Layer, &u.Person, &project); err != nil {
			return nil, err
		}
		if project != nil {
			u.project = *project
		}
		uses = append(uses, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(uses) > 256 {
		return nil, prefFail(413, "line_usage_limit")
	}
	return uses, nil
}

// removalFallback is what dispatch picks for the use once the set is gone, in
// the use's own situation. A review is paired across families with the row's
// build pick, so that pick, also without the set, names the author family.
func removalFallback(ctx context.Context, tx pgx.Tx, current tenant.Principal, u lineUse, now time.Time, excluded []string) (simplePick, error) {
	q := WorkQuery{Column: u.Column, Area: u.Column, Role: "build", Situation: "first", PersonID: u.Person, ProjectID: u.project, WorkspaceOnly: u.Layer == "default" || u.Layer == "workspace", ExplicitBoard: true, Concept: u.Column == "concept"}
	switch {
	case strings.HasPrefix(u.Column, "review:"):
		q.Role, q.AuthorFamily = "review-gate", strings.TrimPrefix(u.Column, "review:")
	case u.Situation == "review":
		builder, err := resolveBoardWork(ctx, tx, current, q, now, excluded)
		if err != nil || builder == nil || builder.Profile == nil {
			return simplePick{}, err
		}
		q.Role, q.AuthorFamily = "review-gate", builder.Profile.Family
	case u.Situation != "":
		q.Situation = u.Situation
	}
	resolved, err := resolveBoardWork(ctx, tx, current, q, now, excluded)
	if err != nil || resolved == nil {
		return simplePick{}, err
	}
	return pickProfile(resolved.Profile), nil
}
