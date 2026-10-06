// SPDX-License-Identifier: AGPL-3.0-only
package reviewgate

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/jackc/pgx/v5"
)

// VerifiedLatestTx is a conservative consumer of the existing review gate.
// It permits an escalation episode to resolve only from the latest exact-range
// review with proven author/reviewer families and effective model identity.
// This predicate grants no merge, deployment or next-run authority.
func VerifiedLatestTx(ctx context.Context, tx pgx.Tx, order, ticket string) (bool, error) {
	b, err := Load(ctx, tx, order)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if b.TicketID != ticket || !ValidRepository(b.Repository) || !ValidSHA(b.BaseSHA) || !ValidSHA(b.HeadSHA) || b.BaseSHA == b.HeadSHA || b.AuthorRunID == nil || !ValidFamily(b.AuthorFamily) {
		return false, nil
	}
	var status, evidence, model, harness, family, orderStatus, github string
	var effective, authorHarness, authorModel, authorFamily *string
	var raw []byte
	var current bool
	err = tx.QueryRow(ctx, `SELECT r.status,r.model_evidence,r.effective_model,p.model,p.harness,p.family,w.status,v.github_status,v.result,
 ap.harness,ap.model,ap.family,
 v.work_order_id=(SELECT x.work_order_id FROM work_order_reviews x WHERE x.ticket_node_id=$2 ORDER BY x.created_at DESC,x.work_order_id DESC LIMIT 1)
 AND NOT EXISTS(SELECT 1 FROM agent_runs newer JOIN nodes n ON n.id=newer.work_order_id JOIN work_orders nw ON nw.node_id=n.id
 WHERE n.parent_id=$2 AND nw.kind='build' AND newer.created_at>ar.created_at)
 FROM work_order_reviews v JOIN agent_runs r ON r.id=v.run_id JOIN work_orders w ON w.node_id=v.work_order_id
 JOIN model_profiles p ON p.id=r.model_profile_id
 LEFT JOIN agent_runs ar ON ar.id=v.author_run_id LEFT JOIN model_profiles ap ON ap.id=ar.model_profile_id
 WHERE v.work_order_id=$1 AND p.id=v.reviewer_profile_id AND v.evidence_id IS NOT NULL AND ar.status='completed'`, order, ticket).Scan(&status, &evidence, &effective, &model, &harness, &family, &orderStatus, &github, &raw, &authorHarness, &authorModel, &authorFamily, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !current || orderStatus == "cancelled" || github == "stale" || len(raw) > MaxOutput || b.ReviewerFamily == nil || family != *b.ReviewerFamily || !harnesslaunch.FamilyMatches(harness, model, family) || authorHarness == nil || authorModel == nil || authorFamily == nil || *authorFamily != b.AuthorFamily || !harnesslaunch.FamilyMatches(*authorHarness, *authorModel, *authorFamily) || effective == nil || !ModelMatches(model, *effective) {
		return false, nil
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, nil
	}
	ok, _ := Gate(status, evidence, effective, *b, result)
	return ok, nil
}
