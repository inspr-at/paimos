// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"fmt"
	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"io"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
)

func (rt *runtime) cmdStatusline() *Command {
	var state, account string
	return &Command{Name: "statusline", Short: "Show Aeon's plan and relay Claude quota to the local daemon", Use: "statusline --state-dir PATH --account-id UUID",
		addFlags: func(f *flagSet) {
			f.string(&state, "state-dir", 0, "private daemon state")
			f.string(&account, "account-id", 0, "enrolled Claude account UUID")
		},
		run: func([]string) error {
			// This command never installs itself or writes Claude settings. Failure
			// renders one neutral line and never prints raw stdin or local paths.
			plan := "Aeon · plan unavailable"
			defer func() { fmt.Fprintln(rt.stdout, plan) }()
			if state == "" || !validUUID(account) {
				return nil
			}
			raw, err := io.ReadAll(io.LimitReader(rt.stdin, (64<<10)+1))
			if err != nil || len(raw) > 64<<10 {
				return nil
			}
			readings := capacity.ClaudeStatusline(raw, time.Now().UTC())
			raw = nil
			c, err := agentdwire.OpenClient(state)
			if err != nil {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
			defer cancel()
			out, err := c.Statusline(ctx, agentd.StatuslineRequest{AccountID: account, Readings: readings})
			if err == nil && safeStatusline(out.Plan) {
				plan = out.Plan
			}
			return nil
		},
	}
}
func safeStatusline(v string) bool {
	if len(v) == 0 || len(v) > 240 {
		return false
	}
	for _, r := range v {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
