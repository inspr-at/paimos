// SPDX-License-Identifier: AGPL-3.0-only
package reviewgate

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/jackc/pgx/v5"
)

// CoordinatorReview is a recorded observation, not permission to merge. Both
// model identities are read from PAIMOS sessions; the caller supplies no family.
type CoordinatorReview struct {
	Head            string `json:"head_sha"`
	Base            string `json:"base_sha"`
	AuthorSession   string `json:"author_session_id"`
	ReviewerSession string `json:"reviewer_session_id"`
	AuthorFamily    string `json:"author_family"`
	ReviewerFamily  string `json:"reviewer_family"`
	OK              bool   `json:"ok"`
}

func CoordinatorFamiliesTx(ctx context.Context, tx pgx.Tx, ticket, project string, in *CoordinatorReview) (bool, error) {
	if !ValidSHA(in.Head) || !ValidSHA(in.Base) || in.Head == in.Base || in.AuthorSession == in.ReviewerSession {
		return false, nil
	}
	family := func(id, role string) (string, error) {
		var harness, model string
		err := tx.QueryRow(ctx, `SELECT harness,coalesce(model,'') FROM harness_sessions WHERE id=$1 AND ticket_node_id=$2 AND project_id=$3 AND role=$4 AND archived_at IS NULL`, id, ticket, project, role).Scan(&harness, &model)
		if errors.Is(err, pgx.ErrNoRows) {
			return "unknown", nil
		}
		if err != nil {
			return "", err
		}
		return harnesslaunch.ModelFamily(harness, model), nil
	}
	var err error
	in.AuthorFamily, err = family(in.AuthorSession, "worker")
	if err != nil {
		return false, err
	}
	in.ReviewerFamily, err = family(in.ReviewerSession, "worker")
	if err != nil {
		return false, err
	}
	return in.AuthorFamily != "unknown" && in.ReviewerFamily != "unknown" && in.AuthorFamily != in.ReviewerFamily, nil
}

// CoordinatorOKTx consumes only the latest recorded verdict on the current
// exact delivery range in the linked repository. A later refusal revokes it.
func CoordinatorOKTx(ctx context.Context, tx pgx.Tx, ticket string) (bool, error) {
	var raw []byte
	var head, base string
	err := tx.QueryRow(ctx, `SELECT e.after,d.head_sha,d.observation->>'base_sha' FROM events e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id JOIN delivery_items d ON d.tenant_id=n.tenant_id AND d.ticket_node_id=n.id JOIN delivery_metric_sources s ON s.tenant_id=d.tenant_id AND s.project_id=d.project_id AND s.repository=d.repository WHERE e.node_id=$1 AND e.type='coordinator_review.recorded' ORDER BY e.id DESC,d.updated_at DESC,d.id DESC LIMIT 1`, ticket).Scan(&raw, &head, &base)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var v CoordinatorReview
	if len(raw) > 8192 || json.Unmarshal(raw, &v) != nil || !v.OK || v.Head != head || v.Base != base || !ValidSHA(head) || !ValidSHA(base) || !ValidFamily(v.AuthorFamily) || !ValidFamily(v.ReviewerFamily) || v.AuthorFamily == v.ReviewerFamily {
		return false, nil
	}
	return true, nil
}
