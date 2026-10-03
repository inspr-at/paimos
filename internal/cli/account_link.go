// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

// Existing short-job harness runner: an optional paired account prompt never
// changes process ownership, stdout, exit status or required cleanup.
func (rt *runtime) startAccountLink(ctx context.Context, o heartbeatOptions) func() {
	noop := func() {}
	var variable string
	switch o.Harness {
	case "codex":
		variable = "CODEX_HOME"
	case "claude":
		variable = "CLAUDE_CONFIG_DIR"
	case "pi":
		variable = "PI_CODING_AGENT_DIR"
	case "grok":
		variable = "GROK_HOME"
	case "cursor":
		variable = "CURSOR_CONFIG_DIR"
	default:
		return noop
	}
	var c agentdwire.Client
	if o.LinkSocket != "" && o.LinkSetupRoot != "" {
		return noop
	}
	if o.LinkSocket != "" {
		c = agentdwire.Client{Socket: o.LinkSocket, TokenFile: o.LinkSocket + ".token"}
	} else {
		root := o.LinkSetupRoot
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return noop
			}
			root, err = agentsetup.DefaultStateRoot(goruntime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
			if err != nil {
				return noop
			}
		}
		var err error
		c, err = agentdwire.OpenClient(filepath.Join(root, "daemon"))
		if err != nil {
			return noop
		}
	}
	home := os.Getenv(variable)
	if home == "" {
		return noop
	} // Never confuse a default login with a private enrolled one.
	in := agentd.AccountLinkRequest{AccountID: o.LinkAccountID, Harness: o.Harness, Home: home, Operation: "offer"}
	op, cancel := context.WithTimeout(ctx, 8*time.Second)
	v, err := c.AccountLink(op, in)
	cancel()
	if err != nil || v.State != "pending" || v.RequestID == "" {
		return noop
	}
	if v.ShowPrompt {
		line, err := agentd.AccountLinkPrompt(v)
		if err != nil {
			return noop
		}
		active, err := rt.api()
		if err != nil || v.URI != strings.TrimRight(active.BaseURL, "/")+"/link" {
			return noop
		}
		if _, err = fmt.Fprintln(rt.stderr, line); err != nil {
			return noop
		}
	}
	in.Operation, in.RequestID, in.AccountID = "poll", v.RequestID, v.AccountID
	return rt.watchAccountLink(ctx, c, in, 5*time.Second)
}

type accountLinkClient interface {
	AccountLink(context.Context, agentd.AccountLinkRequest) (agentsetup.AccountLinkView, error)
}

func (rt *runtime) watchAccountLink(ctx context.Context, c accountLinkClient, in agentd.AccountLinkRequest, interval time.Duration) func() {
	watchCtx, stop := context.WithTimeout(ctx, 10*time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				v, err := c.AccountLink(watchCtx, in)
				if err != nil {
					return
				}
				if v.State == "linked" {
					if line, err := agentd.AccountLinkedLine(v); v.ShowResult && err == nil {
						fmt.Fprintln(rt.stderr, line)
					}
					return
				}
				if v.State != "pending" {
					return
				}
			}
		}
	}()
	return func() { stop(); <-done }
}
