// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/jackc/pgx/v5"
)

func sessionAccountUse(ctx context.Context, tx pgx.Tx, s Session, account *string) (string, error) {
	if account == nil {
		return "unattributed", nil
	}
	var known bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts WHERE id=$1 AND harness=$2)`, *account, s.Harness).Scan(&known); err != nil {
		return "", err
	}
	if !known {
		return "unattributed", nil
	}
	allowed, err := accountuse.AllowedForProject(ctx, tx, *account, s.ProjectID)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "outside_matrix", nil
	}
	return "allowed", nil
}
