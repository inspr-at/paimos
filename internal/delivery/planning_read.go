// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/releasehistory/codename"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workquery"
	"github.com/jackc/pgx/v5"
)

type MatchCounts struct {
	Matched    int  `json:"matched_count"`
	Shown      int  `json:"shown_count"`
	Hidden     int  `json:"hidden_count"`
	Finished   int  `json:"hidden_finished"`
	Exit       int  `json:"hidden_exit"`
	Other      int  `json:"hidden_other,omitempty"`
	Incomplete bool `json:"incomplete"`
}

// ParsePlanningOptions bounds raw input before normalization or database work.
func ParsePlanningOptions(v url.Values, opt *ReadOptions) error {
	for _, key := range []string{"view", "q", "hide_closed", "date_field", "date_from", "date_to"} {
		if len(v[key]) > 1 {
			return invalidInput("repeated " + key)
		}
	}
	opt.View = v.Get("view")
	if opt.View != "" && opt.View != "planning" {
		return invalidInput("invalid view")
	}
	if err := workquery.Bounds(v); err != nil {
		return invalidInput(err.Error())
	}
	if opt.View == "" {
		return nil
	}
	if opt.CompletedLater || opt.CompletedUnplaced || opt.Through != "" {
		return invalidInput("planning conflicts with recovery or through")
	}
	values := url.Values{}
	for k, vs := range v {
		values[k] = slices.Clone(vs)
	}
	values["state"] = values["work_state"]
	q, err := workquery.Parse(values)
	if err != nil {
		return invalidInput(err.Error())
	}
	opt.Work = q
	opt.HideClosed = true
	if v.Has("hide_closed") {
		switch v.Get("hide_closed") {
		case "false":
			opt.HideClosed = false
		case "true":
		default:
			return invalidInput("invalid hide_closed")
		}
	}
	opt.HideStates, _, err = workquery.Values(v, "hide_state", func(s string) bool { return !strings.HasPrefix(s, "!") })
	if err != nil {
		return invalidInput(err.Error())
	}
	// Exclusions do not have a meaning in the Hide selector.
	for _, raw := range v["hide_state"] {
		if strings.Contains(raw, "!") {
			return invalidInput("invalid hide_state")
		}
	}
	return nil
}

var placeholder = regexp.MustCompile(`\$([0-9]+)`)

func shiftSQL(sql string, offset int) string {
	return placeholder.ReplaceAllStringFunc(sql, func(s string) string { n, _ := strconv.Atoi(s[1:]); return fmt.Sprintf("$%d", n+offset) })
}
func planningIdentity(p tenant.Principal, project, collection string, opt ReadOptions) string {
	return p.TenantID + ":" + project + ":" + collection + ":" + workquery.Fingerprint(struct {
		Principal tenant.Principal
		View      string
		Work      workquery.Query
		Hide      bool
		States    []string
	}{p, opt.View, opt.Work, opt.HideClosed, opt.HideStates})
}
func planningCTE(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, opt ReadOptions) (string, []any, error) {
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
		return "", nil, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery WHERE tenant_id=$1 AND project_node_id=$2)`, p.TenantID, project).Scan(&exists); err != nil {
		return "", nil, err
	}
	if !exists {
		return "", nil, ErrNotFound
	}
	if len(opt.HideStates) > 0 {
		var valid bool
		err := tx.QueryRow(ctx, `WITH `+workquery.StateCategoryCTE()+` SELECT NOT EXISTS(SELECT 1 FROM unnest($1::text[]) requested WHERE `+workquery.StateNormSQL("requested")+` NOT IN (SELECT norm FROM configured UNION SELECT unnest(ARRAY['open','new','backlog','blocked','in_progress','inprogress','active','qa','accepted','delivered','done','cancelled','canceled','archived']::text[])))`, opt.HideStates).Scan(&valid)
		if err != nil {
			return "", nil, err
		}
		if !valid {
			return "", nil, invalidInput("unknown hide_state")
		}
	}
	q := opt.Work
	q.Within = &project
	q.HideClosed = false
	prefix, args := workquery.SQL(q, false)
	// Keep the shared filtered relation's planner fence, then let LIMIT/EXISTS
	// stop matched counts after enough rows without materializing that population.
	prefix = shiftSQL(prefix, 2)
	args = append([]any{p.TenantID, project}, args...)
	// Match first, then classify Hide. Full rollups still use the unfiltered Effective relation.
	categories := strings.Replace(workquery.StateCategoryCTE(), "configured AS", "planning_states AS", 1)
	bucket := workquery.StateBucketSQL("n.state", "pc")
	hidden := "false"
	if opt.HideClosed {
		hidden = bucket + ` IN ('done','cancelled','archived')`
		if len(opt.HideStates) > 0 {
			args = append(args, opt.HideStates)
			hidden = workquery.StateNormSQL("n.state") + ` IN (SELECT ` + workquery.StateNormSQL("h") + ` FROM unnest($` + strconv.Itoa(len(args)) + `::text[]) h)`
		}
	}
	prefix += `, ` + categories + `, matched AS NOT MATERIALIZED (SELECT e.*, (` + hidden + `) hidden, (` + bucket + `) bucket FROM ` + Effective + ` e JOIN filtered f ON f.id=e.item_node_id JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.item_node_id LEFT JOIN planning_states pc ON pc.kind_id=n.kind_id AND pc.norm=` + workquery.StateNormSQL("n.state") + ` WHERE e.tenant_id=$1 AND e.project_node_id=$2)`
	return prefix, args, nil
}

const countSelect = `SELECT count(*)::int matched_count,count(*) FILTER(WHERE NOT hidden)::int shown_count,count(*) FILTER(WHERE hidden)::int hidden_count,count(*) FILTER(WHERE hidden AND bucket='done')::int hidden_finished,count(*) FILTER(WHERE hidden AND bucket IN ('cancelled','archived'))::int hidden_exit,count(*) FILTER(WHERE hidden AND bucket NOT IN ('done','cancelled','archived'))::int hidden_other,count(*)>10000 incomplete FROM (SELECT hidden,bucket FROM matched WHERE %s LIMIT 10001) bounded`

func planningCounts(ctx context.Context, tx pgx.Tx, prefix string, args []any, pred string) (MatchCounts, error) {
	var c MatchCounts
	err := tx.QueryRow(ctx, prefix+" "+fmt.Sprintf(countSelect, pred), args...).Scan(&c.Matched, &c.Shown, &c.Hidden, &c.Finished, &c.Exit, &c.Other, &c.Incomplete)
	return c, err
}

func planningItems(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, release string, opt ReadOptions, limit int) (ItemPage, error) {
	out := ItemPage{Items: []ItemView{}}
	prefix, args, err := planningCTE(ctx, tx, p, project, opt)
	if err != nil {
		return out, err
	}
	scope := planningIdentity(p, project, "items:"+release+":"+opt.Part, opt)
	c, err := decodeCursor(opt.Cursor, scope)
	if err != nil {
		return out, err
	}
	pred := "e.release_node_id IS NULL"
	if release != "" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL WHERE r.tenant_id=$1 AND r.project_node_id=$2 AND r.release_node_id=$3)`, p.TenantID, project, release).Scan(&exists); err != nil {
			return out, err
		}
		if !exists {
			return out, ErrNotFound
		}
		args = append(args, release)
		pred = fmt.Sprintf("e.release_node_id=$%d::uuid", len(args))
	} else if opt.Part == "tail" {
		pred += " AND e.rank IS NULL"
	} else {
		pred += " AND e.rank IS NOT NULL"
	}
	counts, err := planningCounts(ctx, tx, prefix, args, strings.ReplaceAll(pred, "e.", ""))
	if err != nil {
		return out, err
	}
	out.Matches = &counts
	out.Count = counts.Shown
	out.Incomplete = counts.Incomplete
	idx := len(args)
	args = append(args, c.Rank, c.ID, limit+1)
	seek := fmt.Sprintf(` AND (coalesce(e.rank,''),e.item_node_id)>($%d COLLATE "C",$%d::uuid) ORDER BY e.rank COLLATE "C",e.item_node_id LIMIT $%d`, idx+1, idx+2, idx+3)
	if release == "" && opt.Part == "tail" {
		args[idx] = c.At
		seek = fmt.Sprintf(` AND (e.created_at,e.item_node_id)>($%d::timestamptz,$%d::uuid) ORDER BY e.created_at,e.item_node_id LIMIT $%d`, idx+1, idx+2, idx+3)
	}
	rows, err := tx.Query(ctx, prefix+` SELECT `+itemColumns+` FROM matched e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.item_node_id WHERE `+pred+` AND NOT e.hidden`+seek, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		it, e := scanItem(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, it)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		it := out.Items[limit-1]
		out.NextCursor = encodeCursor(pageCursor{Scope: scope, Rank: it.Rank, ID: it.ItemID, At: it.CreatedAt})
	}
	return out, nil
}

func planningReleases(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, opt ReadOptions) (ReleasePage, error) {
	out := ReleasePage{Items: []ReleaseView{}}
	limit, err := pageLimit(opt.Limit, 50)
	if err != nil {
		return out, err
	}
	if !slices.Contains([]string{"", "active", "planned", "building", "frozen", "released", "abandoned"}, opt.State) {
		return out, invalidInput("invalid release state")
	}
	prefix, args, err := planningCTE(ctx, tx, p, project, opt)
	if err != nil {
		return out, err
	}
	scope := planningIdentity(p, project, "releases:"+opt.State, opt)
	c, err := decodeCursor(opt.Cursor, scope)
	if err != nil {
		return out, err
	}
	idx := len(args)
	matchedNames := []int{}
	for sequence, name := range opt.ProductNames {
		if strings.Contains(strings.ToLower(name), strings.ToLower(opt.Work.Q)) {
			matchedNames = append(matchedNames, sequence)
		}
	}
	slices.Sort(matchedNames)
	args = append(args, opt.State, opt.Work.Q, matchedNames)
	args = append(args, opt.ProductNames != nil)
	filter := fmt.Sprintf(` AND ($%d='' OR r.state=$%d OR ($%d='active' AND r.state IN ('planned','building','frozen'))) AND ($%d='' OR ((r.visibility='internal' OR NOT $%d::bool) AND n.title ILIKE '%%'||$%d||'%%') OR (r.visibility='published' AND r.sequence=ANY($%d::int[])) OR EXISTS(SELECT 1 FROM matched m WHERE m.release_node_id=r.release_node_id))`, idx+1, idx+1, idx+1, idx+2, idx+4, idx+2, idx+3)
	countSQL := prefix + ` SELECT count(*) FROM (SELECT 1 FROM project_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL WHERE r.tenant_id=$1 AND r.project_node_id=$2` + filter + ` LIMIT 10001) bounded`
	var total int
	if err = tx.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return out, err
	}
	out.Matches = &MatchCounts{Matched: total, Shown: total, Incomplete: total > 10000}
	i := len(args)
	var at any
	if opt.Cursor != "" {
		at = c.At
	}
	args = append(args, c.Rank, c.ID, limit+1, at)
	seek := fmt.Sprintf(` AND (r.rank,r.release_node_id)>($%d COLLATE "C",$%d::uuid) ORDER BY r.rank COLLATE "C",r.release_node_id LIMIT $%d`, i+1, i+2, i+3)
	// Reference the typed timestamp even for rank reads, so every parameter is bound.
	if opt.State == "released" {
		seek = fmt.Sprintf(` AND $%d::text IS NOT NULL AND ($%d::timestamptz IS NULL OR (r.released_at,r.release_node_id)<($%d,$%d::uuid)) ORDER BY r.released_at DESC,r.release_node_id DESC LIMIT $%d`, i+1, i+4, i+4, i+2, i+3)
	} else {
		seek = ` AND ($` + strconv.Itoa(i+4) + `::timestamptz IS NULL OR true)` + seek
	}
	countsSQL := fmt.Sprintf(countSelect, `release_node_id=r.release_node_id`)
	query := strings.Replace(releaseViewSQL, ` FROM project_releases r`, `,mc.matched_count,mc.shown_count,mc.hidden_count,mc.hidden_finished,mc.hidden_exit,mc.hidden_other,mc.incomplete FROM project_releases r`, 1)
	query = strings.Replace(query, ` WHERE r.tenant_id=$1`, ` LEFT JOIN LATERAL (`+countsSQL+`) mc ON true WHERE r.tenant_id=$1`, 1)
	rows, err := tx.Query(ctx, prefix+" "+query+filter+seek, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var counts MatchCounts
		v, e := scanView(rows, &counts.Matched, &counts.Shown, &counts.Hidden, &counts.Finished, &counts.Exit, &counts.Other, &counts.Incomplete)
		if e != nil {
			return out, e
		}
		v.Matches = &counts
		v.DisplayName = v.Title
		if v.Visibility == "published" && opt.ProductNames[v.Sequence] != "" {
			v.DisplayName = opt.ProductNames[v.Sequence]
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		v := out.Items[limit-1]
		at := c.At
		if v.ReleasedAt != nil {
			at = *v.ReleasedAt
		}
		out.NextCursor = encodeCursor(pageCursor{Scope: scope, Rank: v.Rank, ID: v.ID, At: at})
	}
	return out, nil
}

// OverviewQuery keeps every section/count in one bounded repeatable-read request.
func (s *Store) OverviewQuery(ctx context.Context, p tenant.Principal, project string, opt ReadOptions) (Overview, error) {
	if opt.View != "planning" {
		return s.Overview(ctx, p, project, opt.Cursor)
	}
	out := Overview{}
	err := s.read(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.productNames(ctx, tx, p, project, &opt); err != nil {
			return err
		}
		activeOpt := opt
		activeOpt.State = "active"
		activeOpt.Cursor = ""
		active, err := planningReleases(ctx, tx, p, project, activeOpt)
		if err != nil {
			return err
		}
		out.Active = active.Items
		out.ActiveNextCursor = active.NextCursor
		releasedOpt := opt
		releasedOpt.State = "released"
		out.Released, err = planningReleases(ctx, tx, p, project, releasedOpt)
		if err != nil {
			return err
		}
		prefix, args, err := planningCTE(ctx, tx, p, project, opt)
		if err != nil {
			return err
		}
		out.Backlog = map[string]int{}
		out.BacklogMatches = map[string]MatchCounts{}
		for _, part := range []string{"ranked", "tail"} {
			pred := "release_node_id IS NULL AND rank IS NOT NULL"
			if part == "tail" {
				pred = "release_node_id IS NULL AND rank IS NULL"
			}
			counts, err := planningCounts(ctx, tx, prefix, args, pred)
			if err != nil {
				return err
			}
			out.BacklogMatches[part] = counts
			out.Backlog[part] = counts.Shown
			out.CountsIncomplete = out.CountsIncomplete || counts.Incomplete
		}
		counts, err := planningCounts(ctx, tx, prefix, args, "true")
		if err != nil {
			return err
		}
		out.Matches = &counts
		abandonedOpt := opt
		abandonedOpt.State = "abandoned"
		abandonedOpt.Cursor = ""
		abandoned, err := planningReleases(ctx, tx, p, project, abandonedOpt)
		if err != nil {
			return err
		}
		out.Abandoned = abandoned.Matches.Matched
		out.CountsIncomplete = out.CountsIncomplete || counts.Incomplete || active.Matches.Incomplete || out.Released.Matches.Incomplete || abandoned.Matches.Incomplete
		return nil
	})
	return out, err
}

// Resolve product presentation from its explicit runtime binding, in one bounded
// snapshot query. Refuse over-budget name inventories rather than lose later hits.
func (s *Store) productNames(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, opt *ReadOptions) error {
	if p.TenantID != s.productTenant || project != s.productProject {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT r.sequence FROM project_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL WHERE r.tenant_id=$1 AND r.project_node_id=$2 AND r.visibility='published' ORDER BY r.sequence LIMIT 10001`, p.TenantID, project)
	if err != nil {
		return err
	}
	defer rows.Close()
	sequences := []int{}
	for rows.Next() {
		var seq int
		if err = rows.Scan(&seq); err != nil {
			return err
		}
		if seq > 10000 {
			return invalidInput("product sequence exceeds name read budget")
		}
		sequences = append(sequences, seq)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(sequences) > 10000 {
		return invalidInput("product name inventory exceeds read budget")
	}
	opt.ProductNames = map[int]string{}
	for _, seq := range sequences {
		if err := ctx.Err(); err != nil {
			return err
		}
		opt.ProductNames[seq] = codename.Codename(seq)
	}
	return nil
}
