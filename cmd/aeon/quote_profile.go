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

	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

// quoteProfileApply is the operator entry point for an offline document
// profile. It does not use dev-login or contact the classic platform.
func quoteProfileApply(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	const usage = "usage: aeon quote-profile apply --tenant SLUG --bundle DIR|- [--default] [--assign-source-instance NAME] [--refresh-drafts] [--actor-principal-id UUID] [--apply]"
	f := flag.NewFlagSet("aeon quote-profile apply", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	tenantSlug := f.String("tenant", "", "target tenant slug")
	bundlePath := f.String("bundle", "", "bundle directory or - for tar on stdin")
	makeDefault := f.Bool("default", false, "select for new quotes")
	source := f.String("assign-source-instance", "", "select on imported draft quotes")
	refresh := f.Bool("refresh-drafts", false, "point live draft quotes already using this profile at its current revision")
	actorID := f.String("actor-principal-id", "", "tenant admin UUID; defaults to bootstrap operator")
	apply := f.Bool("apply", false, "write changes; default is dry-run")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *tenantSlug == "" || *bundlePath == "" {
		return errors.New(usage)
	}
	var bundle quotes.ProfileBundle
	var err error
	if *bundlePath == "-" {
		bundle, err = quotes.ReadProfileBundleTar(stdin)
	} else {
		bundle, err = quotes.ReadProfileBundleDir(*bundlePath)
	}
	if err != nil {
		return fmt.Errorf("read profile bundle: %w", err)
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	var pool *pgxpool.Pool
	if *apply {
		pool, err = db.Open(ctx, cfg.DatabaseURL)
	} else {
		// A plan must not run migrations just by opening the target.
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
	run := quotes.ApplyProfileBundle
	if *refresh {
		run = quotes.ApplyProfileBundleRefreshingDrafts
	}
	report, err := run(ctx, pool, tenantID, *actorID, cfg.FilesDir, *source, bundle, *makeDefault, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}
