// SPDX-License-Identifier: AGPL-3.0-only
// OPS tool: default READ ONLY plan; --apply only after LEAD approves that plan.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	tid := flag.String("tenant", "", "explicit tenant UUID")
	project := flag.String("project", "", "explicit project UUID")
	actor := flag.String("actor", "", "existing person principal UUID with project delivery authority")
	repository := flag.String("repository", "", "linked owner/repository")
	expected := flag.Int("expect-linked-prs", 87, "refuse a changed linked PR population")
	dry := flag.Bool("dry-run", false, "plan in a READ ONLY snapshot (also the default)")
	apply := flag.Bool("apply", false, "write the approved linked-change plan; OPS only after LEAD go")
	flag.Parse()
	if flag.NArg() != 0 || *dry && *apply || !workorders.UUID(*tid) || !workorders.UUID(*project) || !workorders.UUID(*actor) || !reviewgate.ValidRepository(*repository) {
		return fmt.Errorf("explicit --tenant, --project, --actor and --repository required; choose --dry-run (default) or --apply")
	}
	url := os.Getenv("AEON_FLOW_DATABASE_URL")
	if url == "" {
		return fmt.Errorf("OPS must supply its existing database connection through AEON_FLOW_DATABASE_URL; it is never included in the plan")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("invalid OPS database connection")
	}
	defer pool.Close()
	p := tenant.Principal{TenantID: *tid, ID: *actor, Kind: tenant.Person}
	err = db.InTenantReadSnapshot(db.AllProjects(ctx, "OPS Flow backfill actor lookup"), pool, *tid, func(tx pgx.Tx) error {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind='person' AND status='active')`, *actor).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("active person principal required")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("existing active person principal and a working OPS database connection required")
	}
	m := delivery.New(pool, crossreview.AppConfig{TenantID: *tid, Repository: *repository}, nil, nil, nil)
	plan, err := m.BackfillFlow(ctx, p, *project, *expected, *apply)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(plan)
}
