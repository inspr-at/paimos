// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
)

type agentsPlanResponse struct {
	agentplan.Plan
	PrincipalID  string         `json:"principal_id"`
	Running      map[string]int `json:"running"`
	RunningTotal int            `json:"running_total"`
	Source       string         `json:"source"`
	UpdatedAt    *time.Time     `json:"updated_at"`
}

func (rt *runtime) cmdAgents() *Command {
	return &Command{Name: "agents", Short: "Agent start plan", Use: "agents plan", subs: []*Command{{
		Name: "plan", Short: "Show the person's plan and running counts", Use: "agents plan [--json]",
		Long: "Reads your plan, or your agent key creator's plan with agents.plan.read. Lowering the plan lets running work finish; account capacity is checked separately.",
		run: func(args []string) error {
			var out agentsPlanResponse
			if err := rt.do("GET", "/api/agents/plan", nil, &out); err != nil {
				return err
			}
			if err := out.Plan.Validate(); err != nil {
				return rt.fail(err, "")
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			fmt.Fprintf(rt.stdout, "Planned total: %d · running: %d\n", out.Total, out.RunningTotal)
			// Include known harnesses even if they have no limits or sessions.
			harnesses := []string{"claude", "codex", "cursor", "gemini", "grok", "opencode", "pi"}
			for harness := range out.Running {
				if !slices.Contains(harnesses, harness) {
					harnesses = append(harnesses, harness)
				}
			}
			slices.Sort(harnesses)
			for _, harness := range harnesses {
				label := agentplan.HarnessLabel(harness)
				if label == "" {
					label = harness
				}
				limit := "No limit"
				if l, ok := out.Limits[harness]; ok {
					switch l.Mode {
					case agentplan.Off:
						limit = "Off"
					case agentplan.AtMost:
						limit = "At most " + strconv.Itoa(l.Value)
					}
				}
				fmt.Fprintf(rt.stdout, "%s: %s · %d running\n", label, limit, out.Running[harness])
			}
			return nil
		},
	}}}
}
