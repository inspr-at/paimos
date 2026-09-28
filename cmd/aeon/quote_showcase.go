// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/business/quoteshowcase"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

type archiveQuotes []string

func (a *archiveQuotes) String() string { return strings.Join(*a, ",") }
func (a *archiveQuotes) Set(value string) error {
	*a = append(*a, value)
	return nil
}

// quoteShowcaseApply archives explicit quotes and creates the showcase bundle.
// It does not use dev-login. Dry-run is the default.
func quoteShowcaseApply(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	const usage = "usage: aeon quote-showcase apply --tenant SLUG --bundle DIR|- [--archive-quote UUID ...] [--actor-principal-id UUID] [--apply]"
	f := flag.NewFlagSet("aeon quote-showcase apply", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	tenantSlug := f.String("tenant", "", "target tenant slug")
	bundlePath := f.String("bundle", "", "bundle directory or - for tar on stdin")
	var archives archiveQuotes
	f.Var(&archives, "archive-quote", "quote to archive; repeat for each id")
	actorID := f.String("actor-principal-id", "", "tenant admin UUID; defaults to bootstrap operator")
	apply := f.Bool("apply", false, "write changes; default is dry-run")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *tenantSlug == "" || *bundlePath == "" {
		return errors.New(usage)
	}
	var bundle quoteshowcase.Bundle
	var err error
	if *bundlePath == "-" {
		bundle, err = quoteshowcase.ReadTar(stdin)
	} else {
		bundle, err = quoteshowcase.ReadDir(*bundlePath)
	}
	if err != nil {
		return fmt.Errorf("read showcase bundle: %w", err)
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
	report, err := quoteshowcase.Apply(ctx, pool, tenantID, *actorID, cfg.FilesDir, bundle, archives, cfg.LinkKey, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}
