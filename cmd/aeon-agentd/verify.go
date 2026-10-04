// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func verifyCommand(args []string, out io.Writer) error {
	return requestVerificationApproval(args, out, func(ctx context.Context, link string) error {
		if runtime.GOOS == "darwin" {
			return exec.CommandContext(ctx, "/usr/bin/open", link).Run()
		}
		return exec.CommandContext(ctx, "xdg-open", link).Run()
	})
}

func requestVerificationApproval(args []string, out io.Writer, opener func(context.Context, string) error) error {
	f := flag.NewFlagSet("verify", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var account, root string
	var noBrowser bool
	f.StringVar(&account, "account", "", "existing enrolled account UUID")
	f.StringVar(&root, "state-root", "", "private pairing state (read-only)")
	f.BoolVar(&noBrowser, "no-browser", false, "print the owner approval link without opening it")
	if err := f.Parse(args); err != nil {
		return err
	}
	if account == "" || len(f.Args()) != 0 {
		return errors.New("usage: aeon-agentd verify --account ID [--state-root PATH] [--no-browser]")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root, err = agentsetup.DefaultStateRoot(runtime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
		if err != nil {
			return err
		}
	}
	store, err := agentsetup.OpenStoreReadOnly(root)
	if err != nil {
		return err
	}
	defer store.Close()
	engine := &agentsetup.Engine{Store: store}
	link, err := engine.VerificationApprovalURL(account)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(out, "Open %s and choose Verify again on this account. Signed-in owner approval is required; no check or allowance has been created.\n", link); err != nil {
		return err
	}
	if !noBrowser {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if opener(ctx, link) != nil {
			_, err = fmt.Fprintln(out, "Could not open the browser. Use the printed approval link.")
		}
	}
	return err
}
