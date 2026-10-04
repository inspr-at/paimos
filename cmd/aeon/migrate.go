// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 78 distinguishes a blocked maintenance migration from an ordinary boot error.
const migrationBlockedExitCode = 78

func serverErrorExitCode(err error) int {
	var blocked *db.BusyWorkParentsError
	if errors.As(err, &blocked) {
		return migrationBlockedExitCode
	}
	return 1
}

func migrateCommand(args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "--check" {
		return errors.New("usage: paimos migrate --check (read-only busy-parent preflight; applies no migrations)")
	}
	cfg, err := config.FromEnv()
	if err != nil {
		// Configuration errors can include a URL; never print credential inputs.
		return errors.New("migration preflight: invalid server configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("migration preflight: invalid database configuration")
	}
	defer pool.Close()
	err = db.CheckWorkMigration(ctx, pool, func(p db.BusyWorkParent) error {
		_, err := fmt.Fprintf(stdout, "tenant %s: %s (%s)\n", p.TenantID, p.Key, p.Reason)
		return err
	})
	var blocked *db.BusyWorkParentsError
	if errors.As(err, &blocked) {
		return blocked
	}
	if err != nil {
		return errors.New("migration preflight incomplete; old container must stay running; check database access and retry")
	}
	_, err = fmt.Fprintln(stdout, "Busy-parent preflight clean; no migrations applied. The migration rechecks under its locks; this snapshot does not authorize the switch.")
	return err
}
