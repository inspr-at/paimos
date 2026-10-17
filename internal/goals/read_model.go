// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Counts struct {
	Outcomes int `json:"outcomes"`
	Tickets  int `json:"tickets"`
}
type StateCounts struct {
	Moving int `json:"moving"`
	Queued int `json:"queued"`
	Paused int `json:"paused"`
	Done   int `json:"done"`
}
type ProjectBadge struct {
	ID     string   `json:"id"`
	Key    string   `json:"key"`
	Name   string   `json:"name"`
	Groups []string `json:"group_ids"`
}
type Waits struct {
	Count       int      `json:"count"`
	QuestionIDs []string `json:"question_ids"`
}
type HandedOver struct {
	OutcomeID string `json:"outcome_id"`
	Label     string `json:"label"`
	Detail    string `json:"detail"`
	At        string `json:"at"`
	By        string `json:"by"`
}

type GoalProgress struct {
	OverrideNote            string `json:"override_note,omitempty"`
	Pct                     int    `json:"pct"`
	ComputedPct             int    `json:"computed_pct"`
	Source                  string `json:"source"`
	Basis                   string `json:"basis"`
	Total                   int    `json:"total"`
	Done                    int    `json:"done"`
	ItemsWithoutEstimate    int    `json:"items_without_estimate"`
	ItemsPartiallyEstimated int    `json:"items_partially_estimated"`
	OverrideBy              string `json:"override_by,omitempty"`
	OverrideAt              string `json:"override_at,omitempty"`
}
type OutcomeView struct {
	ID       string   `json:"id"`
	Ref      string   `json:"ref"`
	Label    string   `json:"label"`
	Note     string   `json:"note"`
	Position Position `json:"position"`
	Keys     []string `json:"keys"`
	EffectiveState
	ETA        EffectiveETA `json:"eta"`
	Passed     bool         `json:"passed"`
	Coverage   Coverage     `json:"coverage"`
	Weight     float64      `json:"weight"`
	HandedOver bool         `json:"handed_over"`
}
type GoalView struct {
	ID         string         `json:"id"`
	Key        string         `json:"key"`
	Title      string         `json:"title"`
	Kind       string         `json:"kind"`
	ProjectID  *string        `json:"project_id"`
	Workspace  bool           `json:"workspace"`
	Expansion  string         `json:"expansion"`
	Counts     Counts         `json:"counts"`
	Items      []OutcomeView  `json:"items,omitempty"`
	Steps      []Step         `json:"steps"`
	NextStep   *Step          `json:"next_step"`
	Projects   []ProjectBadge `json:"projects"`
	Waits      Waits          `json:"waits"`
	Progress   *GoalProgress  `json:"progress,omitempty"`
	ETA        *EffectiveETA  `json:"eta,omitempty"`
	Passed     *bool          `json:"passed,omitempty"`
	States     *StateCounts   `json:"states,omitempty"`
	Done       *bool          `json:"done,omitempty"`
	Stall      *Stall         `json:"stall,omitempty"`
	HandedOver *HandedOver    `json:"handed_over,omitempty"`
	Watermark
	created        time.Time
	movingProjects []string
	targets        []string
	node           storedNode
	budget         listBudget
}

type leafFact struct {
	ID                                  string
	Done                                bool
	Hours                               float64
	Sessions                            []string
	PR, Review, Paused, Queued, Blocked bool
}

// These predicates are shared by detail/list/revalidation. Work membership and
// done buckets come from aeon_work_scope; ETA facts use eta.AggregateSQL.
const leafFactsSQL = `WITH scope AS MATERIALIZED (SELECT * FROM aeon_work_scope($1::uuid[])), ` + leafFactsProjectionSQL

const leafFactsProjectionSQL = `leaves AS MATERIALIZED (SELECT DISTINCT id,fields,bucket,project_id FROM scope WHERE is_leaf AND bucket NOT IN ('archived','cancelled')),
 leaf_facts AS MATERIALIZED (SELECT l.id,l.bucket='done' AS done,
 CASE WHEN jsonb_typeof(l.fields->'estimate_hours')='number' THEN CASE WHEN (l.fields->>'estimate_hours')::numeric>0 AND (l.fields->>'estimate_hours')::numeric<=200 THEN (l.fields->>'estimate_hours')::float8 ELSE 0 END ELSE 0 END AS hours,
 ARRAY(SELECT h.id::text FROM harness_sessions h WHERE h.ticket_node_id=l.id AND h.project_id=l.project_id AND h.phase='working' AND h.stopped_at IS NULL AND h.archived_at IS NULL AND h.role IN ('worker','coordinator') ORDER BY h.id LIMIT 201) AS sessions,
 EXISTS(SELECT 1 FROM delivery_items d WHERE d.ticket_node_id=l.id AND d.state<>'merged' AND coalesce(d.observation->>'merged','false')<>'true'
 AND NOT EXISTS(SELECT 1 FROM delivery_metric_pulls p WHERE p.repository=d.repository AND p.pull_request=d.pull_request AND p.merged_at IS NOT NULL)
 AND ((d.pull_request IS NOT NULL AND d.observation->>'open'='true') OR (d.branch LIKE 'work/%' AND substring(lower(d.branch) from '([a-z][a-z0-9_]*-[0-9]+)')=lower(n.key) AND d.head_sha<>'' AND d.head_sha<>coalesce(d.observation->>'base_sha',''))))
 OR EXISTS(SELECT 1 FROM delivery_metric_pulls p JOIN delivery_metric_sources ds ON ds.tenant_id=p.tenant_id AND ds.repository=p.repository AND ds.project_id=l.project_id WHERE p.closed_at IS NULL AND p.merged_at IS NULL AND p.head_branch LIKE 'work/%' AND substring(lower(p.head_branch) from '([a-z][a-z0-9_]*-[0-9]+)')=lower(n.key)) AS pr,
 EXISTS(SELECT 1 FROM review_runs r WHERE r.ticket_node_id=l.id AND r.submitted_at IS NULL AND r.expires_at>$2) OR EXISTS(SELECT 1 FROM delivery_review_rounds r WHERE r.ticket_node_id=l.id AND r.state IN ('queued','claimed')) AS review,
 EXISTS(SELECT 1 FROM harness_sessions h WHERE h.ticket_node_id=l.id AND h.project_id=l.project_id AND h.archived_at IS NULL AND h.stopped_at IS NOT NULL AND h.stop_reason='paused' AND h.pause_record->>'state' IN ('paused','resume_requested') AND NOT EXISTS(SELECT 1 FROM harness_sessions newer WHERE newer.ticket_node_id=h.ticket_node_id AND newer.project_id=h.project_id AND newer.archived_at IS NULL AND newer.stopped_at IS NOT NULL AND (newer.stopped_at,newer.id)>(h.stopped_at,h.id)) AND NOT EXISTS(SELECT 1 FROM harness_sessions live WHERE live.ticket_node_id=l.id AND live.stopped_at IS NULL AND live.archived_at IS NULL))
 OR EXISTS(SELECT 1 FROM delivery_items d WHERE d.ticket_node_id=l.id AND d.state='held')
 OR EXISTS(SELECT 1 FROM desk_askers a JOIN desk_questions q ON q.node_id=a.question_id WHERE q.state='open' AND a.input->>'meanwhile' IN ('parked','paused','stopped') AND a.input->'blocked_node_ids' ? l.id::text) AS paused,
 EXISTS(SELECT 1 FROM agent_runs a WHERE a.queue_node_id=l.id AND a.status='queued' AND replace(replace(lower(btrim(n.state)),' ','_'),'-','_') IN ('new','open','backlog','blocked')) AS queued,
 lower(n.state)='blocked' AS blocked
 FROM leaves l JOIN nodes n ON n.id=l.id)
 SELECT s.root::text,f.id::text,f.done,f.hours,f.sessions,f.pr,f.review,f.paused,f.queued,f.blocked
 FROM scope s JOIN leaf_facts f ON f.id=s.id ORDER BY s.root,f.id`

func loadLeafFacts(ctx context.Context, tx pgx.Tx, roots []string, now time.Time) (map[string][]leafFact, error) {
	out := map[string][]leafFact{}
	if len(roots) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, leafFactsSQL, roots, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var root string
		var leaf leafFact
		if err := rows.Scan(&root, &leaf.ID, &leaf.Done, &leaf.Hours, &leaf.Sessions, &leaf.PR, &leaf.Review, &leaf.Paused, &leaf.Queued, &leaf.Blocked); err != nil {
			return nil, err
		}
		if len(leaf.Sessions) > 200 {
			return nil, fail(413, "outcome session bound exceeded")
		}
		out[root] = append(out[root], leaf)
	}
	return out, rows.Err()
}
func tooLarge(err error) bool {
	var pg *pgconn.PgError
	var bounded *apiFailure
	var validation *ValidationError
	return errors.As(err, &pg) && (pg.Code == "54000" || pg.Code == "57014") || errors.As(err, &bounded) && bounded.status == 413 || errors.As(err, &validation) && validation.Path == "items"
}

func baseGoal(n storedNode, w Watermark) (GoalView, error) {
	out := GoalView{ID: n.ID, Key: n.Key, Title: n.Title, Kind: n.Slug, Workspace: n.Project == "", Expansion: "full", Steps: []Step{}, Projects: []ProjectBadge{}, Waits: Waits{QuestionIDs: []string{}}, Watermark: w, node: n}
	if n.Project != "" {
		out.ProjectID = &n.Project
	}
	if steps, ok := n.Fields["steps"]; ok {
		raw, _ := json.Marshal(steps)
		if err := decodeStrict(raw, &out.Steps); err != nil {
			return out, err
		}
	}
	out.NextStep = NextStep(out.Steps)
	return out, nil
}

func readGoal(ctx context.Context, tx pgx.Tx, p tenant.Principal, n storedNode, now time.Time, w Watermark) (GoalView, error) {
	out, err := baseGoal(n, w)
	if err != nil {
		return out, err
	}
	if !IsGoalKind(n.Slug) {
		return out, pgx.ErrNoRows
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: n.Project}); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT created_at FROM nodes WHERE id=$1`, n.ID).Scan(&out.created); err != nil {
		return out, err
	}
	// Count visible rows before expansion. No hidden-outcome denominator or
	// fallback survives the savepoint when work traversal exceeds its budget.
	err = tx.QueryRow(ctx, `SELECT count(*)::int FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.parent_id=$1 AND k.slug='outcome' AND n.deleted_at IS NULL`, n.ID).Scan(&out.Counts.Outcomes)
	if err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT r.target_node_id)::int FROM node_relations r JOIN nodes o ON o.id=r.source_node_id JOIN node_kinds k ON k.tenant_id=o.tenant_id AND k.id=o.kind_id WHERE o.parent_id=$1 AND o.deleted_at IS NULL AND k.slug='outcome' AND r.type='serves'`, n.ID).Scan(&out.Counts.Tickets); err != nil {
		return out, err
	}
	if out.Counts.Outcomes > MaxOutcomes {
		out.Expansion = "too_large"
		return out, nil
	}
	nested, err := tx.Begin(ctx)
	if err != nil {
		return out, err
	}
	expanded, err := expandGoal(ctx, nested, p, out, now)
	if err != nil {
		if rollback := nested.Rollback(ctx); rollback != nil {
			return out, rollback
		}
		if tooLarge(err) {
			out.Expansion = "too_large"
			return out, nil
		}
		return out, err
	}
	history, err := loadReconstruction(ctx, nested, n.ID, now)
	if err == nil {
		var since time.Time
		_, since, err = history.project(ctx, now, "24h")
		if err == nil {
			stall := DeriveStall(expanded.States.Moving, since, now)
			expanded.Stall = &stall
		}
	}
	if err != nil {
		if rollback := nested.Rollback(ctx); rollback != nil {
			return out, rollback
		}
		if tooLarge(err) {
			out.Expansion = "too_large"
			return out, nil
		}
		return out, err
	}
	if err := nested.Commit(ctx); err != nil {
		return out, err
	}
	return expanded, nil
}
func expandGoal(ctx context.Context, tx pgx.Tx, p tenant.Principal, out GoalView, now time.Time) (GoalView, error) {
	children, err := childNodes(ctx, tx, out.ID)
	if err != nil {
		return out, err
	}
	roots := []string{}
	for _, n := range children {
		roots = append(roots, n.Targets...)
	}
	// Reference-only serves ends remain visible keys/badges but never work roots.
	workRoots, err := filterWorkRoots(ctx, tx, roots)
	if err != nil {
		return out, err
	}
	facts, err := loadLeafFacts(ctx, tx, workRoots, now)
	if err != nil {
		return out, err
	}
	etas, err := effectiveETAs(ctx, tx, children)
	if err != nil {
		return out, err
	}
	keys, err := targetIdentities(ctx, tx, roots)
	if err != nil {
		return out, err
	}
	out, movingTargets := deriveGoal(out, children, facts, etas, keys, now)
	out.Projects, err = projectBadges(ctx, tx, out.node.Project, out.targets)
	if err != nil {
		return out, err
	}
	if out.States.Moving > 0 {
		badges, e := projectBadges(ctx, tx, out.node.Project, movingTargets)
		if e != nil {
			return out, e
		}
		for _, b := range badges {
			out.movingProjects = append(out.movingProjects, b.ID)
		}
	}
	out.Waits, err = goalWaits(ctx, tx, p, out.ID, out.targets)
	return out, err
}

// deriveGoal is shared by detail, revalidation and the page-wide projection.
// Database reads and visibility checks are completed before deriving values.
func deriveGoal(out GoalView, children []storedNode, facts map[string][]leafFact, etas map[string]EffectiveETA, targetKeys map[string]string, now time.Time) (GoalView, []string) {
	sort.Slice(children, func(i, j int) bool {
		a, _ := Position(children[i].Position).decimal()
		b, _ := Position(children[j].Position).decimal()
		if a.Cmp(b) == 0 {
			return children[i].ID < children[j].ID
		}
		return a.Cmp(b) < 0
	})
	roots := []string{}
	for _, n := range children {
		roots = append(roots, n.Targets...)
	}
	movingTargets := []string{}
	inputs := []ProgressOutcome{}
	out.Items = []OutcomeView{}
	states := StateCounts{}
	for _, n := range children {
		f := StateFacts{HandedOver: fieldString(n, "handed_over_at") != "", StateSet: State(fieldString(n, "state_set"))}
		leaves := []LeafEstimate{}
		seen := map[string]bool{}
		for _, root := range n.Targets {
			for _, l := range facts[root] {
				if seen[l.ID] {
					continue
				}
				seen[l.ID] = true
				f.TotalLeaves++
				if l.Done {
					f.DoneLeaves++
				}
				f.SessionIDs = append(f.SessionIDs, l.Sessions...)
				f.OpenPR = f.OpenPR || l.PR
				f.Review = f.Review || l.Review
				f.Paused = f.Paused || l.Paused
				f.Queued = f.Queued || l.Queued
				f.Blocked = f.Blocked || l.Blocked
				leaves = append(leaves, LeafEstimate{l.ID, l.Hours})
			}
		}
		if f.HandedOver {
			out.HandedOver = &HandedOver{OutcomeID: n.ID, Label: n.Title, Detail: fieldString(n, "handed_over_detail"), At: fieldString(n, "handed_over_at"), By: fieldString(n, "handed_over_by")}
		}
		state := DeriveState(f)
		switch state.State {
		case Moving:
			states.Moving++
			movingTargets = append(movingTargets, n.Targets...)
		case Queued:
			states.Queued++
		case Paused:
			states.Paused++
		case Done:
			states.Done++
		}
		keys := []string{}
		for _, id := range n.Targets {
			if key, ok := targetKeys[id]; ok {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		value := etas[n.ID]
		out.Items = append(out.Items, OutcomeView{ID: n.ID, Ref: fieldString(n, "outcome_ref"), Label: n.Title, Note: fieldString(n, "note"), Position: Position(n.Position), Keys: keys, EffectiveState: state, ETA: value, Passed: Passed(value.At, state.State, now), HandedOver: f.HandedOver})
		inputs = append(inputs, ProgressOutcome{State: state.State, Leaves: leaves})
	}
	rollup := ComputeRollup(inputs)
	for i := range out.Items {
		out.Items[i].Coverage = rollup.Outcomes[i].Coverage
		out.Items[i].Weight = rollup.Outcomes[i].Hours
	}
	progress := GoalProgress{Pct: rollup.Pct, ComputedPct: rollup.Pct, Source: "computed", Basis: rollup.Basis, Total: rollup.Total, Done: rollup.Done, ItemsWithoutEstimate: rollup.ItemsWithoutEstimate, ItemsPartiallyEstimated: rollup.ItemsPartiallyEstimated}
	if pct, ok := out.node.Fields["progress_override_pct"].(float64); ok {
		progress.Pct = int(pct)
		progress.Source = "override"
		progress.OverrideBy = fieldString(out.node, "progress_override_by")
		progress.OverrideNote = fieldString(out.node, "progress_override_note")
		progress.OverrideAt = fieldString(out.node, "progress_override_at")
	}
	done := len(children) > 0 && states.Done == len(children)
	value := authoredETA(out.node)
	if value.Source == "none" {
		for _, item := range out.Items {
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
				value.Source = "outcomes"
				value.Partial = partial
			}
		}
	}
	state := Queued
	if done {
		state = Done
	}
	passed := Passed(value.At, state, now)
	out.Progress = &progress
	out.ETA = &value
	out.Passed = &passed
	out.States = &states
	out.Done = &done
	out.targets = slices.Compact(slices.Sorted(slices.Values(roots)))
	return out, movingTargets
}

func filterWorkRoots(ctx context.Context, tx pgx.Tx, ids []string) ([]string, error) {
	out := []string{}
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=ANY($1::uuid[]) AND k.slug IN ('work','epic','ticket','task') AND n.deleted_at IS NULL ORDER BY n.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func targetIdentities(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	out := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT id::text,key FROM nodes WHERE id=ANY($1::uuid[]) AND deleted_at IS NULL ORDER BY key`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[id] = key
	}
	return out, rows.Err()
}
func projectBadges(ctx context.Context, tx pgx.Tx, home string, targets []string) ([]ProjectBadge, error) {
	out := []ProjectBadge{}
	rows, err := tx.Query(ctx, `SELECT p.id::text,coalesce(nullif(p.fields->>'project_key',''),p.key),p.title,ARRAY(SELECT group_id::text FROM project_group_members WHERE project_id=p.id ORDER BY group_id) FROM nodes p JOIN node_kinds k ON k.tenant_id=p.tenant_id AND k.id=p.kind_id WHERE p.kind_id=ANY(ARRAY(SELECT id FROM node_kinds WHERE slug='project')) AND k.slug='project' AND p.deleted_at IS NULL AND (p.id::text=$1 OR p.id IN (SELECT project_id FROM nodes WHERE id=ANY($2::uuid[]))) ORDER BY coalesce(nullif(p.fields->>'project_key',''),p.key),p.id`, home, targets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b ProjectBadge
		if err := rows.Scan(&b.ID, &b.Key, &b.Name, &b.Groups); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func goalWaits(ctx context.Context, tx pgx.Tx, p tenant.Principal, goal string, targets []string) (Waits, error) {
	out := Waits{QuestionIDs: []string{}}
	err := tx.QueryRow(ctx, `WITH visible AS MATERIALIZED (SELECT DISTINCT q.node_id FROM desk_questions q JOIN nodes n ON n.id=q.node_id JOIN desk_askers a ON a.question_id=q.node_id WHERE q.state='open' AND n.deleted_at IS NULL AND ($1 OR a.principal_id=$2::uuid) AND (a.ticket_id=ANY($3::uuid[]) OR EXISTS(SELECT 1 FROM node_relations r WHERE r.type='asks_for' AND r.source_node_id=a.comment_node_id AND r.target_node_id=$4))) SELECT count(*)::int,ARRAY(SELECT node_id::text FROM visible ORDER BY node_id LIMIT 3) FROM visible`, p.Kind == tenant.Person, p.ID, targets, goal).Scan(&out.Count, &out.QuestionIDs)
	return out, err
}
