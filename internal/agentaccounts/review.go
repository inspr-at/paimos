// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReviewAccount uses the ordinary admission policy without reserving. Actual
// reservation and claim recheck capacity and approval on the existing paths.
func ReviewAccount(ctx context.Context, tx pgx.Tx, profileID, harness, projectID string, now time.Time) (*Account, error) {
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return nil, err
	}
	kept := []Account{}
	for _, a := range accounts {
		if a.Harness == harness {
			kept = append(kept, a)
		}
	}
	advice, err := routingAdvice(ctx, tx, kept, profileID, runRow{Purpose: "managed"}, now)
	if err != nil {
		return nil, err
	}
	best := 0
	var selected *Account
	for i := range kept {
		a := &kept[i]
		r := advice[a.ID]
		if r.Wait != nil || r.AvailableSlots < 1 || r.Rank < 1 {
			continue
		}
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals p WHERE p.id=$1 AND p.kind='agent' AND p.status='active'
            AND (aeon_principal_visibility(p.tenant_id,p.id)='*' OR $3::uuid=ANY(NULLIF(NULLIF(aeon_principal_visibility(p.tenant_id,p.id),'*'),'')::uuid[]))
            AND (NOT EXISTS(SELECT 1 FROM agent_pairing_computers WHERE principal_id=p.id) OR EXISTS(
                SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
                JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
                WHERE e.account_id=$2 AND c.principal_id=p.id AND c.state='connected' AND e.state='connected'
                AND q.state='redeemed' AND e.ongoing_approved_at IS NOT NULL)))`, a.RegisteredBy, a.ID, optionalReviewProject(projectID)).Scan(&allowed)
		if err != nil {
			return nil, err
		}
		if allowed && (selected == nil || r.Rank < best) {
			copy := *a
			selected = &copy
			best = r.Rank
		}
	}
	return selected, nil
}
func optionalReviewProject(id string) any {
	if id == "" {
		return nil
	}
	return id
}
