// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

// releaseNotesBackfill is the operator entry point for snapshots that were
// never stored at publication. Dry-run is the default and does not migrate.
func releaseNotesBackfill(ctx context.Context, args []string, stdout io.Writer) error {
	const usage = "usage: aeon release-notes backfill --tenant SLUG [--actor-principal-id UUID] [--apply]"
	f := flag.NewFlagSet("aeon release-notes backfill", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	tenantSlug := f.String("tenant", "", "target tenant slug")
	actorID := f.String("actor-principal-id", "", "principal UUID recorded on the backfill events; default is the access operator")
	apply := f.Bool("apply", false, "insert snapshots; default is a dry-run plan")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *tenantSlug == "" {
		return errors.New(usage)
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	var pool *pgxpool.Pool
	if *apply {
		pool, err = db.Open(ctx, cfg.DatabaseURL)
	} else {
		pool, err = pgxpool.New(ctx, cfg.DatabaseURL)
		if err == nil {
			err = pool.Ping(ctx)
		}
	}
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		return fmt.Errorf("open target database: %w", err)
	}
	defer pool.Close()
	tenantID, err := tenantbootstrap.ResolveSlug(ctx, pool, *tenantSlug)
	if err != nil {
		return fmt.Errorf("tenant %q: %w", *tenantSlug, err)
	}
	report, err := releases.BackfillNoteSnapshots(ctx, pool, tenantID, *actorID, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}
