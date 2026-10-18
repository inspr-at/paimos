// SPDX-License-Identifier: AGPL-3.0-only
package reviewgate

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

func Load(ctx context.Context, tx pgx.Tx, orderID string) (*Binding, error) {
	var b Binding
	err := tx.QueryRow(ctx, `SELECT ticket_snapshot,ticket_node_id::text,repository,base_sha,head_sha,author_run_id::text,author_family,reviewer_profile_id::text,reviewer_family,pull_request FROM work_order_reviews WHERE work_order_id=$1`, orderID).Scan(&b.TicketSnapshot, &b.TicketID, &b.Repository, &b.BaseSHA, &b.HeadSHA, &b.AuthorRunID, &b.AuthorFamily, &b.ProfileID, &b.ReviewerFamily, &b.PullRequest)
	return &b, err
}

var ErrEvidence = errors.New("review evidence requires the assigned, fenced reviewer run")

// RecordEvidence runs after insertion in the same work-order transaction.
// Only the exact assigned run, on its current daemon lease, may set a verdict.
// Replays retain the first result; a new review needs a new work order.
func RecordEvidence(ctx context.Context, tx pgx.Tx, orderID, evidenceID, principalID, runID, kind, text, daemon, generation string) error {
	var harness, model, family string
	err := tx.QueryRow(ctx, `SELECT p.harness,p.model,p.family FROM work_order_reviews v JOIN agent_runs r
        ON r.tenant_id=v.tenant_id AND r.id=v.run_id
        JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.model_profile_id
 JOIN agent_accounts a ON a.tenant_id=r.tenant_id AND a.id=r.account_id
        WHERE v.work_order_id=$1 AND r.id=$2 AND r.agent_principal_id=$3
        AND r.status IN ('starting','running','waiting') AND r.daemon_id=$4 AND r.daemon_generation=$5
        AND a.registered_by_principal_id=r.agent_principal_id AND a.daemon_id=r.daemon_id AND a.last_daemon_generation=r.daemon_generation
 AND p.id=v.reviewer_profile_id AND p.family=v.reviewer_family`, orderID, runID, principalID, daemon, generation).Scan(&harness, &model, &family)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrEvidence
	}
	if err != nil {
		return err
	}
	if !harnesslaunch.FamilyMatches(harness, model, family) || kind != "text" || daemon == "" || generation == "" {
		return ErrEvidence
	}
	var project *string
	var author string
	var authorRun, authorHarness, authorModel, authorFamily *string
	err = tx.QueryRow(ctx, `SELECT n.project_id::text,v.author_family,v.author_run_id::text,ap.harness,ap.model,ap.family
 FROM work_order_reviews v JOIN nodes n ON n.tenant_id=v.tenant_id AND n.id=v.ticket_node_id
 LEFT JOIN agent_runs ar ON ar.tenant_id=v.tenant_id AND ar.id=v.author_run_id
 LEFT JOIN model_profiles ap ON ap.tenant_id=ar.tenant_id AND ap.id=ar.model_profile_id
 WHERE v.work_order_id=$1`, orderID).Scan(&project, &author, &authorRun, &authorHarness, &authorModel, &authorFamily)
	if err != nil {
		return err
	}
	if authorRun != nil {
		if authorHarness == nil || authorModel == nil || authorFamily == nil || !harnesslaunch.FamilyMatches(*authorHarness, *authorModel, *authorFamily) {
			return ErrEvidence
		}
		author = *authorFamily
	}
	policy, err := LoadFamilyPolicyTx(ctx, tx, project)
	if err != nil {
		return err
	}
	if !ValidFamily(author) || policy.Effective.Mode != "off" && family == author {
		return ErrEvidence
	}
	result := Parse(text)
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var previous *string
	if err = tx.QueryRow(ctx, `SELECT evidence_id::text FROM work_order_reviews WHERE work_order_id=$1`, orderID).Scan(&previous); err != nil {
		return err
	}
	if previous != nil {
		var old string
		if err = tx.QueryRow(ctx, `SELECT reference FROM work_evidence WHERE id=$1`, *previous).Scan(&old); err != nil {
			return err
		}
		if old != text {
			return ErrEvidence
		}
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE work_order_reviews SET result=$2,evidence_id=$3 WHERE work_order_id=$1`, orderID, raw, evidenceID)
	return err
}
