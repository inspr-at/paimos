// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func accountLinkCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("link-account", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var root, socket string
	var renew bool
	in := agentd.AccountLinkRequest{Operation: "offer"}
	f.StringVar(&root, "setup-root", "", "private pairing state")
	f.StringVar(&socket, "socket", "", "owner-only local daemon socket")
	f.StringVar(&in.Harness, "harness", "", "enrolled vendor harness")
	f.StringVar(&in.AccountID, "account-id", "", "enrolled account UUID (needed for multiple logins)")
	f.BoolVar(&renew, "renew", false, "replace an expired code")
	if err := f.Parse(args); err != nil {
		return err
	}
	if len(f.Args()) != 0 || root != "" && socket != "" || in.Harness == "" {
		return errors.New("link-account requires --harness NAME and optionally --account-id UUID")
	}
	if renew {
		in.Operation = "renew"
	}
	var c agentdwire.Client
	if socket != "" {
		c = agentdwire.Client{Socket: socket, TokenFile: socket + ".token"}
	} else {
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
		var err error
		c, err = agentdwire.OpenClient(filepath.Join(root, "daemon"))
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	v, err := c.AccountLink(ctx, in)
	if err != nil {
		return err
	}
	if v.State == "linked" {
		return nil
	} // Already linked: never repeat the prompt/result.
	if v.ShowPrompt {
		line, err := agentd.AccountLinkPrompt(v)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	if v.State != "pending" {
		return errors.New("account code expired; run link-account --renew")
	}
	in.Operation, in.RequestID, in.AccountID = "poll", v.RequestID, v.AccountID
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			v, err = c.AccountLink(ctx, in)
			if err != nil {
				return err
			}
			if v.State == "linked" {
				if !v.ShowResult {
					return nil
				}
				line, err := agentd.AccountLinkedLine(v)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(out, line)
				return err
			}
			if v.State != "pending" {
				return errors.New("account code expired or was revoked")
			}
		}
	}
}
