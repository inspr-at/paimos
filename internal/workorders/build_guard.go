// SPDX-License-Identifier: AGPL-3.0-only
package workorders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// BuildConflict contains only public metadata selected under the caller's RLS.
// Private references, leases and repository observations never leave the store.
type BuildConflict struct {
	Reason    string         `json:"reason"`
	SessionID *string        `json:"session_id"`
	Label     *string        `json:"label"`
	StartedBy *BuildStarter  `json:"started_by"`
	Since     *time.Time     `json:"since"`
	Delivery  *BuildDelivery `json:"delivery,omitempty"`
}

type BuildStarter struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type BuildDelivery struct {
	Repository  string `json:"repository"`
	PullRequest *int64 `json:"pull_request"`
	Branch      string `json:"branch"`
}

// BuildCandidate is an exact delivery binding. A harness branch uses the
// ticket's linked repository; orders also pin the repository explicitly.
type BuildCandidate struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
}

// BuildAdmission carries a candidate into the shared predicate and its audit
// out. Admission never appends events: the mutation owner flushes Changes last.
type BuildAdmission struct {
	Candidate    *BuildCandidate
	Continuation *BuildDelivery
	ticket       string
}

func (a BuildAdmission) Changes(session, run string) []events.Change {
	if a.Continuation == nil {
		return nil
	}
	message := "continues delivery on " + a.Continuation.Branch
	if a.Continuation.PullRequest != nil {
		message = fmt.Sprintf("continues delivery PR #%d on %s", *a.Continuation.PullRequest, a.Continuation.Branch)
	}
	return []events.Change{
		{NodeID: &a.ticket, Type: "delivery.continued", After: map[string]any{"session_id": session, "run_id": run, "delivery": a.Continuation, "message": message}},
		{NodeID: &a.ticket, Type: "comment.created", After: map[string]any{"body_markdown": message}},
	}
}

func (a BuildAdmission) Record(ctx context.Context, tx pgx.Tx, p tenant.Principal, session, run string) error {
	for _, change := range a.Changes(session, run) {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}

func ValidateBuildCandidate(candidate *BuildCandidate) error {
	if candidate == nil {
		return nil
	}
	if candidate.Repository == "" || len(candidate.Repository) > 255 || strings.TrimSpace(candidate.Repository) != candidate.Repository || !utf8.ValidString(candidate.Repository) || strings.ContainsFunc(candidate.Repository, unicode.IsSpace) || strings.ContainsFunc(candidate.Repository, unicode.IsControl) || len(candidate.Branch) > 200 || !utf8.ValidString(candidate.Branch) || !strings.HasPrefix(candidate.Branch, "work/") || len(candidate.Branch) <= 5 || strings.ContainsFunc(candidate.Branch, unicode.IsSpace) || strings.ContainsFunc(candidate.Branch, unicode.IsControl) {
		return Fail(400, "build_delivery requires a repository and exact work/ branch within size limits")
	}
	return nil
}

// Orders persist this additive binding in their existing node fields. Read it
// under the caller's RLS and fences; neither brief text nor a run's untrusted
// trace is a repository/branch authority.
func OrderBuildAdmissionTx(ctx context.Context, tx pgx.Tx, order string) (BuildAdmission, error) {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT n.fields->'build_delivery' FROM work_orders w JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id WHERE w.node_id=$1 AND n.deleted_at IS NULL`, order).Scan(&raw)
	if err != nil {
		return BuildAdmission{}, err
	}
	return decodeBuildAdmission(raw)
}

func decodeBuildAdmission(raw json.RawMessage) (BuildAdmission, error) {
	var admission BuildAdmission
	if len(raw) > 2048 {
		return admission, Fail(400, "build_delivery exceeds size limit")
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &admission.Candidate); err != nil {
			return admission, Fail(400, "invalid build_delivery binding")
		}
	}
	return admission, ValidateBuildCandidate(admission.Candidate)
}

// Registration must name its actual branch even for an order-bound worker.
// An order's explicit repository binding takes precedence over project context.
func HarnessBuildAdmissionTx(ctx context.Context, tx pgx.Tx, branch, ticket, order, run *string) (BuildAdmission, error) {
	var admission BuildAdmission
	if branch == nil || *branch == "" {
		return admission, nil
	}
	if order == nil && run != nil {
		var id string
		if err := tx.QueryRow(ctx, `SELECT work_order_id::text FROM agent_runs WHERE id=$1`, *run).Scan(&id); err != nil {
			return admission, err
		}
		order = &id
	}
	if order == nil && ticket != nil {
		var id string
		err := tx.QueryRow(ctx, `SELECT node_id::text FROM work_orders WHERE node_id=$1`, *ticket).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return admission, err
		}
		if err == nil {
			order = &id
		}
	}
	if order != nil {
		var err error
		admission, err = OrderBuildAdmissionTx(ctx, tx, *order)
		if err != nil {
			return admission, err
		}
		if admission.Candidate != nil && admission.Candidate.Branch != *branch {
			return admission, Fail(409, "registration branch conflicts with work-order delivery binding")
		}
	}
	if admission.Candidate == nil {
		admission.Candidate = &BuildCandidate{Branch: *branch}
	}
	return admission, nil
}

func (e *BuildConflict) Error() string {
	if e.Delivery != nil {
		return "already built — ship it: unmerged delivery in " + e.Delivery.Repository
	}
	if e.SessionID == nil {
		return "build in flight: another builder is active outside the visible project"
	}
	starter := "an unavailable principal"
	if e.StartedBy != nil {
		starter = e.StartedBy.Name
	}
	return fmt.Sprintf("build in flight: %s, started by %s since %s (session %s)", *e.Label, starter, e.Since.UTC().Format(time.RFC3339), *e.SessionID)
}
func (e *BuildConflict) HTTPStatus() int   { return 409 }
func (e *BuildConflict) ErrorCode() string { return "build_in_flight" }
func (e *BuildConflict) ErrorDetails() map[string]any {
	out := map[string]any{"reason": e.Reason, "session_id": e.SessionID, "label": e.Label, "started_by": e.StartedBy, "since": e.Since}
	if e.Delivery != nil {
		out["delivery"] = e.Delivery
	}
	return out
}

// NormalizeParallelReason bounds the public audit text before store work.
// An explicitly empty reason is invalid, never an accidental opt-out.
func NormalizeParallelReason(reason *string) error {
	if reason == nil {
		return nil
	}
	if len(*reason) > 2000 || !utf8.ValidString(*reason) || utf8.RuneCountInString(*reason) > 500 || strings.ContainsFunc(*reason, unicode.IsControl) || strings.ContainsAny(*reason, "\u2028\u2029") {
		return Fail(400, "parallel_reason must be 1–500 characters without control characters")
	}
	*reason = strings.TrimSpace(*reason)
	if *reason == "" {
		return Fail(400, "parallel_reason must be nonempty")
	}
	return nil
}

const buildTicketIDSQL = `coalesce(b.queue_ticket,
 CASE WHEN nk.slug='work_order' AND pk.slug IN ('work','ticket','task') THEN n.parent_id END,
 CASE WHEN dk.slug='work_order' AND dpk.slug IN ('work','ticket','task') THEN direct.parent_id END,b.direct_ticket,n.id)`

const buildTicketJoinsSQL = ` LEFT JOIN nodes n ON n.id=b.node_id
 LEFT JOIN node_kinds nk ON nk.tenant_id=n.tenant_id AND nk.id=n.kind_id
 LEFT JOIN nodes parent ON parent.tenant_id=n.tenant_id AND parent.id=n.parent_id
 LEFT JOIN node_kinds pk ON pk.tenant_id=parent.tenant_id AND pk.id=parent.kind_id
 LEFT JOIN nodes direct ON direct.id=b.direct_ticket
 LEFT JOIN node_kinds dk ON dk.tenant_id=direct.tenant_id AND dk.id=direct.kind_id
 LEFT JOIN nodes direct_parent ON direct_parent.tenant_id=direct.tenant_id AND direct_parent.id=direct.parent_id
 LEFT JOIN node_kinds dpk ON dpk.tenant_id=direct_parent.tenant_id AND dpk.id=direct_parent.kind_id `

type buildNodeTarget struct {
	ID     string
	Origin bool
}

// Resolve only the named node and, for an order, its immediate parent. Keeping
// these indexed point reads separate avoids multiplying the nodes' RLS policy
// in a large join when a tenant has hundreds of branches.
func buildNodeTargetTx(ctx context.Context, tx pgx.Tx, id string) (buildNodeTarget, error) {
	var target buildNodeTarget
	var order bool
	var parent *string
	if err := tx.QueryRow(ctx, `SELECT n.id::text,k.slug='work_order',n.parent_id::text
      FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
      WHERE n.id=$1 AND n.deleted_at IS NULL`, id).Scan(&target.ID, &order, &parent); err != nil {
		return target, err
	}
	if !order || parent == nil {
		return target, nil
	}
	if err := tx.QueryRow(ctx, `SELECT k.slug IN ('work','ticket','task') FROM nodes n
      JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1`, *parent).Scan(&target.Origin); err != nil {
		return target, err
	}
	if target.Origin {
		target.ID = *parent
	}
	return target, nil
}

// BuildTicketTx resolves the at-most-three public bindings under caller RLS.
// Work-order children and their runs occupy the originating ticket's slot;
// standalone orders use an explicit ticket if supplied, otherwise their own
// slot. Conflicting originating ticket bindings fail closed.
// Callers hold tenant/tree fences, so the bounded point reads share one binding.
func BuildTicketTx(ctx context.Context, tx pgx.Tx, ticket, order, run *string) (*string, error) {
	cache := make(map[string]buildNodeTarget, 4)
	resolve := func(id string) (buildNodeTarget, error) {
		if target, ok := cache[id]; ok {
			return target, nil
		}
		target, err := buildNodeTargetTx(ctx, tx, id)
		if err == nil {
			cache[id] = target
		}
		return target, err
	}
	var explicit, result *string
	add := func(target buildNodeTarget, fallback bool) error {
		if fallback && !target.Origin && explicit != nil {
			target.ID = *explicit
		}
		if result != nil && *result != target.ID {
			return Fail(409, "ticket, work order and run binding conflict")
		}
		id := target.ID
		result = &id
		return nil
	}
	if ticket != nil {
		target, err := resolve(*ticket)
		if err != nil {
			return nil, err
		}
		id := target.ID
		explicit = &id
		if err = add(target, false); err != nil {
			return nil, err
		}
	}
	if order != nil {
		var node string
		if err := tx.QueryRow(ctx, `SELECT node_id::text FROM work_orders WHERE node_id=$1`, *order).Scan(&node); err != nil {
			return nil, err
		}
		target, err := resolve(node)
		if err != nil {
			return nil, err
		}
		if err = add(target, true); err != nil {
			return nil, err
		}
	}
	if run != nil {
		var node string
		var queueTicket *string
		if err := tx.QueryRow(ctx, `SELECT work_order_id::text,queue_node_id::text FROM agent_runs WHERE id=$1`, *run).Scan(&node, &queueTicket); err != nil {
			return nil, err
		}
		if queueTicket != nil {
			node = *queueTicket
		}
		target, err := resolve(node)
		if err != nil {
			return nil, err
		}
		if err = add(target, queueTicket == nil); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// The same resolution serves the existence probe and public metadata query.
// Also retain a legacy row's explicit ticket as a blocker if its bindings differ.
const activeBuildersSQL = `WITH builders AS (
 SELECT s.*,b.node_id AS binding_node,` + buildTicketIDSQL + ` AS build_ticket FROM harness_sessions s
 LEFT JOIN agent_runs r ON r.tenant_id=s.tenant_id AND r.id=s.run_id
 CROSS JOIN LATERAL (SELECT coalesce(r.work_order_id,s.work_order_id,s.ticket_node_id) AS node_id,r.queue_node_id AS queue_ticket,s.ticket_node_id AS direct_ticket) b
 ` + buildTicketJoinsSQL + `
 WHERE s.role='worker' AND s.stopped_at IS NULL AND s.id::text<>$2
) `

// RequireBuildAdmissionTx is the single builder/queue admission predicate.
// Callers hold tenant -> tree fences, authorize the current target inside the
// final mutation transaction, and append events last. The ticket advisory lock
// also serializes direct store callers: the lock and subsequent reads are
// separate statements, so a waiter sees its predecessor's committed session.
// except is only the generation being rebound/revived, never its parent.
// A parallel reason is accepted only with live person/coordinator authority;
// the admitting caller persists it and appends a ticket audit before commit.
// Optional admission data identifies an exact continuation branch in the
// linked repository. Active builders still block it; the mutation owner flushes
// its continuation audit only after all writes and resource locks are complete.
func RequireBuildAdmissionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, except string, parallelReason *string, admission ...*BuildAdmission) error {
	var result *BuildAdmission
	if len(admission) > 0 {
		result = admission[0]
	}
	if result != nil {
		result.Continuation, result.ticket = nil, ""
	}
	if err := NormalizeParallelReason(parallelReason); err != nil {
		return err
	}
	target, err := BuildTicketTx(ctx, tx, &ticket, nil, nil)
	if err != nil {
		return err
	}
	ticket = *target
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-builder:'||current_setting('aeon.tenant_id')||':'||$1::uuid::text,0))`, ticket); err != nil {
		return err
	}
	if parallelReason != nil {
		var projectID string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL`, ticket).Scan(&projectID); err != nil {
			return err
		}
		if p.Kind == tenant.Person {
			return authz.RequireTx(ctx, tx, p, "run.create", authz.Scope{ProjectID: projectID})
		}
		return authz.RequireQueueCoordinatorTx(ctx, tx, p, projectID)
	}
	// A moved ticket can retain a session in its old, now invisible project.
	// Probe existence with the established bounded internal visibility helper;
	// tenant RLS remains enforced and no hidden identity or time escapes.
	var active bool
	err = withBuildVisibilityTx(ctx, tx, func(step pgx.Tx) error {
		return step.QueryRow(ctx, activeBuildersSQL+`SELECT EXISTS(SELECT 1 FROM builders
		 WHERE ticket_node_id=$1 OR build_ticket=$1 OR binding_node=$1)`, ticket, except).Scan(&active)
	})
	if err != nil {
		return err
	}
	var conflict BuildConflict
	if active {
		conflict.Reason = "active_builder"
		var starterID, starterName, starterKind *string
		err = tx.QueryRow(ctx, activeBuildersSQL+`SELECT s.id::text,coalesce(s.display_label,s.harness),p.id::text,p.name,p.kind,s.created_at
		 FROM builders s LEFT JOIN principals p ON p.tenant_id=s.tenant_id AND p.id=coalesce(s.started_by_principal_id,s.owner_principal_id,s.agent_principal_id)
		 WHERE s.ticket_node_id=$1 OR s.build_ticket=$1 OR s.binding_node=$1
		 ORDER BY s.created_at,s.id LIMIT 1`, ticket, except).Scan(&conflict.SessionID, &conflict.Label, &starterID, &starterName, &starterKind, &conflict.Since)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if starterID != nil {
			conflict.StartedBy = &BuildStarter{ID: *starterID, Name: *starterName, Kind: *starterKind}
		}
		return &conflict
	}
	// Only the project's linked repository participates. The delivery projection
	// links PRs by ticket; repository PR facts can precede that projection and
	// link work/<slug> branches by an exact ticket key (1258 never matches 12580).
	// A merged repository fact supersedes an older unmerged projection.
	candidateRepository, candidateBranch := "", ""
	if result != nil && result.Candidate != nil {
		candidateRepository, candidateBranch = result.Candidate.Repository, result.Candidate.Branch
	}
	var delivery BuildDelivery
	err = tx.QueryRow(ctx, `WITH target AS (
	 SELECT n.id,n.key,n.tenant_id,s.repository FROM nodes n JOIN delivery_metric_sources s
	 ON s.tenant_id=n.tenant_id AND s.project_id=n.project_id WHERE n.id=$1 AND n.deleted_at IS NULL
	), pending AS (
	 SELECT d.repository,d.pull_request,d.branch,d.state_since AS since FROM target n JOIN delivery_items d
	 ON d.tenant_id=n.tenant_id AND d.ticket_node_id=n.id AND d.repository=n.repository
	 WHERE d.state<>'merged' AND coalesce(d.observation->>'merged','false')<>'true'
	 AND NOT EXISTS(SELECT 1 FROM delivery_metric_pulls p WHERE p.repository=d.repository AND p.pull_request=d.pull_request AND p.merged_at IS NOT NULL)
	 AND ((d.pull_request IS NOT NULL AND d.observation->>'open'='true') OR
	 (d.branch LIKE 'work/%' AND substring(lower(d.branch) from '([a-z][a-z0-9_]*-[0-9]+)')=lower(n.key)
	 AND d.head_sha<>'' AND d.head_sha<>coalesce(d.observation->>'base_sha','')))
	 UNION ALL
	 SELECT p.repository,p.pull_request,p.head_branch,p.opened_at FROM target n JOIN delivery_metric_pulls p ON p.tenant_id=n.tenant_id AND p.repository=n.repository
	 WHERE p.closed_at IS NULL AND p.merged_at IS NULL AND p.head_branch LIKE 'work/%'
	 AND substring(lower(p.head_branch) from '([a-z][a-z0-9_]*-[0-9]+)')=lower(n.key)
	) SELECT repository,pull_request,branch,since FROM pending
	 ORDER BY (branch=$2 AND (repository=$3 OR $3='')) DESC,
	 (branch=$2 AND (repository=$3 OR $3='') AND pull_request IS NOT NULL) DESC,
	 since,repository,pull_request NULLS FIRST,branch LIMIT 1`, ticket, candidateBranch, candidateRepository).Scan(&delivery.Repository, &delivery.PullRequest, &delivery.Branch, &conflict.Since)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	conflict.Reason, conflict.Delivery = "built_unmerged", &delivery
	if result != nil && result.Candidate != nil && delivery.Repository != "" &&
		(result.Candidate.Repository == "" || result.Candidate.Repository == delivery.Repository) &&
		strings.HasPrefix(delivery.Branch, "work/") && len(delivery.Branch) > 5 && result.Candidate.Branch == delivery.Branch {
		result.Continuation, result.ticket = &delivery, ticket
		return nil
	}
	return &conflict
}

// withBuildVisibilityTx widens project visibility only for the bounded boolean
// admission probe. Rolling back the savepoint restores every setting; tenant
// RLS remains in force and metadata is selected afterward in the caller scope.
func withBuildVisibilityTx(ctx context.Context, tx pgx.Tx, probe func(pgx.Tx) error) error {
	step, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = step.Rollback(ctx) }()
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return err
	}
	if err = probe(step); err != nil {
		return err
	}
	return step.Rollback(ctx)
}
