// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Keep queue callers on the same lower-level policy used by device pairing.
type BuildConflict = workorders.BuildConflict
type BuildStarter = workorders.BuildStarter
type BuildDelivery = workorders.BuildDelivery

func NormalizeParallelReason(reason *string) error {
	return workorders.NormalizeParallelReason(reason)
}

func BuildTicketTx(ctx context.Context, tx pgx.Tx, ticket, order, run *string) (*string, error) {
	return workorders.BuildTicketTx(ctx, tx, ticket, order, run)
}

func RequireBuildAdmissionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, except string, parallelReason *string) error {
	return workorders.RequireBuildAdmissionTx(ctx, tx, p, ticket, except, parallelReason)
}
