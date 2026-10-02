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

	"github.com/jackc/pgx/v5/pgxpool"
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

// Execute emits nothing until all adapters succeed. No file is overwritten.
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
		_, err = stdout.Write(content)
		return err
	}
	f, err := os.OpenFile(command.Output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("create DSAR output failed; use a new path in a private directory")
	}
	_, err = f.Write(content)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("write DSAR output failed; discard the incomplete private file")
	}
	return nil
}
