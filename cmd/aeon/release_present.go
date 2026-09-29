// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

const releasePresentUsage = "usage: aeon release-notes present --tenant SLUG --project KEY --actor-principal-id UUID --release VERSION " +
	"(--theme TEXT --headline TEXT [--intro TEXT] [--theme-de TEXT] [--headline-de TEXT] [--intro-de TEXT] | --file PATH|- | --clear) [--apply]"

// PresentReport is what `aeon release-notes present` prints.
type PresentReport struct {
	Applied  bool                              `json:"applied"`
	TenantID string                            `json:"tenant_id"`
	Change   releasehistory.PresentationChange `json:"change"`
}

// releaseNotesPresent writes a release's theme, headline and intro (AEON-305)
// against the configured database. Like backfill it never migrates, names an
// explicit active person as actor, and is a dry run unless --apply is given.
func releaseNotesPresent(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	f := flag.NewFlagSet("aeon release-notes present", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	tenantSlug := f.String("tenant", "", "target tenant slug")
	project := f.String("project", "", "project route key, node key or UUID")
	actorID := f.String("actor-principal-id", "", "active person with releases.deploy on the project")
	release := f.String("release", "", "calendar version, with or without v")
	file := f.String("file", "", "JSON presentation file, or - for stdin")
	clear := f.Bool("clear", false, "remove the presentation")
	apply := f.Bool("apply", false, "write; default is a dry run")
	var in releasehistory.PresentationInput
	f.StringVar(&in.ThemeEN, "theme", "", "short theme, English")
	f.StringVar(&in.ThemeDE, "theme-de", "", "short theme, German")
	f.StringVar(&in.HeadlineEN, "headline", "", "one headline sentence, English")
	f.StringVar(&in.HeadlineDE, "headline-de", "", "one headline sentence, German")
	f.StringVar(&in.IntroEN, "intro", "", "two or three sentences, English")
	f.StringVar(&in.IntroDE, "intro-de", "", "two or three sentences, German")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *tenantSlug == "" || *project == "" || *actorID == "" || *release == "" {
		return errors.New(releasePresentUsage)
	}
	textFlags := in != (releasehistory.PresentationInput{})
	sources := 0
	for _, used := range []bool{textFlags, *file != "", *clear} {
		if used {
			sources++
		}
	}
	if sources != 1 {
		return errors.New(releasePresentUsage)
	}
	if *file != "" {
		var r io.Reader = stdin
		if *file != "-" {
			fh, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer fh.Close()
			r = fh
		}
		dec := json.NewDecoder(io.LimitReader(r, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return fmt.Errorf("presentation file: %w", err)
		}
	}
	if !*clear {
		// Fail on bad text before touching the database.
		if _, err := in.Normalize(); err != nil {
			return err
		}
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
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
	report, err := presentRelease(ctx, pool, tenantID, strings.TrimSpace(*actorID), *project, *release, in, *clear, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}

// presentRelease authorizes the actor inside the tenant and plans or writes.
func presentRelease(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, project, version string, in releasehistory.PresentationInput, clear, apply bool) (PresentReport, error) {
	report := PresentReport{Applied: apply, TenantID: tenantID}
	var kind string
	actor := tenant.Principal{ID: actorID, TenantID: tenantID}
	// Project visibility is the actor's own: the transaction reads it from ctx.
	err := db.InTenant(tenant.WithPrincipal(ctx, actor), pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE tenant_id=$1 AND id::text=$2`, tenantID, actorID).Scan(&kind); err != nil {
			return releasehistory.ErrPresentationDenied
		}
		// Offline there is no agent key, so no agent scopes: like backfill, the
		// named actor is a person. The release agent names its operator.
		if kind != string(tenant.Person) {
			return releasehistory.ErrPresentationDenied
		}
		actor.Kind = tenant.Person
		actx := tenant.WithPrincipal(ctx, actor)
		if !apply {
			if _, err := tx.Exec(actx, `SET LOCAL transaction_read_only = on`); err != nil {
				return err
			}
		} else if _, err := tx.Exec(actx, `SET LOCAL lock_timeout = '5s'`); err != nil {
			return err
		}
		projectID, err := releasehistory.ResolveProject(actx, tx, project)
		if err != nil {
			return fmt.Errorf("project %q: %w", project, err)
		}
		if err := releasehistory.AuthorizePresentation(actx, tx, actor, projectID); err != nil {
			return err
		}
		switch {
		case clear && apply:
			report.Change, err = releasehistory.ClearPresentation(actx, tx, actor, projectID, version)
		case clear:
			v := strings.TrimPrefix(strings.TrimSpace(version), "v")
			if !releasehistory.ValidVersion(v) {
				return fmt.Errorf("%w: version %q is not an inspr-calendar-v2 coordinate", releasehistory.ErrPresentationInvalid, version)
			}
			before, lerr := releasehistory.LoadPresentation(actx, tx, projectID, v, false)
			report.Change, err = releasehistory.PresentationChange{Version: v, ProjectID: projectID, Changed: before != nil, Before: before}, lerr
		case apply:
			report.Change, err = releasehistory.SavePresentation(actx, tx, actor, projectID, version, in)
		default:
			report.Change, _, err = releasehistory.PlanPresentation(actx, tx, projectID, version, in, false)
		}
		return err
	})
	return report, err
}
