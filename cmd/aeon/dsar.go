// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"errors"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/dsar"
)

func dsarCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "dsar" {
		return errors.New(dsar.Usage)
	}
	command, err := dsar.Parse(args[1:])
	if err != nil {
		return err
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return errors.New("load operator database configuration failed")
	}
	ctx := context.Background()
	// pgxpool.New does not migrate. Both commands use read-only transactions.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("open operator database failed")
	}
	defer pool.Close()
	return dsar.Execute(ctx, pool, command, stdout)
}
