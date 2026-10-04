// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

// The SQL guard protects every writer. This boundary check also rejects an
// explicit PATCH/bulk status value that happens to equal the derived value.
// It uses canonical visibility only within the narrow predicate function.
func requireLeafStatusWrite(ctx context.Context, tx pgx.Tx, id string) error {
	parent, err := db.WorkStatusParentTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if parent {
		return conflictCoded("parent status follows its children", "parent_status_derived")
	}
	return nil
}
