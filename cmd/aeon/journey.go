// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/journey"
)

// journeyCommand is operator-only. Seed accepts --brief 1|2|3 for a fixed
// disposable intake; these mutations have no HTTP route.
func journeyCommand(args []string, stdout io.Writer) error {
	if err := journey.ValidateOperator(args); err != nil {
		return err
	}
	return withPool(func(ctx context.Context, pool *pgxpool.Pool) error {
		return journey.RunOperator(ctx, pool, args, stdout)
	})
}
