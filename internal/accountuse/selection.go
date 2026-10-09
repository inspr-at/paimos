// SPDX-License-Identifier: AGPL-3.0-only
package accountuse

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const NotAllowed = "account_not_allowed_for_context"

type ContextLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ContextLabels projects matrix cells only; it never narrows usage totals.
func ContextLabels(ctx context.Context, tx pgx.Tx, ids []string) (map[string][]ContextLabel, error) {
	out := map[string][]ContextLabel{}
	for start := 0; start < len(ids); start += 256 {
		rows, err := tx.Query(ctx, `SELECT x.account_id::text,c.id::text,c.name FROM account_use_cells x JOIN work_contexts c ON c.tenant_id=x.tenant_id AND c.id=x.context_id WHERE x.account_id=ANY($1::uuid[]) AND c.archived_at IS NULL ORDER BY x.account_id,c.name,c.id LIMIT 4097`, ids[start:min(start+256, len(ids))])
		if err != nil {
			return nil, err
		}
		n := 0
		for rows.Next() {
			var account string
			var label ContextLabel
			if err := rows.Scan(&account, &label.ID, &label.Name); err != nil {
				rows.Close()
				return nil, err
			}
			n++
			if n > 4096 {
				rows.Close()
				return nil, fail(503, "account context projection exceeds bound")
			}
			out[account] = append(out[account], label)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func projectArg(project string) any {
	if project == "" {
		return nil
	}
	return project
}

// AllowedForProject delegates to the database boundary: an absent project uses
// the default context; an unmapped or holding project never falls back to it.
func AllowedForProject(ctx context.Context, tx pgx.Tx, account, project string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT aeon_account_use_allowed_for_project($1,$2)`, account, projectArg(project)).Scan(&allowed)
	return allowed, err
}

func RequireProject(ctx context.Context, tx pgx.Tx, account, project string) error {
	allowed, err := AllowedForProject(ctx, tx, account, project)
	if err != nil {
		return err
	}
	if !allowed {
		return fail(409, NotAllowed)
	}
	return nil
}

// RequireRun includes only the database's enrollment-bound verification
// exception. A purpose string alone cannot bypass the matrix.
func RequireRun(ctx context.Context, tx pgx.Tx, account, run string) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT aeon_account_use_allowed($1,$2)`, account, run).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return fail(409, NotAllowed)
	}
	return nil
}

// AllowedIDs batches predicate reads, keeping the SQL input bounded even when
// callers already own a larger display snapshot. It never mutates that snapshot.
func AllowedIDs(ctx context.Context, tx pgx.Tx, ids []string, project string) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for start := 0; start < len(ids); start += 256 {
		rows, err := tx.Query(ctx, `SELECT id::text FROM unnest($1::uuid[]) id WHERE aeon_account_use_allowed_for_project(id,$2)`, ids[start:min(start+256, len(ids))], projectArg(project))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
