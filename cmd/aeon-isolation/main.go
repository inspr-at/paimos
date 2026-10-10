// SPDX-License-Identifier: AGPL-3.0-only

// aeon-isolation is an opt-in local test/run helper. It does not alter agentd
// launch behavior or confer permission to run a routine.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/inspr-at/paimos/internal/runisolation"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:]); err != nil {
		// Do not print exec errors: command arguments may contain credentials.
		fmt.Fprintln(os.Stderr, "isolation operation failed; inspect the value-free run report")
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string) error {
	if len(args) == 1 && args[0] == "_guard" {
		return runisolation.Guard(ctx, os.Stdin, os.Stdout, os.Stderr)
	}
	if len(args) == 0 || (args[0] != "run" && args[0] != "check") {
		fmt.Fprintln(os.Stderr, "usage: aeon-isolation run --root PRIVATE-DIR --tenant ID --account ID --host ID --run ATTEMPT-ID -- COMMAND [ARGS]; aeon-isolation check --root PRIVATE-DIR")
		return errors.New("expected run or check")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	// Flag errors can include supplied values; keep them out of diagnostics.
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "shared private registry for this host/account")
	owner := runisolation.Owner{}
	flags.StringVar(&owner.TenantID, "tenant", "", "tenant ID")
	flags.StringVar(&owner.AccountID, "account", "", "selected account ID")
	flags.StringVar(&owner.HostID, "host", "", "registered host ID")
	flags.StringVar(&owner.RunID, "run", "", "unique assignment/attempt ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "check" {
		if flags.NArg() != 0 {
			return errors.New("unexpected check arguments")
		}
		report, err := runisolation.Check(ctx, *root)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return runisolation.Control(ctx, executable, []string{"_guard"}, runisolation.Request{Root: *root, Owner: owner, Directory: directory, Command: flags.Args()}, os.Stdout, os.Stderr)
}
