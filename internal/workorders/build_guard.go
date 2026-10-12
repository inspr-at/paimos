// SPDX-License-Identifier: AGPL-3.0-only
package workorders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
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

// BuildTicketTx resolves the at-most-three public bindings under caller RLS.
// Work-order children and their runs occupy the originating ticket's slot;
// standalone orders use an explicit ticket if supplied, otherwise their own
// slot. Conflicting originating ticket bindings fail closed.
// Callers hold the tenant/tree fences, so bindings cannot change before write.
func BuildTicketTx(ctx context.Context, tx pgx.Tx, ticket, order, run *string) (*string, error) {
	expected := 0
	for _, id := range []*string{ticket, order, run} {
		if id != nil {
			expected++
		}
	}
	if expected == 0 {
		return nil, nil
	}
	var target *string
	var found, distinct int
	err := tx.QueryRow(ctx, `WITH bindings AS (
	 SELECT $1::uuid AS node_id,NULL::uuid AS queue_ticket,$1::uuid AS direct_ticket WHERE $1::uuid IS NOT NULL
	 UNION ALL SELECT node_id,NULL::uuid,$1::uuid FROM work_orders WHERE node_id=$2
	 UNION ALL SELECT work_order_id,queue_node_id,$1::uuid FROM agent_runs WHERE id=$3
	) SELECT min(`+buildTicketIDSQL+`::text),count(*),count(DISTINCT `+buildTicketIDSQL+`)
	 FROM bindings b `+buildTicketJoinsSQL+` WHERE n.id IS NOT NULL AND n.deleted_at IS NULL`, ticket, order, run).Scan(&target, &found, &distinct)
	if err != nil {
		return nil, err
	}
	if found != expected {
		return nil, pgx.ErrNoRows
	}
	if distinct != 1 {
		return nil, Fail(409, "ticket, work order and run binding conflict")
	}
	return target, nil
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
func RequireBuildAdmissionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, except string, parallelReason *string) error {
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
	) SELECT repository,pull_request,branch,since FROM pending ORDER BY since,repository,pull_request NULLS FIRST,branch LIMIT 1`, ticket).Scan(&delivery.Repository, &delivery.PullRequest, &delivery.Branch, &conflict.Since)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	conflict.Reason, conflict.Delivery = "built_unmerged", &delivery
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
