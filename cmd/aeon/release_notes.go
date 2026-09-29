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
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

// releaseNotesBackfill is the operator entry point for snapshots that were
// never stored at publication. Dry-run is the default and does not migrate.
func releaseNotesBackfill(ctx context.Context, args []string, stdout io.Writer) error {
	return releaseNotesBackfillWithHistory(ctx, args, stdout, releasehistory.Embedded)
}

func releaseNotesBackfillWithHistory(ctx context.Context, args []string, stdout io.Writer, loadHistory func() (releasehistory.History, error)) error {
	const usage = "usage: aeon release-notes backfill --tenant SLUG --project KEY --actor-principal-id UUID [--release VERSION | --all-missing] [--apply]"
	f := flag.NewFlagSet("aeon release-notes backfill", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	tenantSlug := f.String("tenant", "", "target tenant slug")
	actorID := f.String("actor-principal-id", "", "active person with tenant-admin authority; required for dry-run and apply")
	project := f.String("project", "", "project route key, node key or UUID")
	release := f.String("release", "", "one published version")
	allMissing := f.Bool("all-missing", false, "all missing snapshots in the project (default)")
	apply := f.Bool("apply", false, "insert snapshots; default is a dry-run plan")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *tenantSlug == "" || *project == "" || *actorID == "" || (*release != "" && *allMissing) {
		return errors.New(usage)
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	// Maintenance never migrates before actor authorization. The deployed server
	// applies its migrations at startup; this command only plans/inserts notes.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err == nil {
		err = pool.Ping(ctx)
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
	history, err := loadHistory()
	if err != nil {
		return err
	}
	report, err := releases.BackfillNoteSnapshots(ctx, pool, tenantID, *actorID, *apply, releases.NoteBackfillOptions{Project: *project, Release: *release, AllMissing: *allMissing, History: history})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}
