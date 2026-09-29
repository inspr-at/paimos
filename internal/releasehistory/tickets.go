// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
)

// TicketSource loads ticket metadata for one tenant. Keys that are not
// tickets in that tenant are omitted. A non-nil error leaves groups off the
// response so clients keep deriving them from the commit type.
type TicketSource func(ctx context.Context, tenantID string, keys []string) (map[string]TicketMeta, error)

// DBTickets reads kind and benefit fields for the given keys under the
// caller's tenant visibility. Deleted nodes are skipped.
func DBTickets(pool *pgxpool.Pool) TicketSource {
	return func(ctx context.Context, tenantID string, keys []string) (map[string]TicketMeta, error) {
		keys = uniqueTicketKeys(keys)
		out := map[string]TicketMeta{}
		if pool == nil || strings.TrimSpace(tenantID) == "" || len(keys) == 0 {
			return out, nil
		}
		err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			for start := 0; start < len(keys); start += 1000 {
				end := start + 1000
				if end > len(keys) {
					end = len(keys)
				}
				rows, err := tx.Query(ctx, `
					SELECT n.key, k.slug, n.fields
					FROM nodes n
					JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
					WHERE n.deleted_at IS NULL AND n.key = ANY($1::text[])`, keys[start:end])
				if err != nil {
					return err
				}
				for rows.Next() {
					var key, kind string
					var fields []byte
					if err := rows.Scan(&key, &kind, &fields); err != nil {
						rows.Close()
						return err
					}
					out[key] = ParseTicketMeta(kind, fields)
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return out, nil
	}
}

func uniqueTicketKeys(keys []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if ticketKey.FindString(key) != key || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

func historyTicketKeys(h History) []string {
	var keys []string
	for _, rel := range h.Releases {
		for _, change := range rel.Changes {
			keys = append(keys, change.Tickets...)
		}
	}
	return uniqueTicketKeys(keys)
}
