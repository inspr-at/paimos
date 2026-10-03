// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func (rt *runtime) cmdCapacity() *Command {
	var emitEnv bool
	var daemon, profile, socket, root, shell string
	shell = "sh"
	return &Command{Name: "capacity", Short: "Account capacity advice", Use: "capacity next <harness>", subs: []*Command{{
		Name: "next", Short: "Next account in the server's routing order", Use: "capacity next <harness> [--env]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.bool(&emitEnv, "env", 0, "print a config-home export from the owning local agentd")
			fs.string(&daemon, "daemon-id", 0, "restrict advice to a computer")
			fs.string(&profile, "model-profile-id", 0, "restrict advice to a model profile")
			fs.string(&socket, "socket", 0, "owner-only local agentd socket (for --env)")
			fs.string(&root, "setup-root", 0, "private pairing state (for --env)")
			fs.string(&shell, "shell", 0, "sh, bash, zsh or fish (for --env)")
		},
		run: func(args []string) error {
			if emitEnv && rt.jsonOut {
				return usagef("--env and --json cannot be combined")
			}
			switch args[0] {
			case "codex", "claude", "grok", "cursor", "pi", "gemini", "opencode":
			default:
				return usagef("unknown harness")
			}
			if socket != "" && root != "" {
				return usagef("choose --socket or --setup-root")
			}
			switch shell {
			case "sh", "bash", "zsh", "fish":
			default:
				return usagef("unsupported shell")
			}
			q := url.Values{"harness": {args[0]}}
			if daemon != "" {
				q.Set("daemon_id", daemon)
			}
			if profile != "" {
				q.Set("model_profile_id", profile)
			}
			var advice agentaccounts.CapacityNext
			if err := rt.do("GET", "/api/agent-accounts/capacity/next?"+q.Encode(), nil, &advice); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(advice)
			}
			if len(advice.Accounts) == 0 {
				if emitEnv {
					return fmt.Errorf("no eligible account; no environment was printed")
				}
				reason := "no eligible account"
				if advice.Wait != nil {
					reason = advice.Wait.Code
					if advice.Wait.Until != nil {
						reason += " until " + advice.Wait.Until.Format(time.RFC3339)
					}
				}
				fmt.Fprintln(rt.stdout, "Waiting: "+reason)
				return nil
			}
			choice := advice.Accounts[0]
			if !emitEnv {
				fmt.Fprintf(rt.stdout, "%s · %s · %d agents can run in parallel\n", choice.AccountLabel, choice.DaemonID, advice.ParallelRuns)
				return nil
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
					root, err = agentsetup.DefaultStateRoot(goruntime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
					if err != nil {
						return fmt.Errorf("local pairing state unavailable")
					}
				}
				var err error
				c, err = agentdwire.OpenClient(filepath.Join(root, "daemon"))
				if err != nil {
					return fmt.Errorf("owning local agentd unavailable; no environment was printed")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			local, err := c.AccountEnvironment(ctx, choice.AccountID, choice.DaemonID, choice.Harness)
			if err != nil {
				return fmt.Errorf("account is not available through this local agentd; no environment was printed")
			}
			line, err := capacityExport(local, shell)
			if err != nil {
				return err
			}
			fmt.Fprintln(rt.stdout, line)
			return nil
		},
	}}}
}
func capacityExport(local agentd.AccountEnvironment, shell string) (string, error) {
	names := map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR", "pi": "PI_CODING_AGENT_DIR", "cursor": "CURSOR_CONFIG_DIR", "grok": "GROK_HOME", "gemini": "HOME", "opencode": "HOME"}
	if names[local.Harness] == "" || local.Variable != names[local.Harness] || !filepath.IsAbs(local.Home) || strings.ContainsAny(local.Home, "\x00\r\n") {
		return "", fmt.Errorf("invalid local account environment")
	}
	if shell == "fish" {
		quoted := strings.ReplaceAll(strings.ReplaceAll(local.Home, `\`, `\\`), "'", `\'`)
		return "set -gx " + local.Variable + " '" + quoted + "'", nil
	}
	return "export " + local.Variable + "='" + strings.ReplaceAll(local.Home, "'", `'"'"'`) + "'", nil
}
