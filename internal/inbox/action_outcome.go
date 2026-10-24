// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// RecordActionOutcomeTx accepts only a server-recorded terminal action outcome.
// The approval source holds tenant/tree, request and FK-parent locks, performs
// its current authorization checks, and finishes every record write first.
// System event metadata and the ordinary inbox receipt/wake protocol preserve
// project-only decision authority without enlarging the caller's visibility.
func RecordActionOutcomeTx(ctx context.Context, tx pgx.Tx, system tenant.Principal, messageID, recipient, project, key, body string) error {
	var authorized bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals p
 WHERE p.id=$1 AND p.tenant_id=$2 AND p.kind='agent' AND p.name='System' AND p.roles @> ARRAY['system'] AND p.status='active')
 AND EXISTS(SELECT 1 FROM action_approval_requests a WHERE a.tenant_id=$2 AND a.outcome_message_id=$3
 AND a.agent_principal_id=$4 AND a.project_id=$5 AND 'action-outcome:'||a.id::text=$6 AND a.state IN ('approved','denied','expired','refused'))`, system.ID, system.TenantID, messageID, recipient, project, key).Scan(&authorized)
	if err != nil {
		return err
	}
	if !authorized || system.Kind != tenant.Agent {
		return errForbidden
	}
	_, err = acceptMessageWith(ctx, tx, system, Acceptance{policyOutcome: true, MessageID: messageID, RecipientPrincipalID: recipient, SenderLabel: "Action approvals", Body: body, IdempotencyKey: key}, func(change events.Change) (int64, error) {
		if change.Type == "inbox.receipt_queued" {
			// The established system-event allowlist predates queued receipts.
			// Attribute this new receipt to the authorized initiating actor;
			// its own metadata event remains readable under project-only RLS.
			actor, ok := tenant.PrincipalFrom(ctx)
			if !ok {
				actor = system // expiry already runs in explicit service visibility
			}
			event, err := events.Append(ctx, tx, actor, change)
			return event.ID, err
		}
		return systemEvent(ctx, tx, system.TenantID, change.Type, change.After)
	})
	return err
}
