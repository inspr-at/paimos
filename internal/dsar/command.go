// SPDX-License-Identifier: AGPL-3.0-only
package dsar

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

const Usage = "usage: aeon admin dsar export --tenant SLUG --actor-principal-id OWNER_UUID --person UUID|EMAIL [--output PATH]\n       aeon admin dsar erase --tenant SLUG --actor-principal-id OWNER_UUID --person UUID|EMAIL --dry-run [--output PATH]"

type Command struct {
	Options Options
	Output  string
}

// Parse rejects destructive flags and incomplete scope before opening a DB.
func Parse(args []string) (Command, error) {
	var command Command
	if len(args) == 0 || (args[0] != "export" && args[0] != "erase") {
		return command, errors.New(Usage)
	}
	f := flag.NewFlagSet("aeon admin dsar "+args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&command.Options.Tenant, "tenant", "", "tenant slug (required)")
	f.StringVar(&command.Options.ActorID, "actor-principal-id", "", "active workspace owner person UUID (required)")
	f.StringVar(&command.Options.Person, "person", "", "principal UUID or email (required)")
	f.StringVar(&command.Output, "output", "-", "new owner-readable JSON file; default stdout")
	dry := f.Bool("dry-run", false, "required for erase; there is no destructive mode")
	if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
		return command, errors.New(Usage)
	}
	command.Options.Erase = args[0] == "erase"
	if command.Options.Tenant == "" || !uuidLike(command.Options.ActorID) || strings.TrimSpace(command.Options.Person) == "" || command.Output == "" || (*dry != command.Options.Erase) {
		return command, errors.New(Usage)
	}
	if !uuidLike(command.Options.Person) && !strings.Contains(command.Options.Person, "@") {
		return command, errors.New(Usage)
	}
	return command, nil
}

// Execute emits nothing until all adapters succeed and the collection audit
// commits in a separate transaction. No file is overwritten.
func Execute(ctx context.Context, pool *pgxpool.Pool, command Command, stdout io.Writer) error {
	report, err := Collect(ctx, pool, command.Options)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.New("encode DSAR packet failed")
	}
	if len(content) > 64<<20 {
		return errors.New("DSAR packet exceeds 64 MiB; no output emitted")
	}
	content = append(content, '\n')
	if command.Output == "-" {
		if err := auditCollection(ctx, pool, command.Options.ActorID, report); err != nil {
			return err
		}
		_, err = stdout.Write(content)
		return err
	}
	f, err := os.OpenFile(command.Output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("create DSAR output failed; use a new path in a private directory")
	}
	defer func() { _ = f.Close() }()
	if err := auditCollection(ctx, pool, command.Options.ActorID, report); err != nil {
		return err
	}
	_, err = f.Write(content)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("write DSAR output failed; discard the incomplete private file")
	}
	return nil
}

// auditCollection records references and counts, never packet contents or the
// supplied UUID/email lookup. It describes collection, not delivery: output
// can still fail after this durable record has committed.
func auditCollection(ctx context.Context, pool *pgxpool.Pool, actorID string, report Report) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Begin directly on the pool: even a caller's surrounding transaction must
	// not turn this into a savepoint or defer the audit commit until after output.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return errors.New("open DSAR audit transaction failed; no output emitted")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.system','on',true),
		set_config('aeon.visible_projects','',true),set_config('aeon.principal_ids','',true),set_config('statement_timeout','30s',true)`, report.TenantID); err != nil {
		return errors.New("scope DSAR audit transaction failed; no output emitted")
	}
	var authorized bool
	if err := tx.QueryRow(ctx, ownerQuery, report.TenantID, actorID).Scan(&authorized); err != nil || !authorized {
		return errors.New("DSAR owner authorization changed; no output emitted")
	}
	count := 0
	for _, section := range report.Sections {
		count += len(section.Records)
	}
	kind := "dsar.export.collected"
	if report.DryRun {
		kind = "dsar.erase.planned"
	}
	_, err = events.Append(ctx, tx, tenant.Principal{ID: actorID, TenantID: report.TenantID, Kind: tenant.Person}, events.Change{
		Type: kind,
		After: struct {
			SubjectID   string `json:"subject_principal_id"`
			Operation   string `json:"operation"`
			DryRun      bool   `json:"dry_run"`
			RecordCount int    `json:"record_count"`
			TableCount  int    `json:"table_count"`
		}{report.Subject.ID, report.Operation, report.DryRun, count, len(report.Sections)},
	})
	if err != nil {
		return errors.New("append DSAR audit event failed; no output emitted")
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("commit DSAR audit event failed; no output emitted")
	}
	return nil
}
