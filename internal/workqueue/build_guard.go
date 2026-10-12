// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

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
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// BuildConflict contains only public metadata selected under the caller's RLS.
// Private references, leases and repository observations never leave the store.
type BuildConflict struct {
	Reason    string         `json:"reason"`
	SessionID *string        `json:"session_id"`
	Label     *string        `json:"label"`
	StartedBy *BuildStarter  `json:"started_by"`
	Since     time.Time      `json:"since"`
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
	return fmt.Sprintf("build in flight: %s, started by %s since %s (session %s)", *e.Label, e.StartedBy.Name, e.Since.UTC().Format(time.RFC3339), *e.SessionID)
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
	if !utf8.ValidString(*reason) || utf8.RuneCountInString(*reason) > 500 || strings.ContainsFunc(*reason, unicode.IsControl) {
		return workorders.Fail(400, "parallel_reason must be 1–500 characters without control characters")
	}
	*reason = strings.TrimSpace(*reason)
	if *reason == "" {
		return workorders.Fail(400, "parallel_reason must be nonempty")
	}
	return nil
}

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
	var conflict BuildConflict
	var starter BuildStarter
	err := tx.QueryRow(ctx, `SELECT s.id::text,coalesce(s.display_label,s.harness),p.id::text,p.name,p.kind,s.created_at
	 FROM harness_sessions s JOIN principals p ON p.tenant_id=s.tenant_id AND p.id=coalesce(s.started_by_principal_id,s.owner_principal_id,s.agent_principal_id)
	 WHERE s.ticket_node_id=$1 AND s.role='worker' AND s.stopped_at IS NULL AND s.id::text<>$2
	 ORDER BY s.created_at,s.id LIMIT 1`, ticket, except).Scan(&conflict.SessionID, &conflict.Label, &starter.ID, &starter.Name, &starter.Kind, &conflict.Since)
	if err == nil {
		conflict.Reason, conflict.StartedBy = "active_builder", &starter
		return &conflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
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
