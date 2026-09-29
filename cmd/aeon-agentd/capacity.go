// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/internal/agentdwire"
)

// Read the authenticated daemon projection; this command does not launch a
// vendor, crawl config homes, or make a request to the Aeon server.
func capacityCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("capacity", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var root, socket, account string
	f.StringVar(&root, "setup-root", "", "private approved pairing state")
	f.StringVar(&socket, "socket", "", "local daemon socket")
	f.StringVar(&account, "account-id", "", "optional enrolled account UUID")
	if err := f.Parse(args); err != nil {
		return err
	}
	if len(f.Args()) != 0 || (root == "") == (socket == "") {
		return errors.New("capacity requires exactly one of --setup-root or --socket")
	}
	c := agentdwire.Client{Socket: socket, TokenFile: socket + ".token"}
	if root != "" {
		var err error
		c, err = agentdwire.OpenClient(filepath.Join(root, "daemon"))
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accounts, err := c.CapacityAccounts(ctx, account)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(accounts)
}
