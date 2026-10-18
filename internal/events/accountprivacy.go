// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"strings"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func redactAccountEvents(ctx context.Context, tx pgx.Tx, p tenant.Principal, items []Event) error {
	for i := range items {
		e := &items[i]
		if !strings.HasPrefix(e.Type, "account.") {
			continue
		}
		ids := []string{}
		seen := map[string]bool{}
		for _, raw := range [][]byte{e.Before, e.After} {
			if len(raw) == 0 {
				continue
			}
			found, err := accountprivacy.IDs(raw, "")
			if err != nil {
				return err
			}
			for _, id := range found {
				if !seen[id] {
					ids = append(ids, id)
					seen[id] = true
				}
			}
		}
		policy, err := accountprivacy.Load(ctx, tx, p, ids)
		if err != nil {
			return err
		}
		fallback := ""
		if len(ids) == 1 {
			fallback = ids[0]
		}
		e.Before, err = accountprivacy.EventSnapshot(e.Before, policy, fallback)
		if err != nil {
			return err
		}
		e.After, err = accountprivacy.EventSnapshot(e.After, policy, fallback)
		if err != nil {
			return err
		}
	}
	return nil
}
