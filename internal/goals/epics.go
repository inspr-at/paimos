// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// AEON-1395: Goals › Group: Epics. GET /api/goals/epics lists every open epic
// or parent work item in the goal shape, so the Goals view renders them with
// the goal-row anatomy. An area is the topmost open parent: an open work node
// with work children whose own parent is no open work node. Its items are its
// direct children (Closed ones left out); an item's keys are its own key and
// its children's. Nothing is hand-kept: counts follow the children's status
// and live work, progress is the leaf-hours rollup (leaves only, a parent's
// estimate never counts), LANDS is the latest worker ETA, else the target
// release, else the suggested release, and "what's missing" is the epic's
// failing entry checks for its next status from the Definitions engine, then
// holds and blockers. Notes and session ids need harness.read; the author's
// name needs harness.read or members.read; a pull request and a hold need
// delivery.read. An epic is done only when its own status is.
const (
	epicWorkKinds  = `('work','epic','ticket','task')`
	epicClosed     = `('done','delivered','accepted','cancelled','archived')`
	maxEpicSteps   = MaxSteps
	maxEpicHolds   = 3
	maxEpicBlocks  = 3
	maxEpicBatch   = 100
	epicStepIDSize = 32
)

type epicNode struct {
	storedNode
	State, Category string
	Created         time.Time
}

type epicChild struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	Title     string  `json:"title"`
	State     string  `json:"state"`
	Category  string  `json:"category"`
	Position  string  `json:"position"`
	Release   string  `json:"release"`
	Suggested string  `json:"suggested"`
	Estimate  float64 `json:"estimate"`
}

type epicNote struct {
	Note string     `json:"note"`
	By   string     `json:"author"`
	At   *time.Time `json:"at"`
}

// EpicDelivery is a child's most recent unmerged pull request state, its live
// line when no agent wrote a note.
type EpicDelivery struct {
	State       string `json:"state"`
	PullRequest *int64 `json:"pull_request,omitempty"`
}

type epicHold struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

type epicBlocker struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type epicPageData struct {
	items      map[string]int
	children   map[string][]epicChild
	leaves     map[string]leafFact
	leafRoots  map[string][]string
	rootPairs  map[string]int
	etas       map[string]EffectiveETA
	reported   map[string]time.Time
	keys       map[string][]string
	keysTotal  map[string]int
	notes      map[string]epicNote
	deliveries map[string]EpicDelivery
	holds      map[string][]epicHold
	blockers   map[string][]epicBlocker
	waits      map[string]Waits
	badges     map[string]ProjectBadge
	queue      map[string]queueWait
	cadence    releaseGap
	cadenceOK  bool
	mayRelease map[string]bool
}

// The release label as the node list reads it: a string or {label|name},
// also from the imported classic fields.
func fieldLabelSQL(alias, key string) string {
	value := func(path string) string {
		return `CASE jsonb_typeof(` + path + `) WHEN 'object' THEN coalesce(` + path + `->>'label',` + path + `->>'name') WHEN 'string' THEN ` + path + `#>>'{}' END`
	}
	return `left(btrim(coalesce(CASE WHEN ` + alias + `.fields ? '` + key + `' THEN ` + value(`(`+alias+`.fields->'`+key+`')`) + ` ELSE ` + value(`(`+alias+`.fields->'classic'->'`+key+`')`) + ` END,'')),40)`
}

func releaseLabelSQL(alias string) string { return fieldLabelSQL(alias, "release") }

const epicCandidateColumns = `n.id::text,n.key,n.title,coalesce(n.parent_id::text,''),coalesce(n.project_id::text,''),k.slug,n.position::text,n.fields,false,n.state,aeon_work_status_category(n.state,k.field_schema),n.created_at`

// One bounded statement finds the candidates; RLS confines them to visible
// projects before the authorization check below.
func epicCandidatesSQL() string {
	return `SELECT ` + epicCandidateColumns + ` FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE k.slug IN ` + epicWorkKinds + ` AND n.deleted_at IS NULL AND n.project_id IS NOT NULL
 AND aeon_work_status_category(n.state,k.field_schema) NOT IN ` + epicClosed + `
 AND EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug IN ` + epicWorkKinds + `)
 AND NOT EXISTS(SELECT 1 FROM nodes a JOIN node_kinds ak ON ak.tenant_id=a.tenant_id AND ak.id=a.kind_id WHERE a.id=n.parent_id AND a.deleted_at IS NULL AND ak.slug IN ` + epicWorkKinds + ` AND aeon_work_status_category(a.state,ak.field_schema) NOT IN ` + epicClosed + `)
 AND ($1='' OR EXISTS(SELECT 1 FROM nodes project WHERE project.id=n.project_id AND (project.id::text=$1 OR project.key=$1 OR project.fields->>'project_key'=$1)))
 AND ($2='' OR EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=n.project_id AND gm.group_id::text=$2) OR $2='none' AND NOT EXISTS(SELECT 1 FROM project_group_members gm WHERE gm.project_id=n.project_id))
 ORDER BY n.id LIMIT 1001`
}

// One invoker statement loads a batch: children, the work scope traversed
// once for leaf facts and worker ETAs, item keys, live notes, unmerged pull
// requests, holds, blockers, Desk waits and the project badges.
func epicPageQuery() string {
	return `WITH page AS MATERIALIZED (
 SELECT n.id,n.project_id FROM nodes n WHERE n.id=ANY($1::uuid[]) AND n.deleted_at IS NULL
 ), children AS MATERIALIZED (
 SELECT c.id,c.parent_id AS epic,c.key,c.title,c.state,c.position,c.fields,c.project_id,aeon_work_status_category(c.state,k.field_schema) AS category
 FROM nodes c JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id
 WHERE c.parent_id=ANY($1::uuid[]) AND c.deleted_at IS NULL AND k.slug IN ` + epicWorkKinds + `
 ), counts AS MATERIALIZED (
 SELECT p.id AS epic,count(c.id) FILTER (WHERE c.category NOT IN ('cancelled','archived'))::int AS items
 FROM page p LEFT JOIN children c ON c.epic=p.id GROUP BY p.id
 ), kept AS MATERIALIZED (
 SELECT c.* FROM children c JOIN counts n ON n.epic=c.epic WHERE n.items<=` + strconv.Itoa(MaxOutcomes) + ` AND c.category NOT IN ('cancelled','archived')
 ), page_scope AS MATERIALIZED (SELECT * FROM aeon_work_scope(ARRAY(SELECT id FROM kept))),
 facts(root,id,done,hours,sessions,pr,review,paused,queued,blocked,merged) AS MATERIALIZED (
 WITH scope AS MATERIALIZED (SELECT * FROM page_scope), ` + leafFactsProjectionSQL + `
 ), root_eta AS MATERIALIZED (` + eta.AggregateScopeSQL("page_scope") + `),
 grandchildren AS MATERIALIZED (
 SELECT g.parent_id AS child,g.key,row_number() OVER (PARTITION BY g.parent_id ORDER BY length(g.key),g.key,g.id) AS rn,count(*) OVER (PARTITION BY g.parent_id)::int AS total
 FROM nodes g JOIN node_kinds k ON k.tenant_id=g.tenant_id AND k.id=g.kind_id
 WHERE g.parent_id IN (SELECT id FROM kept) AND g.deleted_at IS NULL AND k.slug IN ` + epicWorkKinds + ` AND aeon_work_status_category(g.state,k.field_schema) NOT IN ('cancelled','archived')
 ), notes AS MATERIALIZED (
 SELECT DISTINCT ON (s.root) s.root AS child,h.activity_note AS note,h.agent_principal_id::text AS author,
 coalesce((SELECT a.created_at FROM harness_activity_notes a WHERE a.session_id=h.id ORDER BY a.id DESC LIMIT 1),h.heartbeat_at) AS at
 FROM page_scope s JOIN harness_sessions h ON h.ticket_node_id=s.id AND h.project_id=s.project_id
 WHERE s.is_leaf AND h.phase='working' AND h.stopped_at IS NULL AND h.archived_at IS NULL AND h.role IN ('worker','coordinator') AND h.activity_note IS NOT NULL
 ORDER BY s.root,h.heartbeat_at DESC NULLS LAST,h.id
 ), deliveries AS MATERIALIZED (
 SELECT DISTINCT ON (s.root) s.root AS child,d.state,d.pull_request
 FROM page_scope s JOIN delivery_items d ON d.ticket_node_id=s.id
 WHERE d.state<>'merged' AND coalesce(d.observation->>'merged','false')<>'true'
 ORDER BY s.root,d.updated_at DESC,d.id
 ), members AS MATERIALIZED (
 SELECT p.id AS epic,p.id AS node FROM page p UNION SELECT c.epic,s.id FROM kept c JOIN page_scope s ON s.root=c.id
 ), holds AS MATERIALIZED (
 SELECT m.epic,n.key,left(coalesce(d.held_reason,''),160) AS reason,row_number() OVER (PARTITION BY m.epic ORDER BY d.updated_at DESC,d.id) AS rn
 FROM members m JOIN delivery_items d ON d.ticket_node_id=m.node AND d.state='held' JOIN nodes n ON n.id=m.node
 ), blockers AS MATERIALIZED (
 SELECT r.target_node_id AS epic,b.key,left(b.title,160) AS title,row_number() OVER (PARTITION BY r.target_node_id ORDER BY length(b.key),b.key,b.id) AS rn
 FROM node_relations r JOIN nodes b ON b.tenant_id=r.tenant_id AND b.id=r.source_node_id AND b.deleted_at IS NULL
 WHERE r.type='blocks' AND r.target_node_id=ANY($1::uuid[])
 ), waits AS MATERIALIZED (
 SELECT DISTINCT m.epic,q.node_id AS question FROM members m
 JOIN desk_askers a ON a.ticket_id=m.node AND ($3::boolean OR a.principal_id=$4::uuid)
 JOIN desk_questions q ON q.node_id=a.question_id AND q.state='open'
 JOIN nodes n ON n.id=q.node_id AND n.deleted_at IS NULL
 ), badges AS MATERIALIZED (
 SELECT n.id,coalesce(nullif(n.fields->>'project_key',''),n.key) AS key,n.title AS name,
 ARRAY(SELECT group_id::text FROM project_group_members WHERE project_id=n.id ORDER BY group_id) AS group_ids
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.kind_id=ANY(ARRAY(SELECT id FROM node_kinds WHERE slug='project')) AND k.slug='project' AND n.deleted_at IS NULL AND n.id IN (SELECT project_id FROM page)
 )
 SELECT 'count'::text AS section,c.epic::text AS id,to_jsonb(c.items) AS data FROM counts c
 UNION ALL SELECT 'child',c.epic::text,jsonb_build_object('id',c.id,'key',c.key,'title',c.title,'state',c.state,'category',c.category,'position',c.position::text,'release',` + releaseLabelSQL("c") + `,'suggested',` + fieldLabelSQL("c", "suggested_release") + `,'estimate',CASE WHEN jsonb_typeof(c.fields->'estimate_hours')='number' AND (c.fields->>'estimate_hours')::numeric>0 AND (c.fields->>'estimate_hours')::numeric<=200 THEN (c.fields->>'estimate_hours')::float8 ELSE 0 END) FROM kept c
 UNION ALL SELECT 'fact',f.id::text,to_jsonb(f)-'root' FROM (SELECT DISTINCT ON (id) * FROM facts ORDER BY id,root) f
 UNION ALL SELECT 'fact_roots',f.id::text,to_jsonb(array_agg(f.root ORDER BY f.root)) FROM facts f GROUP BY f.id
 UNION ALL SELECT 'root',s.root::text,to_jsonb(count(*)) FROM page_scope s GROUP BY s.root
 UNION ALL SELECT 'eta',e.id::text,jsonb_build_object('live',e.eta_live_at,'ready',e.eta_ready_at,'live_by',e.live_by,'ready_by',e.ready_by,'live_reported',e.live_reported_at,'ready_reported',e.ready_reported_at) FROM root_eta e
 UNION ALL SELECT 'key',g.child::text,jsonb_build_object('key',g.key,'total',g.total) FROM grandchildren g WHERE g.rn<` + strconv.Itoa(MaxTargets) + `
 UNION ALL SELECT 'note',n.child::text,to_jsonb(n)-'child' FROM notes n
 UNION ALL SELECT 'delivery',d.child::text,to_jsonb(d)-'child' FROM deliveries d
 UNION ALL SELECT 'hold',h.epic::text,jsonb_build_object('key',h.key,'reason',h.reason) FROM holds h WHERE h.rn<=` + strconv.Itoa(maxEpicHolds) + `
 UNION ALL SELECT 'blocker',b.epic::text,jsonb_build_object('key',b.key,'title',b.title) FROM blockers b WHERE b.rn<=` + strconv.Itoa(maxEpicBlocks) + `
 UNION ALL SELECT 'wait',p.id::text,jsonb_build_object('count',(SELECT count(*) FROM waits w WHERE w.epic=p.id),'question_ids',ARRAY(SELECT question::text FROM waits w WHERE w.epic=p.id ORDER BY question LIMIT 3)) FROM page p
 UNION ALL SELECT 'badge',b.id::text,to_jsonb(b) FROM badges b`
}

func (m *module) epics(w http.ResponseWriter, r *http.Request) {
	if err := boundedGoalQuery(r); err != nil {
		respond(w, nil, err)
		return
	}
	p, ok := requestPrincipal(w, r)
	if !ok {
		return
	}
	l, err := parseListParams(r, "epics")
	if err != nil {
		respond(w, nil, err)
		return
	}
	out := GoalList{Items: []GoalView{}, QuietProjects: QuietProjects{Keys: []string{}}}
	err = m.readSnapshot(r, p, func(ctx context.Context, tx pgx.Tx, w Watermark, now time.Time) error {
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, epicCandidatesSQL(), l.project, l.group)
		if err != nil {
			return err
		}
		candidates := []epicNode{}
		for rows.Next() {
			var e epicNode
			var raw []byte
			if err := rows.Scan(&e.ID, &e.Key, &e.Title, &e.Parent, &e.Project, &e.Slug, &e.Position, &raw, &e.Deleted, &e.State, &e.Category, &e.Created); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(raw, &e.Fields); err != nil {
				rows.Close()
				return err
			}
			e.Targets = []string{}
			candidates = append(candidates, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(candidates) > 1000 {
			return fail(413, "epics: narrow project/group filters to at most 1000 epics")
		}
		check, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		readable := candidates[:0]
		for _, e := range candidates {
			if check("nodes.read", e.Project) {
				readable = append(readable, e)
			}
		}
		views, err := readEpicList(ctx, tx, p, readable, now, w, check)
		if err != nil {
			return err
		}
		redactEpicSecrets(views, check)
		if err := finishList(ctx, tx, p, l, views, &out, w); err != nil {
			return err
		}
		byID := map[string]epicNode{}
		for _, e := range readable {
			byID[e.ID] = e
		}
		return attachEpicSteps(ctx, tx, p, out.Items, byID)
	})
	respond(w, out, err)
}

// readEpicList loads at most 100 epics per statement. A batch whose work
// scope exceeds its budget is split; a single epic that still exceeds it
// keeps its item count and becomes too_large, so siblings stay complete.
func readEpicList(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodes []epicNode, now time.Time, w Watermark, check authz.ProjectCheck) ([]GoalView, error) {
	out := []GoalView{}
	responseBytes := 0
	var load func([]epicNode) error
	load = func(nodes []epicNode) error {
		nested, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		values, err := loadEpicPage(ctx, nested, p, nodes, now, w, check)
		if err == nil {
			if err := nested.Commit(ctx); err != nil {
				return err
			}
			for _, g := range values {
				raw, err := json.Marshal(g)
				if err != nil {
					return err
				}
				if responseBytes += len(raw); responseBytes > maxListBytes {
					return fail(413, "epics: narrow project/group filters to fit the list byte budget")
				}
			}
			out = append(out, values...)
			return nil
		}
		if rollback := nested.Rollback(ctx); rollback != nil {
			return rollback
		}
		if !tooLarge(err) {
			return err
		}
		if len(nodes) > 1 {
			mid := len(nodes) / 2
			if err := load(nodes[:mid]); err != nil {
				return err
			}
			return load(nodes[mid:])
		}
		g, err := baseEpic(nodes[0], w)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM nodes c JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id WHERE c.parent_id=$1 AND c.deleted_at IS NULL AND k.slug IN `+epicWorkKinds+` AND aeon_work_status_category(c.state,k.field_schema) NOT IN ('cancelled','archived')`, g.ID).Scan(&g.Counts.Outcomes); err != nil {
			return err
		}
		g.Expansion = "too_large"
		out = append(out, g)
		return nil
	}
	for len(nodes) > 0 {
		n := min(maxEpicBatch, len(nodes))
		if err := load(nodes[:n]); err != nil {
			return nil, err
		}
		nodes = nodes[n:]
	}
	return out, nil
}

func baseEpic(e epicNode, w Watermark) (GoalView, error) {
	project := e.Project
	g := GoalView{ID: e.ID, Key: e.Key, Title: e.Title, Kind: "epic", ProjectID: &project, Expansion: "full", Steps: []Step{}, Projects: []ProjectBadge{}, Waits: Waits{QuestionIDs: []string{}}, Watermark: w, node: e.storedNode, searchText: " " + e.Key, created: e.Created, budget: newListBudget()}
	// An epic's node fields belong to the work item, never to the goal model.
	g.node.Fields = map[string]any{}
	return g, nil
}

func loadEpicPage(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodes []epicNode, now time.Time, w Watermark, check authz.ProjectCheck) ([]GoalView, error) {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	d := epicPageData{items: map[string]int{}, children: map[string][]epicChild{}, leaves: map[string]leafFact{}, leafRoots: map[string][]string{}, rootPairs: map[string]int{}, etas: map[string]EffectiveETA{}, reported: map[string]time.Time{}, keys: map[string][]string{}, keysTotal: map[string]int{}, notes: map[string]epicNote{}, deliveries: map[string]EpicDelivery{}, holds: map[string][]epicHold{}, blockers: map[string][]epicBlocker{}, waits: map[string]Waits{}, badges: map[string]ProjectBadge{}, queue: map[string]queueWait{}, mayRelease: map[string]bool{}}
	rows, err := tx.Query(ctx, epicPageQuery(), ids, now, p.Kind == tenant.Person, p.ID)
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
			return nil, fail(413, "epic list byte budget exceeded")
		}
		if err := d.read(section, id, raw); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	facts := map[string][]leafFact{}
	for leaf, roots := range d.leafRoots {
		for _, root := range roots {
			facts[root] = append(facts[root], d.leaves[leaf])
		}
	}
	for _, list := range facts {
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	}
	if err := d.attachQueueLanding(ctx, tx, nodes, now, check); err != nil {
		return nil, err
	}
	out := make([]GoalView, 0, len(nodes))
	for _, e := range nodes {
		g, err := baseEpic(e, w)
		if err != nil {
			return nil, err
		}
		if b, ok := d.badges[e.Project]; ok {
			g.Projects = append(g.Projects, b)
		}
		g.Waits = d.waits[e.ID]
		if g.Waits.QuestionIDs == nil {
			g.Waits.QuestionIDs = []string{}
		}
		g.Counts.Outcomes = d.items[e.ID]
		if g.Counts.Outcomes > MaxOutcomes {
			g.Expansion = "too_large"
			out = append(out, g)
			continue
		}
		for _, c := range d.children[e.ID] {
			g.budget.roots[c.ID] = d.rootPairs[c.ID]
		}
		out = append(out, deriveEpic(g, e, d, facts, now))
	}
	return out, nil
}

func (d *epicPageData) read(section, id string, raw []byte) error {
	switch section {
	case "count":
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		d.items[id] = n
	case "child":
		var c epicChild
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if strings.Contains(c.Position, ".") {
			c.Position = strings.TrimRight(strings.TrimRight(c.Position, "0"), ".")
		}
		d.children[id] = append(d.children[id], c)
	case "fact":
		var l leafFact
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		if len(l.Sessions) > 200 {
			return fail(413, "epic item session bound exceeded")
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
			LiveBy      string     `json:"live_by"`
			ReadyBy     string     `json:"ready_by"`
			LiveAt      *time.Time `json:"live_reported"`
			ReadyAt     *time.Time `json:"ready_reported"`
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
		for _, reported := range []*time.Time{e.LiveAt, e.ReadyAt} {
			if reported != nil && reported.After(d.reported[id]) {
				d.reported[id] = reported.UTC()
			}
		}
	case "key":
		var k struct {
			Key   string
			Total int
		}
		if err := json.Unmarshal(raw, &k); err != nil {
			return err
		}
		d.keys[id] = append(d.keys[id], k.Key)
		d.keysTotal[id] = k.Total
	case "note":
		var n epicNote
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		d.notes[id] = n
	case "delivery":
		var v EpicDelivery
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		d.deliveries[id] = v
	case "hold":
		var h epicHold
		if err := json.Unmarshal(raw, &h); err != nil {
			return err
		}
		d.holds[id] = append(d.holds[id], h)
	case "blocker":
		var b epicBlocker
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		d.blockers[id] = append(d.blockers[id], b)
	case "wait":
		var waits Waits
		if err := json.Unmarshal(raw, &waits); err != nil {
			return err
		}
		d.waits[id] = waits
	case "badge":
		var b ProjectBadge
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		d.badges[id] = b
	}
	return nil
}

// EpicItemState maps a direct child onto the goal states (AEON-1395):
// Moving is a live session, In progress or QA, or an open pull request or
// review; Done is Done, Delivered or Accepted, or every leaf done; Paused is
// Backlog or work paused on purpose; everything else is Queued (open, new
// or queued), with the queue or a blocker as its reason.
func EpicItemState(category, state string, leaves []leafFact) EffectiveState {
	sessions := []string{}
	done, total := 0, 0
	var pr, review, paused, queued, blocked bool
	for _, l := range leaves {
		total++
		if l.Done {
			done++
		}
		sessions = append(sessions, l.Sessions...)
		pr, review, paused = pr || l.PR, review || l.Review, paused || l.Paused
		queued, blocked = queued || l.Queued, blocked || l.Blocked
	}
	slices.Sort(sessions)
	sessions = slices.DeleteFunc(slices.Compact(sessions), func(id string) bool { return id == "" })
	result := EffectiveState{SessionIDs: sessions}
	status := workorders.EntryState(state)
	switch {
	case len(sessions) > 0:
		result.State = Moving
	case category == "done" || category == "delivered" || category == "accepted" || total > 0 && done == total:
		result.State = Done
	case category == "in_progress" || status == "qa" || pr || review:
		result.State = Moving
	case status == "backlog" || paused:
		result.State = Paused
	default:
		result.State = Queued
		switch {
		case queued:
			result.Reason = "start_queue"
		case blocked || category == "blocked":
			result.Reason = "blocked"
		default:
			result.Reason = "waiting"
		}
	}
	return result
}

// EpicProgress is the leaf-hours rollup over an epic's leaves, each leaf once:
// completed hours over all estimated hours, or completed leaves over all
// leaves while no leaf carries an estimate. Parents never contribute hours.
func EpicProgress(leaves []LeafEstimate, done map[string]bool) (pct int, basis string) {
	total, finished, estimated, doneCount := 0.0, 0.0, 0, 0
	for _, l := range leaves {
		if done[l.ID] {
			doneCount++
		}
		if l.Hours > 0 && l.Hours <= 200 && !math.IsNaN(l.Hours) {
			estimated++
			total += l.Hours
			if done[l.ID] {
				finished += l.Hours
			}
		}
	}
	switch {
	case estimated > 0:
		basis = "estimate"
		if estimated < len(leaves) {
			basis = "mixed"
		}
		return int(math.Round(100 * finished / total)), basis
	case len(leaves) > 0:
		return int(math.Round(100 * float64(doneCount) / float64(len(leaves)))), "count"
	}
	return 0, "count"
}

func deriveEpic(g GoalView, e epicNode, d epicPageData, facts map[string][]leafFact, now time.Time) GoalView {
	children := slices.Clone(d.children[e.ID])
	sort.SliceStable(children, func(i, j int) bool {
		a, _ := Position(children[i].Position).decimal()
		b, _ := Position(children[j].Position).decimal()
		if a != nil && b != nil && a.Cmp(b) != 0 {
			return a.Cmp(b) < 0
		}
		if len(children[i].Key) != len(children[j].Key) {
			return len(children[i].Key) < len(children[j].Key)
		}
		if children[i].Key != children[j].Key {
			return children[i].Key < children[j].Key
		}
		return children[i].ID < children[j].ID
	})
	states := StateCounts{}
	g.Items = []OutcomeView{}
	all := []LeafEstimate{}
	doneLeaves := map[string]bool{}
	open := map[string]float64{}
	seen := map[string]bool{}
	progress := GoalProgress{Source: "computed", Total: len(children)}
	moving := false
	for _, c := range children {
		leaves := facts[c.ID]
		state := EpicItemState(c.Category, c.State, leaves)
		switch state.State {
		case Moving:
			states.Moving++
			moving = true
		case Queued:
			states.Queued++
		case Paused:
			states.Paused++
		case Done:
			states.Done++
			progress.Done++
		}
		keys := append([]string{c.Key}, d.keys[c.ID]...)
		item := OutcomeView{ID: c.ID, Ref: strings.ToLower(c.Key), Label: c.Title, Position: Position(c.Position), Keys: keys, EffectiveState: state}
		if total := 1 + d.keysTotal[c.ID]; total > len(keys) {
			item.KeysTotal = total
		}
		parent := d.keysTotal[c.ID] > 0
		if value, ok := d.etas[c.ID]; ok && state.State != Done {
			item.ETA = value
		} else if c.Release != "" {
			item.ETA = EffectiveETA{Source: "release", Text: c.Release}
		} else if state.State != Done && !parent {
			if text := suggestionText(c.Suggested); text != "" {
				item.ETA = EffectiveETA{Source: "release", Text: text}
			} else if text := d.queueLanding(e.Project, c, now); text != "" {
				item.ETA = EffectiveETA{Source: "release", Text: text}
			} else {
				item.ETA = EffectiveETA{Source: "none"}
			}
		} else {
			item.ETA = EffectiveETA{Source: "none"}
		}
		item.Passed = Passed(item.ETA.At, state.State, now)
		if at := d.reported[c.ID]; at.After(g.etaSetAt) {
			g.etaSetAt = at
		}
		if note, ok := d.notes[c.ID]; ok && state.State != Done {
			item.Note, item.NoteBy = note.Note, note.By
			if note.At != nil {
				at := note.At.UTC()
				item.NoteAt = &at
			}
		}
		if v, ok := d.deliveries[c.ID]; ok && state.State != Done {
			delivery := v
			item.Delivery = &delivery
		}
		for _, l := range leaves {
			if l.Hours > 0 {
				item.Coverage.EstimatedLeaves++
				item.Weight += l.Hours
			}
			item.Coverage.TotalLeaves++
			if state.State == Done && l.Done && l.Merged != nil {
				if at, err := time.Parse(time.RFC3339Nano, *l.Merged); err == nil && (item.MergedAt == nil || at.After(*item.MergedAt)) {
					at = at.UTC()
					item.MergedAt = &at
				}
			}
			if seen[l.ID] {
				continue
			}
			seen[l.ID] = true
			all = append(all, LeafEstimate{l.ID, l.Hours})
			if l.Done {
				doneLeaves[l.ID] = true
			} else {
				open[l.ID] = l.Hours
			}
		}
		switch {
		case item.Coverage.EstimatedLeaves == 0:
			progress.ItemsWithoutEstimate++
		case item.Coverage.EstimatedLeaves < item.Coverage.TotalLeaves:
			progress.ItemsPartiallyEstimated++
		}
		item.Weight = math.Round(item.Weight*10) / 10
		g.Items = append(g.Items, item)
	}
	g.Counts.Tickets = len(all)
	progress.Pct, progress.Basis = EpicProgress(all, doneLeaves)
	progress.ComputedPct = progress.Pct
	progress.RemainingHours, progress.RemainingUnestimated = remainingWork(open)
	// Done follows the epic's own status. Every child being done does not hide
	// an open epic, and does not drop its entry checks.
	done := e.Category == "done" || e.Category == "delivered" || e.Category == "accepted"
	// LANDS: the latest worker ETA over the open items, else the epic's target
	// release, else its suggested release. Item text does not roll up.
	value := EffectiveETA{Source: "none"}
	for _, item := range g.Items {
		if item.State == Done {
			continue
		}
		if item.ETA.At == nil {
			value.Partial = true
			continue
		}
		if value.At == nil || item.ETA.At.After(*value.At) {
			partial := value.Partial
			value = item.ETA
			value.Source, value.Partial = "outcomes", partial
		}
	}
	if value.At == nil {
		value = EffectiveETA{Source: "none"}
		if label := strings.TrimSpace(fieldLabel(e.Fields, "release")); label != "" {
			value = EffectiveETA{Source: "release", Text: label}
		} else if text := suggestionText(fieldLabel(e.Fields, "suggested_release")); text != "" {
			value = EffectiveETA{Source: "release", Text: text}
		}
	}
	state := Queued
	if done {
		state = Done
	}
	passed := Passed(value.At, state, now)
	// Next: the first open item by ETA (then position), never a done one.
	next := g.Items[:0:0]
	for _, item := range g.Items {
		if item.State != Done && !item.HandedOver {
			next = append(next, item)
		}
	}
	sort.SliceStable(next, func(i, j int) bool {
		a, b := next[i].ETA.At, next[j].ETA.At
		if (a == nil) != (b == nil) {
			return a != nil
		}
		return a != nil && a.Before(*b)
	})
	if len(next) > 0 {
		g.NextStep = &Step{ID: stepID("item-" + next[0].Ref), Text: clip(next[0].Label, 200), State: "now"}
	}
	if moving {
		g.movingProjects = []string{e.Project}
	}
	g.Progress, g.ETA, g.Passed, g.States, g.Done = &progress, &value, &passed, &states, &done
	g.holds, g.blockers = d.holds[e.ID], d.blockers[e.ID]
	return g
}

func fieldLabel(fields map[string]any, key string) string {
	value, ok := fields[key]
	if !ok {
		if classic, isMap := fields["classic"].(map[string]any); isMap {
			value = classic[key]
		}
	}
	switch v := value.(type) {
	case string:
		return clip(strings.TrimSpace(v), 40)
	case map[string]any:
		for _, name := range []string{"label", "name"} {
			if s, ok := v[name].(string); ok {
				return clip(strings.TrimSpace(s), 40)
			}
		}
	}
	return ""
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// stepID keeps the GoalStep id grammar: lower-case letters, digits, dots and
// dashes, at most 32 characters.
func stepID(raw string) string {
	b := strings.Builder{}
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	id := strings.Trim(b.String(), "-.")
	if len(id) > epicStepIDSize {
		id = strings.TrimRight(id[:epicStepIDSize], "-.")
	}
	if id == "" {
		id = "step"
	}
	return id
}

// EpicNextStatus is the status an epic's next entry checks guard, as the
// ticket's checklist reads it: Ready from Open, Backlog or Blocked, QA from
// In progress, Done from QA. Done and closed epics have none.
func EpicNextStatus(category, state string) string {
	switch {
	case workorders.EntryState(state) == "qa":
		return "done"
	case category == "in_progress":
		return "qa"
	case category == "open" || category == "blocked":
		return "in_progress"
	}
	return ""
}

// attachEpicSteps fills "what's missing" for the epics on the page: the
// next status's failing entry checks in Definitions order, then holds and
// blockers. Admission's no_active_build check is left out: it takes the
// builder fence, which belongs to status and queue writes, not to a list read.
func attachEpicSteps(ctx context.Context, tx pgx.Tx, p tenant.Principal, items []GoalView, nodes map[string]epicNode) error {
	definitions := map[string]*workorders.EntryDefinition{}
	for i := range items {
		g := &items[i]
		e, ok := nodes[g.ID]
		if !ok || g.Done != nil && *g.Done {
			continue
		}
		texts := []string{}
		if next := EpicNextStatus(e.Category, e.State); next != "" {
			definition, known := definitions[e.Project]
			if !known {
				project := e.Project
				var err error
				if definition, err = workorders.EntryDefinitionTx(ctx, tx, &project, true); err != nil {
					return err
				}
				definitions[e.Project] = definition
			}
			if definition != nil && len(definition.Checks[next]) > 0 {
				ticket, err := workorders.EntryTicketTx(ctx, tx, e.ID)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				scoped := *definition
				scoped.Checks = map[string][]workorders.EntryRule{next: slices.DeleteFunc(slices.Clone(definition.Checks[next]), func(r workorders.EntryRule) bool { return r.ID == "no_active_build" })}
				block, err := workorders.EvaluateEntryTx(ctx, tx, p, ticket, &scoped, next)
				if err != nil {
					return err
				}
				for _, r := range block.Checks {
					if !r.Applicable || r.Passed {
						continue
					}
					text := strings.TrimSpace(r.Label)
					if r.Owner != nil && strings.HasPrefix(r.ID, "manual:") {
						text = "Waiting for " + r.Owner.Name + ": " + text
					}
					texts = append(texts, text)
				}
			}
		}
		for _, h := range g.holds {
			text := h.Key + " on hold"
			if reason := strings.TrimSpace(h.Reason); reason != "" {
				text += ": " + reason
			}
			texts = append(texts, text)
		}
		for _, b := range g.blockers {
			texts = append(texts, strings.TrimSpace("Blocked by "+b.Key+" "+b.Title))
		}
		if len(g.blockers) == 0 {
			if named := blockerText(e.Fields); named != "" {
				texts = append(texts, "Blocked: "+named)
			} else if e.Category == "blocked" {
				texts = append(texts, "Blocked")
			}
		}
		if len(texts) > maxEpicSteps {
			rest := len(texts) - maxEpicSteps + 1
			texts = append(texts[:maxEpicSteps-1], strconv.Itoa(rest)+" more")
		}
		used := map[string]bool{}
		for j, text := range texts {
			id := stepID(text)
			for n := 2; used[id]; n++ {
				id = stepID(text[:min(len(text), 24)] + "-" + strconv.Itoa(n))
			}
			used[id] = true
			state := "later"
			if j == 0 {
				state = "now"
			}
			g.Steps = append(g.Steps, Step{ID: id, Text: clip(text, 200), State: state})
		}
		if g.NextStep == nil && len(g.Steps) > 0 {
			first := g.Steps[0]
			g.NextStep = &first
		}
	}
	return nil
}

func blockerText(fields map[string]any) string {
	for _, key := range []string{"blocker", "blocked_by"} {
		if v, ok := fields[key].(string); ok && strings.TrimSpace(v) != "" && !strings.EqualFold(strings.TrimSpace(v), "unnamed") {
			return clip(strings.TrimSpace(v), 160)
		}
	}
	return ""
}

// redactEpicSecrets applies the live-feed rule before search, author lookup
// and step text. A missing permission drops the field; counts stay.
func redactEpicSecrets(goals []GoalView, check authz.ProjectCheck) {
	for i := range goals {
		g := &goals[i]
		project := ""
		if g.ProjectID != nil {
			project = *g.ProjectID
		}
		harness := check("harness.read", project)
		named := harness || check("members.read", project)
		if !harness {
			for j := range g.Items {
				g.Items[j].Note = ""
				g.Items[j].NoteAt = nil
				g.Items[j].SessionIDs = []string{}
			}
		}
		if !named {
			for j := range g.Items {
				g.Items[j].NoteBy, g.Items[j].NoteByName, g.Items[j].ETA.By = "", "", ""
			}
			if g.ETA != nil {
				g.ETA.By = ""
			}
		}
		if !check("delivery.read", project) {
			for j := range g.Items {
				g.Items[j].Delivery = nil
			}
			g.holds = nil
		}
	}
}

// suggestionText is a suggested release as the Goals ETA shows it. The work
// list draws the tilde beside the label; this text is the whole label.
func suggestionText(label string) string {
	label = strings.TrimSpace(label)
	if label == "" || label == "—" {
		return ""
	}
	if strings.HasPrefix(label, "~") {
		return clip(label, 40)
	}
	return clip("~ "+label, 40)
}

type releaseGap struct {
	hours  float64
	latest time.Time
}

type queueWait struct {
	hours float64
	ok    bool
}

// epicReleaseCadence is the product release cadence. Tests replace it; the
// embedded history is empty until a build writes it.
var epicReleaseCadence = func() (releaseGap, bool) {
	h, err := releasehistory.Embedded()
	if err != nil {
		return releaseGap{}, false
	}
	return cadenceFrom(h.Releases)
}

func cadenceFrom(releases []releasehistory.Release) (releaseGap, bool) {
	seen := map[int64]bool{}
	times := []time.Time{}
	for _, r := range releases {
		if r.State != releasehistory.StatePublished {
			continue
		}
		at := r.PublishedAt
		if at == nil {
			at = r.TaggedAt
		}
		if at == nil {
			continue
		}
		ms := at.UTC().UnixMilli()
		if seen[ms] {
			continue
		}
		seen[ms] = true
		times = append(times, at.UTC())
	}
	sort.Slice(times, func(i, j int) bool { return times[i].After(times[j]) })
	if len(times) > 10 {
		times = times[:10]
	}
	if len(times) < 2 {
		return releaseGap{}, false
	}
	gaps := make([]float64, len(times)-1)
	for i := 1; i < len(times); i++ {
		gaps[i-1] = times[i-1].Sub(times[i]).Hours()
	}
	sort.Float64s(gaps)
	mid := len(gaps) / 2
	hours := gaps[mid]
	if len(gaps)%2 == 0 {
		hours = (gaps[mid-1] + gaps[mid]) / 2
	}
	if hours <= 0 {
		return releaseGap{}, false
	}
	return releaseGap{hours: hours, latest: times[0]}, true
}

func normaliseWorkState(state string) string {
	state = strings.ToLower(strings.TrimSpace(state))
	state = strings.ReplaceAll(state, " ", "_")
	return strings.ReplaceAll(state, "-", "_")
}

func queueEligible(state string) bool {
	switch normaliseWorkState(state) {
	case "new", "open", "backlog":
		return true
	}
	return false
}

// queueLandingText places queued work on the release cadence: the wait, the
// estimate and an hour of review. The text keeps the closed ETA source
// `release`. The wait is the canonical expected start when the queue has one,
// otherwise the earlier estimates in the same partition. An untargeted pool
// divides that sum by the parallel-run capacity and stays blank when the
// capacity is unknown.
func queueLandingText(state string, estimate, wait float64, waitOK bool, cadence releaseGap, now time.Time) string {
	if !waitOK || estimate <= 0 || cadence.hours <= 0 || !queueEligible(state) {
		return ""
	}
	ready := now.Add(time.Duration((wait + estimate + 1) * float64(time.Hour)))
	interval := time.Duration(cadence.hours * float64(time.Hour))
	if interval <= 0 {
		return ""
	}
	steps := int(math.Floor(float64(now.Sub(cadence.latest))/float64(interval))) + 1
	if steps < 1 {
		steps = 1
	}
	next := cadence.latest.Add(time.Duration(steps) * interval)
	offset := int(math.Ceil(float64(ready.Sub(next)) / float64(interval)))
	if offset < 0 {
		offset = 0
	}
	if offset == 0 {
		return "~ Next"
	}
	return "~ Next +" + strconv.Itoa(offset)
}

func (d *epicPageData) queueLanding(project string, c epicChild, now time.Time) string {
	if !d.cadenceOK || !d.mayRelease[project] {
		return ""
	}
	wait, ok := d.queue[c.ID]
	if !ok {
		return ""
	}
	return queueLandingText(c.State, c.Estimate, wait.hours, wait.ok, d.cadence, now)
}

const queueLandingColumns = `SELECT q.queue_node_id::text, coalesce(q.queue_target_agent_id::text,''), q.queue_target_agent_id IS NOT NULL, q.queue_position,
 CASE WHEN jsonb_typeof(q.fields->'estimate_hours')='number' THEN (q.fields->>'estimate_hours')::float8 ELSE 0 END
 FROM queue_heads q `

// Same advisory total as the work-queue list with an empty project filter
// (internal/agentruns/queue_list.go): fresh available accounts, one slot count
// per harness and quota pool. The epic wait sums the whole untargeted
// partition, so the divisor is that unfiltered total.
const queueParallelSQL = `SELECT coalesce(sum(slots),0)::int FROM (
 SELECT max(a.max_parallel_runs) slots FROM agent_accounts a
 WHERE a.state='available' AND a.last_probe_ok AND a.last_probe_at>clock_timestamp()-interval '2 minutes'
 AND ($1='' OR aeon_account_use_allowed_for_project(a.id,NULLIF($1,'')::uuid))
 AND EXISTS(SELECT 1 FROM model_profiles m WHERE m.enabled AND m.harness=a.harness AND aeon_account_allows_profile(a.harness,a.allowed_model_profile_ids,m.id))
 GROUP BY a.harness,coalesce(nullif(a.quota_pool_fingerprint,''),a.id::text)) pools`

type queueLandingRow struct {
	id, agent string
	targeted  bool
	position  int
	hours     float64
}

// attachQueueLanding reads the canonical queue ranking for leaves that still
// have no worker ETA, target release or stored suggestion. Release cadence
// needs releases.read. A canonical expected start wins over the estimate sum.
// A truncated partition is ignored. Unknown parallel capacity stays blank.
func (d *epicPageData) attachQueueLanding(ctx context.Context, tx pgx.Tx, nodes []epicNode, now time.Time, check authz.ProjectCheck) error {
	ids := []string{}
	for _, e := range nodes {
		if !check("releases.read", e.Project) {
			continue
		}
		d.mayRelease[e.Project] = true
		for _, c := range d.children[e.ID] {
			if c.Release != "" || strings.TrimSpace(c.Suggested) != "" || d.keysTotal[c.ID] > 0 || c.Estimate <= 0 || !queueEligible(c.State) {
				continue
			}
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	gap, ok := epicReleaseCadence()
	if !ok {
		return nil
	}
	d.cadence, d.cadenceOK = gap, true
	loaded, err := loadEpicQueue(ctx, tx, ids)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, id := range ids {
		q := loaded[id]
		if q == nil || q.ExpectedStart == nil {
			continue
		}
		hours := q.ExpectedStart.Sub(now).Hours()
		if math.IsNaN(hours) || math.IsInf(hours, 0) {
			continue
		}
		if hours < 0 {
			hours = 0
		}
		d.queue[id] = queueWait{hours: hours, ok: true}
		expected[id] = true
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	// Targeted partitions stay on their own bound. Pulling the general pool
	// into the same limit would drop a targeted suggestion when the pool is long.
	targeted, truncated, err := readQueueLanding(ctx, tx, workqueue.CTE+queueLandingColumns+`
 WHERE q.queue_target_agent_id IS NOT NULL
 AND (q.queue_node_id = ANY($1::uuid[])
  OR q.queue_target_agent_id IN (SELECT h.queue_target_agent_id FROM queue_heads h WHERE h.queue_node_id = ANY($1::uuid[]) AND h.queue_target_agent_id IS NOT NULL))
 LIMIT 501`, ids)
	if err != nil {
		return err
	}
	if !truncated {
		for _, part := range queuePartitions(targeted, true) {
			d.scoreQueuePartition(part, want, expected, 1)
		}
	}
	shared, sharedCut, err := readQueueLanding(ctx, tx, workqueue.CTE+queueLandingColumns+`
 WHERE q.queue_target_agent_id IS NULL
 AND EXISTS (SELECT 1 FROM queue_heads h WHERE h.queue_node_id = ANY($1::uuid[]) AND h.queue_target_agent_id IS NULL)
 LIMIT 501`, ids)
	if err != nil {
		return err
	}
	if sharedCut {
		return nil
	}
	needsCapacity := false
	for _, row := range shared {
		if want[row.id] && !expected[row.id] {
			needsCapacity = true
			break
		}
	}
	if !needsCapacity {
		return nil
	}
	var parallel int
	if err := tx.QueryRow(ctx, queueParallelSQL, "").Scan(&parallel); err != nil {
		return err
	}
	// No fresh parallel-run count: do not invent a clock for the general pool.
	if parallel <= 0 {
		return nil
	}
	for _, part := range queuePartitions(shared, false) {
		d.scoreQueuePartition(part, want, expected, parallel)
	}
	return nil
}

// loadEpicQueue reads workqueue.Load in bounded batches. WaitForRuns refuses
// more than 4096 runs, and a page of epic leaves can be larger than that.
func loadEpicQueue(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*workqueue.Queued, error) {
	out := map[string]*workqueue.Queued{}
	const batch = 4096
	for start := 0; start < len(ids); start += batch {
		end := start + batch
		if end > len(ids) {
			end = len(ids)
		}
		part, err := workqueue.Load(ctx, tx, ids[start:end])
		if err != nil {
			return nil, err
		}
		for id, q := range part {
			out[id] = q
		}
	}
	return out, nil
}

func readQueueLanding(ctx context.Context, tx pgx.Tx, query string, ids []string) ([]queueLandingRow, bool, error) {
	rows, err := tx.Query(ctx, query, ids)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	found := []queueLandingRow{}
	for rows.Next() {
		var row queueLandingRow
		if err := rows.Scan(&row.id, &row.agent, &row.targeted, &row.position, &row.hours); err != nil {
			return nil, false, err
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return found, len(found) > 500, nil
}

func queuePartitions(rows []queueLandingRow, targeted bool) map[string][]queueLandingRow {
	out := map[string][]queueLandingRow{}
	for _, row := range rows {
		if row.targeted != targeted || targeted && row.agent == "" {
			continue
		}
		key := ""
		if targeted {
			key = row.agent
		}
		out[key] = append(out[key], row)
	}
	return out
}

// scoreQueuePartition sums earlier estimates and divides by parallel runs.
// parallel is 1 for a targeted agent. A non-positive count is not a guess.
func (d *epicPageData) scoreQueuePartition(part []queueLandingRow, want, expected map[string]bool, parallel int) {
	if parallel <= 0 {
		return
	}
	sort.Slice(part, func(i, j int) bool { return part[i].position < part[j].position })
	for i, row := range part {
		if !want[row.id] || expected[row.id] {
			continue
		}
		ok := true
		var sum float64
		for _, earlier := range part[:i] {
			if earlier.hours <= 0 || math.IsNaN(earlier.hours) || math.IsInf(earlier.hours, 0) {
				ok = false
				break
			}
			sum += earlier.hours
		}
		if !ok {
			d.queue[row.id] = queueWait{}
			continue
		}
		d.queue[row.id] = queueWait{hours: sum / float64(parallel), ok: true}
	}
}
