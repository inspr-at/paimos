// SPDX-License-Identifier: AGPL-3.0-only
package reviewgate

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// CoordinatorOKTx retains the consumer entry point, but trusts only a verdict
// authenticated by the assigned reviewer run. Legacy event rows fail closed.
func CoordinatorOKTx(ctx context.Context, tx pgx.Tx, ticket string) (bool, error) {
	return AuthenticatedOKTx(ctx, tx, ticket, "review")
}
