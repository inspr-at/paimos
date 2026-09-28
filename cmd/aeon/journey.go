// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/journey"
)

// journeyCommand is operator-only. Seed --brief 1|2|3 completes a fixed
// disposable intake through the intake and requirements mutations; these
// commands have no HTTP route.
func journeyCommand(args []string, stdout io.Writer) error {
	if err := journey.ValidateOperator(args); err != nil {
		return err
	}
	return withPool(func(ctx context.Context, pool *pgxpool.Pool) error {
		return journey.RunOperator(ctx, pool, args, stdout)
	})
}
