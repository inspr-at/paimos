// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

var useAccountID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (rt *runtime) cmdUse() *Command {
	var shell, socket, root string
	shell = "sh"
	return &Command{
		Name: "use", Short: "Print the local environment for one account", Use: "use <harness> <label-or-account-id>", minArgs: 2, maxArgs: 2,
		addFlags: func(fs *flagSet) {
			fs.string(&shell, "shell", 0, "sh, bash, zsh or fish")
			fs.string(&socket, "socket", 0, "owner-only local agentd socket")
			fs.string(&root, "setup-root", 0, "private pairing state")
		},
		run: func(args []string) error {
			switch args[0] {
			case "codex", "claude", "grok", "cursor", "pi":
			default:
				return usagef("unknown harness")
			}
			if strings.ContainsAny(args[1], "/\\") || args[1] == "" {
				return usagef("invalid label")
			}
			switch shell {
			case "sh", "bash", "zsh", "fish":
			default:
				return usagef("unsupported shell")
			}
			if socket != "" && root != "" {
				return usagef("choose --socket or --setup-root")
			}
			q := url.Values{"harness": {args[0]}, "label": {args[1]}}
			if useAccountID.MatchString(args[1]) {
				q = url.Values{"harness": {args[0]}, "account_id": {args[1]}}
			}
			var found agentaccounts.UseResult
			if err := rt.do("GET", "/api/agent-accounts/use?"+q.Encode(), nil, &found); err != nil {
				return err
			}
			if len(found.Accounts) == 0 {
				return fmt.Errorf("account not found")
			}
			var c agentdwire.Client
			if socket != "" {
				c = agentdwire.Client{Socket: socket, TokenFile: socket + ".token"}
			} else {
				if root == "" {
					home, err := os.UserHomeDir()
					if err != nil {
						return fmt.Errorf("local pairing state unavailable")
					}
					var err2 error
					root, err2 = agentsetup.DefaultStateRoot(goruntime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
					if err2 != nil {
						return fmt.Errorf("local pairing state unavailable")
					}
				}
				opened, err := agentdwire.OpenClient(filepath.Join(root, "daemon"))
				if err != nil {
					return fmt.Errorf("owning local agentd unavailable; no environment was printed")
				}
				c = opened
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var last error
			for _, door := range found.Accounts {
				local, err := c.AccountEnvironment(ctx, door.AccountID, door.DaemonID, door.Harness)
				if err != nil {
					last = err
					continue
				}
				line, err := capacityExport(local, shell)
				if err != nil {
					return err
				}
				fmt.Fprintln(rt.stdout, line)
				return nil
			}
			if last != nil {
				return fmt.Errorf("account is not available through this local agentd; no environment was printed")
			}
			return fmt.Errorf("account is not available through this local agentd; no environment was printed")
		},
	}
}
