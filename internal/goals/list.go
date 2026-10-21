// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type QuietProjects struct {
	Count int      `json:"count"`
	Keys  []string `json:"keys"`
}
type GoalList struct {
	Items         []GoalView    `json:"items"`
	Totals        StateCounts   `json:"totals"`
	TotalsPartial bool          `json:"totals_partial"`
	QuietProjects QuietProjects `json:"quiet_projects"`
	NextCursor    string        `json:"next_cursor,omitempty"`
	Truncated     bool          `json:"truncated"`
	// AEON-1494: the workspace status note (workspace readers only) and the
	// latest authored ETA over every goal the filters match, for freshness.
	StatusNote   *StatusNote `json:"status_note,omitempty"`
	ETALastSetAt *time.Time  `json:"eta_last_set_at,omitempty"`
	// AEON-1614: the goals the caller hid for themselves among the project/group
	// filters, whatever the search, state, hide-done or hidden filter (Goals only).
	HiddenCount *int `json:"hidden_count,omitempty"`
	Watermark
}

// listParams are the filters, sort and page shared by the Goals and Epics
// lists (AEON-1395). The cursor binds the digest of every filter, so a page
// cursor never crosses into another query or the other grouping.
type listParams struct {
	limit                  int
	search, project, group string
	sortBy, dir, state     string
	hideDone               bool
	includeHidden          bool
	queryKey               string
	cursor                 *goalCursor
}

func parseListParams(r *http.Request, scope string) (listParams, error) {
	q := r.URL.Query()
	limit, err := positiveLimit(r, 100, 50)
	if err != nil {
		return listParams{}, err
	}
	l := listParams{limit: limit, search: strings.ToLower(strings.TrimSpace(q.Get("q"))), project: q.Get("project"), group: q.Get("group"), sortBy: q.Get("sort"), dir: q.Get("dir"), state: q.Get("state")}
	if l.sortBy == "" {
		l.sortBy = "eta"
	}
	if !slices.Contains([]string{"eta", "name", "progress", "project", "moving", "queued", "done"}, l.sortBy) {
		return l, invalid("sort", "unknown goal sort")
	}
	if l.dir == "" {
		l.dir = "asc"
		if slices.Contains([]string{"progress", "moving", "queued", "done"}, l.sortBy) {
			l.dir = "desc"
		}
	}
	if l.dir != "asc" && l.dir != "desc" || len(l.project) > 64 || len(l.group) > 64 || utf8.RuneCountInString(l.search) > 200 || l.state != "" && !State(l.state).valid() {
		return l, invalid("query", "invalid direction, state or filter")
	}
	l.hideDone = q.Get("hide_done") == "true"
	if q.Get("hide_done") != "" && q.Get("hide_done") != "false" && !l.hideDone {
		return l, invalid("hide_done", "must be true or false")
	}
	parts := []string{l.project, l.group, l.search, l.state, q.Get("hide_done"), l.sortBy, l.dir}
	// AEON-1614: hidden=exclude (the default) or include; only the Goals list reads it.
	switch hidden := q.Get("hidden"); hidden {
	case "", "exclude":
	case "include":
		l.includeHidden = true
		parts = append(parts, "hidden="+hidden)
	default:
		return l, invalid("hidden", "must be exclude or include")
	}
	if scope != "" {
		parts = append([]string{scope}, parts...)
	}
	queryDigest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	l.queryKey = hex.EncodeToString(queryDigest[:])
	if raw := q.Get("cursor"); raw != "" {
		c, e := decodeGoalCursor(raw)
		if e != nil {
			return l, e
		}
		if c.Query != l.queryKey {
			return l, invalid("cursor", "filters changed")
		}
		l.cursor = &c
	}
	return l, nil
}

func (m *module) list(w http.ResponseWriter, r *http.Request) {
	if err := boundedGoalQuery(r); err != nil {
		respond(w, nil, err)
		return
	}
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	l, err := parseListParams(r, "")
	if err != nil {
		respond(w, nil, err)
		return
	}
	out := GoalList{Items: []GoalView{}, QuietProjects: QuietProjects{Keys: []string{}}}
	err = m.readSnapshot(r, p, func(ctx context.Context, tx pgx.Tx, w Watermark, now time.Time) error {
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		// Candidate discovery is bounded before expansion. Oversized scans fail
		// explicitly; totals and computed sorts are never silently sampled.
		rows, err := tx.Query(ctx, `SELECT `+nodeColumns+` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.kind_id=ANY(ARRAY(SELECT id FROM node_kinds WHERE slug IN ('goal','release'))) AND k.slug IN ('goal','release') AND n.deleted_at IS NULL
 AND ($1='' OR EXISTS(SELECT 1 FROM nodes project WHERE (project.id=n.project_id OR project.id IN (SELECT t.project_id FROM nodes o JOIN node_kinds ok ON ok.tenant_id=o.tenant_id AND ok.id=o.kind_id AND ok.slug='outcome' CROSS JOIN LATERAL aeon_outcome_serves(o.id) r JOIN nodes t ON t.id=r.target_node_id WHERE o.parent_id=n.id AND o.deleted_at IS NULL)) AND (project.id::text=$1 OR project.key=$1 OR project.fields->>'project_key'=$1)))
 AND ($2='' OR EXISTS(SELECT 1 FROM nodes project JOIN node_kinds pk ON pk.tenant_id=project.tenant_id AND pk.id=project.kind_id AND pk.slug='project' WHERE (project.id=n.project_id OR project.id IN (SELECT t.project_id FROM nodes o JOIN node_kinds ok ON ok.tenant_id=o.tenant_id AND ok.id=o.kind_id AND ok.slug='outcome' CROSS JOIN LATERAL aeon_outcome_serves(o.id) r JOIN nodes t ON t.id=r.target_node_id WHERE o.parent_id=n.id AND o.deleted_at IS NULL)) AND (EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=project.id AND gm.group_id::text=$2) OR $2='none' AND NOT EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=project.id))))
 ORDER BY n.id LIMIT 1001`, l.project, l.group)
		if err != nil {
			return err
		}
		nodes := []storedNode{}
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				rows.Close()
				return err
			}
			nodes = append(nodes, n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(nodes) > 1000 {
			return fail(413, "goals: narrow project/group filters to at most 1000 goals")
		}
		check, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if !check("nodes.read", n.Project) {
				return authz.ErrForbidden
			}
		}
		goals, err := readGoalList(ctx, tx, p, nodes, now, w)
		if err != nil {
			return err
		}
		ids := make([]string, len(goals))
		for i, g := range goals {
			ids[i] = g.ID
		}
		hidden, err := hiddenAmong(ctx, tx, p, ids)
		if err != nil {
			return err
		}
		count := 0
		for i := range goals {
			if hidden[goals[i].ID] {
				goals[i].Hidden = true
				count++
			}
		}
		out.HiddenCount = &count
		return finishList(ctx, tx, p, l, goals, &out, w)
	})
	respond(w, out, err)
}

// finishList applies the full-filter totals, the list filters, the computed
// sort and the page budget, then the note authors, the status note and the
// quiet projects. Goals and Epics share it, so their lists behave alike.
func finishList(ctx context.Context, tx pgx.Tx, p tenant.Principal, l listParams, goals []GoalView, out *GoalList, w Watermark) error {
	candidates := []GoalView{}
	movingProjects := map[string]bool{}
	var etaSetAt time.Time
	for _, g := range goals {
		for _, at := range []time.Time{g.etaSetAt, fieldTime(g.node, "eta_set_at")} {
			if at.After(etaSetAt) {
				etaSetAt = at
			}
		}
		if g.States == nil {
			out.TotalsPartial = true
		} else {
			out.Totals.Moving += g.States.Moving
			out.Totals.Queued += g.States.Queued
			out.Totals.Paused += g.States.Paused
			out.Totals.Done += g.States.Done
			if g.States.Moving > 0 {
				for _, id := range g.movingProjects {
					movingProjects[id] = true
				}
			}
		}
		// A goal the person hid counts like a hidden done goal: in the totals, not in the list.
		if g.Hidden && !l.includeHidden {
			continue
		}
		if l.hideDone && g.Done != nil && *g.Done {
			continue
		}
		if l.state != "" {
			if g.States == nil {
				continue
			}
			count := 0
			switch State(l.state) {
			case Moving:
				count = g.States.Moving
			case Queued:
				count = g.States.Queued
			case Paused:
				count = g.States.Paused
			case Done:
				count = g.States.Done
			}
			if count == 0 {
				continue
			}
		}
		if l.search != "" {
			text := g.Title + g.searchText
			if g.NextStep != nil {
				text += " " + g.NextStep.Text
			}
			for _, b := range g.Projects {
				text += " " + b.Key
			}
			for _, item := range g.Items {
				text += " " + item.Label + " " + item.Note + " " + strings.Join(item.Keys, " ")
			}
			if !strings.Contains(strings.ToLower(text), l.search) {
				continue
			}
		}
		candidates = append(candidates, g)
	}
	compare := func(ak string, an bool, aid, bk string, bn bool, bid string) int {
		if an != bn {
			if an {
				return 1
			}
			return -1
		}
		c := strings.Compare(ak, bk)
		if l.dir == "desc" {
			c = -c
		}
		if c == 0 {
			return strings.Compare(aid, bid)
		}
		return c
	}
	sort.Slice(candidates, func(i, j int) bool {
		ak, an := keyForGoal(candidates[i], l.sortBy)
		bk, bn := keyForGoal(candidates[j], l.sortBy)
		return compare(ak, an, candidates[i].ID, bk, bn, candidates[j].ID) < 0
	})
	budget := newListBudget()
	for _, g := range candidates {
		key, null := keyForGoal(g, l.sortBy)
		if l.cursor != nil && compare(key, null, g.ID, l.cursor.Key, l.cursor.Null, l.cursor.ID) <= 0 {
			continue
		}
		nextBudget := newListBudget()
		nextBudget.add(budget)
		nextBudget.add(g.budget)
		if len(out.Items) == l.limit || len(out.Items) > 0 && !nextBudget.fits() {
			out.Truncated = len(out.Items) < l.limit
			last := out.Items[len(out.Items)-1]
			k, n := keyForGoal(last, l.sortBy)
			out.NextCursor = encodeGoalCursor(goalCursor{Key: k, Null: n, ID: last.ID, Query: l.queryKey})
			break
		}
		budget = nextBudget
		out.Items = append(out.Items, g)
	}
	if !etaSetAt.IsZero() {
		out.ETALastSetAt = &etaSetAt
	}
	if err := attachNoteAuthors(ctx, tx, out.Items); err != nil {
		return err
	}
	var err error
	if out.StatusNote, err = visibleStatusNote(ctx, tx, p); err != nil {
		return err
	}
	projects, err := tx.Query(ctx, `SELECT n.id::text,coalesce(nullif(n.fields->>'project_key',''),n.key) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.kind_id=ANY(ARRAY(SELECT id FROM node_kinds WHERE slug='project')) AND k.slug='project' AND n.deleted_at IS NULL AND ($1='' OR n.id::text=$1 OR n.key=$1 OR n.fields->>'project_key'=$1) AND ($2='' OR EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=n.id AND gm.group_id::text=$2) OR $2='none' AND NOT EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=n.id)) ORDER BY n.id LIMIT 2001`, l.project, l.group)
	if err != nil {
		return err
	}
	defer projects.Close()
	count := 0
	for projects.Next() {
		count++
		if count > 2000 {
			return fail(413, "quiet project bound exceeded")
		}
		var id, key string
		if err := projects.Scan(&id, &key); err != nil {
			return err
		}
		if !movingProjects[id] {
			out.QuietProjects.Keys = append(out.QuietProjects.Keys, key)
		}
	}
	sort.Strings(out.QuietProjects.Keys)
	out.QuietProjects.Count = len(out.QuietProjects.Keys)
	out.Watermark = w
	return projects.Err()
}
