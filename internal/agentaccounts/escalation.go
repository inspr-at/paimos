// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// EscalationRoomIDs narrows already-qualified accounts to fresh measured room.
// A manual cap or legacy provisional window cannot authorize automatic model
// escalation. The ordinary reservation/claim gates still run at actual launch.
func EscalationRoomIDs(ctx context.Context, tx pgx.Tx, ids []string, now time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if len(ids) > 256 {
		return nil, fail(503, "too many escalation accounts")
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM account_allowance_windows WHERE account_id=ANY($1::uuid[]) AND NOT pairing_verification AND NOT capacity_retired AND removed_at IS NULL LIMIT 4097) w`, ids).Scan(&count); err != nil {
		return nil, err
	}
	if count > 4096 {
		return nil, fail(503, "too many escalation room windows")
	}
	out := []string{}
	for _, id := range ids {
		account, err := getAccount(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		windows := account.Windows
		facts, err := factWindows(ctx, tx, account, now)
		if err != nil {
			return nil, err
		}
		windows = append(windows, facts...)
		known := false
		for _, w := range activeWindows(windows, now) {
			if w.capacityReadAt != nil && !now.Before(*w.capacityReadAt) && now.Sub(*w.capacityReadAt) <= 10*time.Minute && w.capacityAllowed && w.capacitySource != "estimate" && !synthetic(w) {
				known = true
				break
			}
		}
		if known {
			out = append(out, id)
		}
	}
	return out, nil
}
