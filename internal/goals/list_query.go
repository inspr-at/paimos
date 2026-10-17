// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxListBytes = 32 << 20

// One invoker statement loads the whole batch. The work scope is traversed
// once; state and the shared ETA projection consume the same materialized
// pairs. Historical discovery is separate from current facts, so stall never
// samples current state. All intermediate collections have explicit bounds.
func listPageQuery() string {
	return `WITH RECURSIVE
 page AS MATERIALIZED (
 SELECT n.*,k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=ANY($1::uuid[]) AND n.deleted_at IS NULL AND k.slug IN ('goal','release')
 ), outcomes AS MATERIALIZED (
 SELECT n.* FROM nodes n JOIN page g ON g.id=n.parent_id
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.parent_id=ANY($1::uuid[]) AND n.kind_id=ANY(ARRAY(SELECT id FROM node_kinds WHERE slug='outcome')) AND k.slug='outcome' AND n.deleted_at IS NULL
 ), relations AS MATERIALIZED (
 SELECT o.id AS source_node_id,r.target_node_id FROM outcomes o
 CROSS JOIN LATERAL aeon_outcome_serves(o.id) r
 ), counts AS MATERIALIZED (
 SELECT g.id,count(DISTINCT o.id)::int AS outcomes,count(DISTINCT r.target_node_id)::int AS tickets
 FROM page g LEFT JOIN outcomes o ON o.parent_id=g.id LEFT JOIN relations r ON r.source_node_id=o.id GROUP BY g.id
 ), children AS MATERIALIZED (
 SELECT o.* FROM outcomes o JOIN counts c ON c.id=o.parent_id WHERE c.outcomes<=200
 ), relation_targets AS MATERIALIZED (
 SELECT n.id,n.key,n.project_id,k.slug,n.deleted_at IS NOT NULL AS deleted FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=ANY(ARRAY(SELECT target_node_id FROM relations))
 ), serves AS MATERIALIZED (
 SELECT o.parent_id AS goal,o.id AS outcome,n.* FROM children o
 CROSS JOIN LATERAL (SELECT target_node_id FROM relations WHERE source_node_id=o.id ORDER BY target_node_id LIMIT 21) r
 JOIN relation_targets n ON n.id=r.target_node_id
 ), work_roots AS MATERIALIZED (SELECT DISTINCT id FROM serves WHERE NOT deleted AND slug IN ('work','ticket','task','epic')),
 page_scope AS MATERIALIZED (SELECT * FROM aeon_work_scope(ARRAY(SELECT id FROM work_roots))),
 facts(root,id,done,hours,sessions,pr,review,paused,queued,blocked) AS MATERIALIZED (
 WITH scope AS MATERIALIZED (SELECT * FROM page_scope), ` + leafFactsProjectionSQL + `
 ), root_eta AS MATERIALIZED (` + eta.AggregateScopeSQL("page_scope") + `),
 badges AS MATERIALIZED (
 SELECT n.id,coalesce(nullif(n.fields->>'project_key',''),n.key) AS key,n.title AS name,
 ARRAY(SELECT group_id::text FROM project_group_members WHERE project_id=n.id ORDER BY group_id) AS group_ids
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.kind_id=ANY(ARRAY(SELECT id FROM node_kinds WHERE slug='project')) AND k.slug='project' AND n.deleted_at IS NULL AND (n.id IN (SELECT project_id FROM page) OR n.id IN (SELECT project_id FROM serves))
 ), waits AS MATERIALIZED (
 SELECT DISTINCT g.id AS goal,q.node_id AS question FROM page g
 JOIN desk_askers a ON ($3::boolean OR a.principal_id=$4::uuid)
 JOIN desk_questions q ON q.node_id=a.question_id AND q.state='open'
 JOIN nodes n ON n.id=q.node_id AND n.deleted_at IS NULL
 WHERE EXISTS(SELECT 1 FROM serves s WHERE s.goal=g.id AND s.id=a.ticket_id)
 OR EXISTS(SELECT 1 FROM node_relations r WHERE r.type='asks_for' AND r.source_node_id=a.comment_node_id AND r.target_node_id=g.id)
 ), subjects AS MATERIALIZED (
 SELECT DISTINCT g.id AS goal,n.id FROM page g
 CROSS JOIN LATERAL (SELECT e.* FROM events e WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid
 AND e.node_refs @> ARRAY[g.id] AND e.type='goal.items_changed'
 AND (e.before->>'goal_node_id'=g.id::text OR e.after->>'goal_node_id'=g.id::text) OFFSET 0) e
 JOIN nodes n ON n.id=e.node_id AND e.node_id::text=coalesce(e.after->>'outcome_node_id',e.before->>'outcome_node_id')
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug='outcome'
 LIMIT 4097
 ), outcome_events AS MATERIALIZED (
 SELECT s.goal,e.id,e.at,e.node_id,e.after,'outcome'::text AS family,NULL::text AS bucket
 FROM subjects s CROSS JOIN LATERAL (SELECT e.id,e.at,e.node_id,e.after FROM events e
 WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.node_id=s.id AND e.type='goal.items_changed' AND e.at<=$2
 ORDER BY e.at,e.id LIMIT 20001) e
 ORDER BY e.at,e.id LIMIT 20001
 ), history_roots AS MATERIALIZED (
 SELECT DISTINCT e.goal,t.id::uuid AS root FROM outcome_events e
 CROSS JOIN LATERAL jsonb_array_elements_text(e.after->'ticket_node_ids') t(id)
 WHERE (SELECT count(*) FROM subjects)<=4096 AND (SELECT count(*) FROM outcome_events)<=20000
 ), walk(root,id) AS (
 SELECT n.id,n.id FROM nodes n WHERE n.id IN (SELECT root FROM history_roots)
 AND (SELECT count(DISTINCT root) FROM history_roots)<=4096
 UNION SELECT w.root,e.id FROM walk w CROSS JOIN LATERAL (
 (SELECT n.id FROM nodes n WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.parent_id=w.id OFFSET 0)
 UNION SELECT h.id FROM aeon_goal_history_children(w.id,$5,$2) h
 ) e
 ), history_scope AS MATERIALIZED (SELECT root,id FROM walk LIMIT 50001),
 history_nodes AS MATERIALIZED (
 SELECT DISTINCT r.goal,n.id,n.key,n.kind_id,k.slug FROM history_roots r
 JOIN history_scope s ON s.root=r.root CROSS JOIN LATERAL (SELECT n.* FROM nodes n WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=s.id OFFSET 0) n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE (SELECT count(*) FROM history_scope)<=50000
 ), source_events AS MATERIALIZED (
 SELECT * FROM outcome_events
 UNION ALL
 ` + nodeHistoryQuery("$2", "$5") + `
 UNION ALL
 ` + sessionHistoryQuery("$2", "$5") + `
 ), bounded_events AS MATERIALIZED (SELECT * FROM source_events ORDER BY at,id LIMIT 20001),
 kinds AS MATERIALIZED (SELECT id,slug FROM node_kinds ORDER BY id LIMIT 1001)
 SELECT 'bound'::text AS section,''::text AS id,jsonb_build_object('subjects',(SELECT count(*) FROM subjects),
 'roots',(SELECT count(DISTINCT root) FROM history_roots),'pairs',(SELECT count(*) FROM history_scope),
 'events',(SELECT count(*) FROM bounded_events),'kinds',(SELECT count(*) FROM kinds)) AS data
 UNION ALL SELECT 'goal',g.id::text,jsonb_build_object('node',jsonb_build_object(
 'outcome_node_id',g.id,'key',g.key,'label',g.title,'goal_node_id',g.parent_id,'position',g.position::text,'fields',g.fields),
 'project',coalesce(g.project_id::text,''),'kind',g.slug,'created',g.created_at,'counts',to_jsonb(c)-'id')
 FROM page g JOIN counts c ON c.id=g.id
 UNION ALL SELECT 'child',o.parent_id::text,jsonb_build_object('outcome_node_id',o.id,'key',o.key,'label',o.title,
 'goal_node_id',o.parent_id,'position',o.position::text,'fields',o.fields,
 'ticket_node_ids',ARRAY(SELECT s.id::text FROM serves s WHERE s.outcome=o.id ORDER BY s.id)) FROM children o
 UNION ALL SELECT 'target',s.goal::text,jsonb_build_object('id',s.id,'key',s.key,'project',coalesce(s.project_id::text,''),'work',NOT s.deleted AND s.slug IN ('work','ticket','task','epic'),'deleted',s.deleted) FROM serves s
 UNION ALL SELECT 'fact',f.id::text,to_jsonb(f)-'root' FROM (SELECT DISTINCT ON (id) * FROM facts ORDER BY id,root) f
 UNION ALL SELECT 'fact_roots',f.id::text,to_jsonb(array_agg(f.root ORDER BY f.root)) FROM facts f GROUP BY f.id
 UNION ALL SELECT 'root',s.root::text,to_jsonb(count(*)) FROM page_scope s GROUP BY s.root
 UNION ALL SELECT 'eta',e.id::text,jsonb_build_object('live',e.eta_live_at,'ready',e.eta_ready_at,'live_by',e.live_by,'ready_by',e.ready_by) FROM root_eta e
 UNION ALL SELECT 'badge',b.id::text,to_jsonb(b) FROM badges b
 UNION ALL SELECT 'wait',g.id::text,jsonb_build_object('count',(SELECT count(*) FROM waits w WHERE w.goal=g.id),
 'question_ids',ARRAY(SELECT question::text FROM waits w WHERE w.goal=g.id ORDER BY question LIMIT 3)) FROM page g
 UNION ALL SELECT 'metadata',h.goal::text,jsonb_build_object('ID',h.id,'Key',h.key,'Kind',h.kind_id,'Work',h.slug IN ('work','ticket','task','epic')) FROM history_nodes h
 UNION ALL SELECT 'history_root',r.goal::text,jsonb_build_object('root',r.root,'pairs',count(*)) FROM history_roots r JOIN history_scope s ON s.root=r.root GROUP BY r.goal,r.root
 UNION ALL SELECT 'event',e.goal::text,jsonb_build_object('ID',e.id,'At',e.at,'Node',e.node_id,'Family',e.family,'After',e.after,'Bucket',e.bucket) FROM bounded_events e
 UNION ALL SELECT 'kind',k.id::text,to_jsonb(k.slug IN ('work','ticket','task','epic')) FROM kinds k`
}

type listBudget struct {
	roots, history map[string]int
	events         map[int64]int
}

func newListBudget() listBudget {
	return listBudget{map[string]int{}, map[string]int{}, map[int64]int{}}
}

func (b listBudget) add(other listBudget) {
	for k, v := range other.roots {
		b.roots[k] = v
	}
	for k, v := range other.history {
		b.history[k] = v
	}
	for k, v := range other.events {
		b.events[k] = v
	}
}

func (b listBudget) fits() bool {
	for _, roots := range []map[string]int{b.roots, b.history} {
		pairs := 0
		for _, n := range roots {
			pairs += n
		}
		if len(roots) > 4096 || pairs > 50000 {
			return false
		}
	}
	bytes := 0
	for _, n := range b.events {
		bytes += n
	}
	return len(b.events) <= maxSeriesEvents && bytes <= maxListBytes
}

// A failed work scope is monotonic in its root set within this fenced read.
// Retain only already visibility-filtered identities/counts emitted before the
// scope error, so retries can avoid repeating a known over-budget traversal.
type listScopeFailure struct {
	cause error
	roots map[string][]string
	bases map[string]GoalView
}

func (e *listScopeFailure) Error() string { return e.cause.Error() }
func (e *listScopeFailure) Unwrap() error { return e.cause }

type listPageData struct {
	goals     map[string]GoalView
	children  map[string][]storedNode
	facts     map[string][]leafFact
	leaves    map[string]leafFact
	leafRoots map[string][]string
	keys      map[string]string
	projects  map[string]string
	work      map[string]bool
	rootPairs map[string]int
	etas      map[string]EffectiveETA
	badges    map[string]ProjectBadge
	histories map[string]*reconstruction
	kinds     map[string]bool
	waits     map[string]Waits
	budgets   map[string]listBudget
}

func loadListPage(ctx context.Context, tx pgx.Tx, p tenant.Principal, ids []string, now time.Time, w Watermark) ([]GoalView, error) {
	d := listPageData{goals: map[string]GoalView{}, children: map[string][]storedNode{}, facts: map[string][]leafFact{}, leaves: map[string]leafFact{}, leafRoots: map[string][]string{}, keys: map[string]string{}, projects: map[string]string{}, work: map[string]bool{}, rootPairs: map[string]int{}, etas: map[string]EffectiveETA{}, badges: map[string]ProjectBadge{}, histories: map[string]*reconstruction{}, kinds: map[string]bool{}, waits: map[string]Waits{}, budgets: map[string]listBudget{}}
	for _, id := range ids {
		d.budgets[id] = newListBudget()
		d.histories[id] = &reconstruction{goal: id, metadata: map[string]historicalNode{}}
	}
	starts, _, err := planSeries(now, "24h")
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, listPageQuery(), ids, now, p.Kind == tenant.Person, p.ID, starts[0])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bytes := 0
	for rows.Next() {
		var section, id string
		var raw []byte
		if err := rows.Scan(&section, &id, &raw); err != nil {
			return nil, err
		}
		bytes += len(raw)
		if len(raw) > 1<<20 || bytes > maxListBytes {
			return nil, fail(413, "goal list byte budget exceeded")
		}
		if err := d.read(section, id, raw, w); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "54000" && strings.HasPrefix(pg.Message, "work scope ") && len(d.goals) == len(ids) {
			roots := map[string][]string{}
			for _, id := range ids {
				set := map[string]bool{}
				for _, child := range d.children[id] {
					for _, root := range child.Targets {
						if d.work[root] {
							set[root] = true
						}
					}
				}
				roots[id] = []string{}
				for root := range set {
					roots[id] = append(roots[id], root)
				}
				sort.Strings(roots[id])
			}
			return nil, &listScopeFailure{cause: err, roots: roots, bases: d.goals}
		}
		return nil, err
	}
	// Decode each leaf once, then share its immutable facts across the bounded
	// root/leaf memberships. Overlapping roots must not repeat JSON payloads.
	for leaf, roots := range d.leafRoots {
		for _, root := range roots {
			d.facts[root] = append(d.facts[root], d.leaves[leaf])
		}
	}
	for _, facts := range d.facts {
		sort.Slice(facts, func(i, j int) bool { return facts[i].ID < facts[j].ID })
	}
	out := []GoalView{}
	for _, id := range ids {
		g, ok := d.goals[id]
		if !ok {
			return nil, pgx.ErrNoRows
		}
		if g.Counts.Outcomes > MaxOutcomes {
			g.Expansion = "too_large"
			out = append(out, g)
			continue
		}
		g.Waits = d.waits[id]
		g.budget = d.budgets[id]
		children := d.children[id]
		etas := map[string]EffectiveETA{}
		for _, n := range children {
			if len(n.Targets) > MaxTargets {
				return nil, fail(413, "outcome target bound exceeded")
			}
			value := authoredETA(n)
			if value.Source == "none" {
				for _, root := range n.Targets {
					if e := d.etas[root]; e.At != nil && (value.At == nil || e.At.After(*value.At)) {
						value = e
					}
				}
			}
			etas[n.ID] = value
		}
		var moving []string
		g, moving = deriveGoal(g, children, d.facts, etas, d.keys, now)
		projectSet := map[string]bool{g.node.Project: true}
		movingSet := map[string]bool{}
		if g.States.Moving > 0 {
			movingSet[g.node.Project] = true
		}
		for _, root := range g.targets {
			projectSet[d.projects[root]] = true
			if d.work[root] {
				g.budget.roots[root] = d.rootPairs[root]
			}
		}
		for _, root := range moving {
			movingSet[d.projects[root]] = true
		}
		for project := range projectSet {
			if b, ok := d.badges[project]; ok {
				g.Projects = append(g.Projects, b)
			}
		}
		for project := range movingSet {
			if _, ok := d.badges[project]; ok {
				g.movingProjects = append(g.movingProjects, project)
			}
		}
		sort.Slice(g.Projects, func(i, j int) bool {
			if g.Projects[i].Key == g.Projects[j].Key {
				return g.Projects[i].ID < g.Projects[j].ID
			}
			return g.Projects[i].Key < g.Projects[j].Key
		})
		h := d.histories[id]
		h.created = g.created
		h.kinds = d.kinds
		// The statement already orders these globally; sorting also defines ties
		// after partitioning by goal and avoids depending on UNION row order.
		sort.Slice(h.events, func(i, j int) bool {
			if h.events[i].At.Equal(h.events[j].At) {
				if h.events[i].ID == h.events[j].ID {
					return h.events[i].Node < h.events[j].Node
				}
				return h.events[i].ID < h.events[j].ID
			}
			return h.events[i].At.Before(h.events[j].At)
		})
		_, since, err := h.project(ctx, now, "24h")
		if err != nil {
			return nil, err
		}
		stall := DeriveStall(g.States.Moving, since, now)
		g.Stall = &stall
		out = append(out, g)
	}
	return out, nil
}

func (d *listPageData) read(section, id string, raw []byte, w Watermark) error {
	switch section {
	case "bound":
		var b struct{ Subjects, Roots, Pairs, Events, Kinds int }
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		if b.Subjects > 4096 || b.Roots > 4096 || b.Pairs > 50000 || b.Events > maxSeriesEvents || b.Kinds > 1000 {
			return fail(413, "goal list history budget exceeded")
		}
	case "goal":
		var value struct {
			Node          storedNode
			Project, Kind string
			Created       time.Time
			Counts        Counts
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		value.Node.Project, value.Node.Slug = value.Project, value.Kind
		g, err := baseGoal(value.Node, w)
		if err != nil {
			return err
		}
		g.created, g.Counts, g.budget = value.Created, value.Counts, newListBudget()
		d.goals[id] = g
	case "child":
		var n storedNode
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		d.children[id] = append(d.children[id], n)
	case "target":
		var t struct {
			ID, Key, Project string
			Work, Deleted    bool
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		if !t.Deleted {
			d.keys[t.ID] = t.Key
		}
		d.projects[t.ID], d.work[t.ID] = t.Project, t.Work
	case "fact":
		var l leafFact
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		if len(l.Sessions) > 200 {
			return fail(413, "outcome session bound exceeded")
		}
		d.leaves[id] = l
	case "fact_roots":
		var roots []string
		if err := json.Unmarshal(raw, &roots); err != nil {
			return err
		}
		d.leafRoots[id] = roots
	case "root":
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		d.rootPairs[id] = n
	case "eta":
		var e struct {
			Live, Ready *time.Time
			LiveBy      string `json:"live_by"`
			ReadyBy     string `json:"ready_by"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		at, by := e.Live, e.LiveBy
		if at == nil {
			at, by = e.Ready, e.ReadyBy
		}
		if at != nil {
			utc := at.UTC()
			d.etas[id] = EffectiveETA{Source: "worker", At: &utc, By: by}
		}
	case "badge":
		var b ProjectBadge
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		d.badges[id] = b
	case "wait":
		var waits Waits
		if err := json.Unmarshal(raw, &waits); err != nil {
			return err
		}
		d.waits[id] = waits
	case "metadata":
		var n historicalNode
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		d.histories[id].metadata[n.ID] = n
	case "history_root":
		var r struct {
			Root  string
			Pairs int
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		b := d.budgets[id]
		b.history[r.Root] = r.Pairs
	case "event":
		var e sourceEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if err := d.histories[id].add(e); err != nil {
			return err
		}
		b := d.budgets[id]
		b.events[e.ID] = len(e.After)
	case "kind":
		var work bool
		if err := json.Unmarshal(raw, &work); err != nil {
			return err
		}
		d.kinds[id] = work
	}
	return nil
}

// Shrink failed aggregate batches under a savepoint. A singleton retains its
// visible counts and becomes too_large; siblings still receive full values.
// The same batch loader builds the full-filter sort/totals input, in chunks of
// at most 100, so pagination never changes totals or silently samples them.
func readGoalList(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodes []storedNode, now time.Time, w Watermark) ([]GoalView, error) {
	out := []GoalView{}
	responseBytes := 0
	knownRoots := map[string][]string{}
	bases := map[string]GoalView{}
	failedSets := []map[string]bool{}
	appendValues := func(values []GoalView) error {
		for _, g := range values {
			raw, err := json.Marshal(g)
			if err != nil {
				return err
			}
			responseBytes += len(raw)
			if responseBytes > maxListBytes {
				return fail(413, "goals: narrow project/group filters to fit the list byte budget")
			}
		}
		out = append(out, values...)
		return nil
	}
	knownFailure := func(nodes []storedNode) bool {
		union := map[string]bool{}
		for _, n := range nodes {
			roots, ok := knownRoots[n.ID]
			if !ok {
				return false
			}
			for _, root := range roots {
				union[root] = true
			}
		}
		for _, failed := range failedSets {
			contains := true
			for root := range failed {
				if !union[root] {
					contains = false
					break
				}
			}
			if contains {
				return true
			}
		}
		return false
	}
	var load func([]storedNode) error
	load = func(nodes []storedNode) error {
		ids := make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID
		}
		var values []GoalView
		var err error
		if knownFailure(nodes) {
			err = fail(413, "known work scope budget exceeded")
		} else {
			nested, beginErr := tx.Begin(ctx)
			if beginErr != nil {
				return beginErr
			}
			values, err = loadListPage(ctx, nested, p, ids, now, w)
			if err == nil {
				if err := nested.Commit(ctx); err != nil {
					return err
				}
			} else if rollback := nested.Rollback(ctx); rollback != nil {
				return rollback
			}
		}
		if err == nil {
			return appendValues(values)
		}
		if !tooLarge(err) {
			return err
		}
		var scope *listScopeFailure
		if errors.As(err, &scope) {
			failed := map[string]bool{}
			for id, roots := range scope.roots {
				knownRoots[id] = roots
				bases[id] = scope.bases[id]
				for _, root := range roots {
					failed[root] = true
				}
			}
			if len(failed) > 0 {
				failedSets = append(failedSets, failed)
			}
			// Isolate the largest contribution first. Learned failures then prune
			// unions containing it, without another database traversal.
			nodes = append([]storedNode{}, nodes...)
			sort.Slice(nodes, func(i, j int) bool {
				a, b := len(knownRoots[nodes[i].ID]), len(knownRoots[nodes[j].ID])
				if a == b {
					return nodes[i].ID < nodes[j].ID
				}
				return a > b
			})
		}
		if len(nodes) > 1 {
			if scope != nil {
				if err := load(nodes[:1]); err != nil {
					return err
				}
				return load(nodes[1:])
			}
			mid := len(nodes) / 2
			if err := load(nodes[:mid]); err != nil {
				return err
			}
			return load(nodes[mid:])
		}
		g, ok := bases[nodes[0].ID]
		if !ok {
			g, err = baseGoal(nodes[0], w)
			if err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `SELECT count(DISTINCT o.id)::int,count(DISTINCT r.target_node_id)::int FROM nodes o JOIN node_kinds k ON k.tenant_id=o.tenant_id AND k.id=o.kind_id AND k.slug='outcome' LEFT JOIN node_relations r ON r.source_node_id=o.id AND r.type='serves' WHERE o.parent_id=$1 AND o.deleted_at IS NULL`, g.ID).Scan(&g.Counts.Outcomes, &g.Counts.Tickets)
			if err != nil {
				return err
			}
		}
		g.Expansion = "too_large"
		g.budget = newListBudget()
		return appendValues([]GoalView{g})
	}
	for len(nodes) > 0 {
		n := min(100, len(nodes))
		if err := load(nodes[:n]); err != nil {
			return nil, err
		}
		nodes = nodes[n:]
	}
	return out, nil
}
