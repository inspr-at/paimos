// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// MessagingAssistantNoticeTx uses the existing receipt-free System notice and
// wake path. The caller holds the tenant fence and has finished record writes.
// Fence all existing FK parents before postNotice first touches the event counter.
func MessagingAssistantNoticeTx(ctx context.Context, tx pgx.Tx, tenantID, owner, key, body string) error {
	var systemID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND name='System' AND roles @> ARRAY['system'] AND status='active'`).Scan(&systemID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{systemID, owner}); err != nil {
		return err
	}
	var targets int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM inbox_delivery_targets
	 WHERE principal_id=$1::uuid AND kind='webhook' AND enabled ORDER BY id LIMIT 33 FOR KEY SHARE) t`, owner).Scan(&targets); err != nil {
		return err
	}
	if targets > 32 {
		return errors.New("assistant notice target limit exceeded")
	}
	return postNotice(ctx, tx, tenant.Principal{ID: systemID, TenantID: tenantID, Kind: tenant.Agent}, owner, nil, "messaging-assistant/"+key, body)
}
