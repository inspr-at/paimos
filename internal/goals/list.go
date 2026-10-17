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
	Watermark
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
	q := r.URL.Query()
	limit, err := positiveLimit(r, 100, 50)
	if err != nil {
		respond(w, nil, err)
		return
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	project, group := q.Get("project"), q.Get("group")
	sortBy, dir, state := q.Get("sort"), q.Get("dir"), q.Get("state")
	if sortBy == "" {
		sortBy = "eta"
	}
	if !slices.Contains([]string{"eta", "name", "progress", "project", "moving", "queued", "done"}, sortBy) {
		respond(w, nil, invalid("sort", "unknown goal sort"))
		return
	}
	if dir == "" {
		dir = "asc"
		if slices.Contains([]string{"progress", "moving", "queued", "done"}, sortBy) {
			dir = "desc"
		}
	}
	if dir != "asc" && dir != "desc" || len(project) > 64 || len(group) > 64 || utf8.RuneCountInString(search) > 200 || state != "" && !State(state).valid() {
		respond(w, nil, invalid("query", "invalid direction, state or filter"))
		return
	}
	hideDone := q.Get("hide_done") == "true"
	if q.Get("hide_done") != "" && q.Get("hide_done") != "false" && !hideDone {
		respond(w, nil, invalid("hide_done", "must be true or false"))
		return
	}
	queryDigest := sha256.Sum256([]byte(strings.Join([]string{project, group, search, state, q.Get("hide_done"), sortBy, dir}, "\x00")))
	queryKey := hex.EncodeToString(queryDigest[:])
	var cursor *goalCursor
	if raw := q.Get("cursor"); raw != "" {
		c, e := decodeGoalCursor(raw)
		if e != nil {
			respond(w, nil, e)
			return
		}
		if c.Query != queryKey {
			respond(w, nil, invalid("cursor", "filters changed"))
			return
		}
		cursor = &c
	}
	out := GoalList{Items: []GoalView{}, QuietProjects: QuietProjects{Keys: []string{}}}
	err = m.readSnapshot(r, p, func(ctx context.Context, tx pgx.Tx, w Watermark, now time.Time) error {
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		// Candidate discovery is bounded before expansion. Oversized scans fail
		// explicitly; totals and computed sorts are never silently sampled.
		rows, err := tx.Query(ctx, `SELECT `+nodeColumns+` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.kind_id IN (SELECT id FROM node_kinds WHERE slug IN ('goal','release')) AND k.slug IN ('goal','release') AND n.deleted_at IS NULL
 AND ($1='' OR EXISTS(SELECT 1 FROM nodes project WHERE (project.id=n.project_id OR project.id IN (SELECT t.project_id FROM nodes o JOIN node_kinds ok ON ok.tenant_id=o.tenant_id AND ok.id=o.kind_id AND ok.slug='outcome' JOIN node_relations r ON r.source_node_id=o.id AND r.type='serves' JOIN nodes t ON t.id=r.target_node_id WHERE o.parent_id=n.id AND o.deleted_at IS NULL)) AND (project.id::text=$1 OR project.key=$1 OR project.fields->>'project_key'=$1)))
 AND ($2='' OR EXISTS(SELECT 1 FROM nodes project JOIN node_kinds pk ON pk.tenant_id=project.tenant_id AND pk.id=project.kind_id AND pk.slug='project' WHERE (project.id=n.project_id OR project.id IN (SELECT t.project_id FROM nodes o JOIN node_kinds ok ON ok.tenant_id=o.tenant_id AND ok.id=o.kind_id AND ok.slug='outcome' JOIN node_relations r ON r.source_node_id=o.id AND r.type='serves' JOIN nodes t ON t.id=r.target_node_id WHERE o.parent_id=n.id AND o.deleted_at IS NULL)) AND (EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=project.id AND gm.group_id::text=$2) OR $2='none' AND NOT EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=project.id))))
 ORDER BY n.id LIMIT 1001`, project, group)
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
		candidates := []GoalView{}
		movingProjects := map[string]bool{}
		for _, g := range goals {
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
			if hideDone && g.Done != nil && *g.Done {
				continue
			}
			if state != "" {
				if g.States == nil {
					continue
				}
				count := 0
				switch State(state) {
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
			if search != "" {
				text := g.Title
				if g.NextStep != nil {
					text += " " + g.NextStep.Text
				}
				for _, b := range g.Projects {
					text += " " + b.Key
				}
				for _, item := range g.Items {
					text += " " + item.Label + " " + item.Note + " " + strings.Join(item.Keys, " ")
				}
				if !strings.Contains(strings.ToLower(text), search) {
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
			if dir == "desc" {
				c = -c
			}
			if c == 0 {
				return strings.Compare(aid, bid)
			}
			return c
		}
		sort.Slice(candidates, func(i, j int) bool {
			ak, an := keyForGoal(candidates[i], sortBy)
			bk, bn := keyForGoal(candidates[j], sortBy)
			return compare(ak, an, candidates[i].ID, bk, bn, candidates[j].ID) < 0
		})
		budget := newListBudget()
		for _, g := range candidates {
			key, null := keyForGoal(g, sortBy)
			if cursor != nil && compare(key, null, g.ID, cursor.Key, cursor.Null, cursor.ID) <= 0 {
				continue
			}
			nextBudget := newListBudget()
			nextBudget.add(budget)
			nextBudget.add(g.budget)
			if len(out.Items) == limit || len(out.Items) > 0 && !nextBudget.fits() {
				out.Truncated = len(out.Items) < limit
				last := out.Items[len(out.Items)-1]
				k, n := keyForGoal(last, sortBy)
				out.NextCursor = encodeGoalCursor(goalCursor{Key: k, Null: n, ID: last.ID, Query: queryKey})
				break
			}
			budget = nextBudget
			out.Items = append(out.Items, g)
		}
		projects, err := tx.Query(ctx, `SELECT n.id::text,coalesce(nullif(n.fields->>'project_key',''),n.key) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.kind_id IN (SELECT id FROM node_kinds WHERE slug='project') AND k.slug='project' AND n.deleted_at IS NULL AND ($1='' OR n.id::text=$1 OR n.key=$1 OR n.fields->>'project_key'=$1) AND ($2='' OR EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=n.id AND gm.group_id::text=$2) OR $2='none' AND NOT EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=n.id)) ORDER BY n.id LIMIT 2001`, project, group)
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
	})
	respond(w, out, err)
}
