// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

// This historical consumer accepts only a submitted reviewer capability and
// its authenticated event, before the merge. A reported measurement, legacy
// coordinator assertion or GitHub status cannot substitute for that verdict.
// It is observe-only; current admission continues to use AuthenticatedOKTx.
func auditHarnessCandidate(ctx context.Context, tx pgx.Tx, o Observation) (auditReviewBinding, error) {
	var b auditReviewBinding
	// Admission consumes submission events in event order. A newly created
	// pending run must not hide a verdict, and an older run can submit last.
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.base_sha,r.head_sha
 FROM events e JOIN review_runs r ON r.tenant_id=e.tenant_id
 AND r.id::text=e.after->>'run_id' AND r.ticket_node_id=e.node_id
 AND r.reviewer_principal_id=e.actor_principal_id
 WHERE e.node_id=$1 AND e.type='coordinator_review.recorded'
 AND r.project_id=$2 AND r.repository=$3 AND r.pull_request=$4
 AND r.kind='review' AND r.submitted_at IS NOT NULL AND r.submitted_at<=$5
 ORDER BY e.id DESC LIMIT 1`, o.Ticket, o.Project, o.Repository, o.PR, o.At).Scan(&b.Run, &b.Base, &b.Head)
	if errors.Is(err, pgx.ErrNoRows) {
		return auditReviewBinding{}, nil
	}
	return b, err
}

func auditReviewVerified(ctx context.Context, tx pgx.Tx, o Observation, b auditReviewBinding) (bool, error) {
	if b.Run == "" {
		return reviewgate.VerifiedLatestTx(ctx, tx, b.Order, *o.Ticket)
	}
	var binding reviewgate.RunBinding
	var author, reviewer, harness, model, verdict string
	var recorded bool
	err := tx.QueryRow(ctx, `SELECT r.repository,r.pull_request,r.target_branch,r.base_sha,r.head_sha,r.scope,
 r.author_family,r.reviewer_family,r.harness,r.model,coalesce(r.verdict,''),
 r.submitted_at IS NOT NULL AND r.submitted_at<=$5 AND r.submitted_at>=r.created_at
 AND r.submitted_at<r.expires_at AND r.credential_key_id IS NOT NULL
 AND EXISTS(SELECT 1 FROM events e WHERE e.node_id=r.ticket_node_id
 AND e.type='coordinator_review.recorded' AND e.after->>'run_id'=r.id::text
 AND e.actor_principal_id=r.reviewer_principal_id)
 FROM review_runs r WHERE r.id=$1 AND r.ticket_node_id=$2 AND r.project_id=$3
 AND r.repository=$4 AND r.kind='review'`, b.Run, o.Ticket, o.Project, o.Repository, o.At).
		Scan(&binding.Repository, &binding.PR, &binding.Target, &binding.Base, &binding.Head, &binding.Scope,
			&author, &reviewer, &harness, &model, &verdict, &recorded)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return recorded && verdict == "ok" && binding.Valid() && o.PR != nil && binding.PR == *o.PR &&
		binding.Base == b.Base && binding.Head == b.Head && reviewgate.ValidFamily(author) && reviewgate.ValidFamily(reviewer) &&
		author != reviewer && model != "" && harnesslaunch.ModelFamily(harness, model) == reviewer, nil
}
