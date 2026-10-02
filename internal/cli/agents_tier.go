// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/servicetier"
)

func tierRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func (rt *runtime) cmdAgentsTier() *Command {
	tier := &Command{Name: "tier", Short: "Show, set or request a session service tier", Use: "agents tier <show|set|ask|approve|decline>"}
	for _, action := range []string{"show", "set", "ask", "approve", "decline"} {
		tier.subs = append(tier.subs, rt.agentTierAction(action))
	}
	return &Command{Name: "agents", Short: "Agent session settings", Use: "agents <command>", subs: []*Command{tier}}
}
func (rt *runtime) agentTierAction(action string) *Command {
	var project, session, tier, reason, request, decisionRequest string
	return &Command{Name: action, Short: action + " service tier", Use: "agents tier " + action + " --project KEY --session UUID [--tier default|fast|fastest]", addFlags: func(fs *flagSet) {
		fs.string(&project, "project", 'p', "project key or id")
		fs.string(&session, "session", 0, "session UUID")
		fs.string(&tier, "tier", 0, "Default, Fast or Fastest")
		fs.string(&reason, "reason", 0, "why the agent asks")
		fs.string(&request, "request-id", 0, "idempotent operation UUID (generated if omitted)")
		fs.string(&decisionRequest, "decision-request", 0, "agent request UUID to approve or decline")
	}, run: func(args []string) error {
		if len(args) > 0 || project == "" || !validUUID(session) {
			return usagef("--project and a session UUID are required")
		}
		projectID, err := rt.harnessProject(project)
		if err != nil {
			return err
		}
		path := harnessPath(projectID, session) + "/tier"
		if action == "show" {
			var out harness.TierState
			if err = rt.harnessDo(http.MethodGet, path, "", nil, &out); err != nil {
				return err
			}
			return rt.printTierState(out)
		}
		tier = strings.ToLower(tier)
		if !servicetier.Valid(tier) {
			return usagef("--tier must be default, fast or fastest")
		}
		if request == "" {
			request, err = tierRequestID()
			if err != nil {
				return err
			}
		}
		if !validUUID(request) {
			return usagef("--request-id must be a UUID")
		}
		body := map[string]any{"request_id": request, "tier": tier}
		var out any
		if action == "ask" {
			if strings.TrimSpace(reason) == "" {
				return usagef("--reason is required")
			}
			body["reason"] = reason
			path += "/ask"
		} else {
			var state harness.TierState
			if err = rt.harnessDo(http.MethodGet, path, "", nil, &state); err != nil {
				return err
			}
			var s harness.Session
			if err = rt.harnessDo(http.MethodGet, harnessPath(projectID, session), "", nil, &s); err != nil {
				return err
			}
			body["expected_revision"], body["expected_ownership"] = state.Revision, s.ProcessOwnership
			if action == "approve" || action == "decline" {
				if !validUUID(decisionRequest) {
					return usagef("--decision-request must be the agent request UUID")
				}
				body["decision"] = action
				path += "/requests/" + url.PathEscape(decisionRequest) + "/decision"
			}
		}
		if err = rt.harnessDo(http.MethodPost, path, "", body, &out); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(out)
		}
		return rt.printJSON(out)
	}}
}
func (rt *runtime) printTierState(s harness.TierState) error {
	if rt.jsonOut {
		return rt.printJSON(s)
	}
	active := "Unknown"
	if s.Active != nil {
		active = map[string]string{"default": "Default", "fast": "Fast", "fastest": "Fastest"}[*s.Active]
	}
	if _, err := fmt.Fprintf(rt.stdout, "Service tier: %s\n", active); err != nil {
		return err
	}
	if s.Pending != nil {
		fmt.Fprintf(rt.stdout, "Pending: %s (waiting for the daemon)\n", s.Pending.Value)
	}
	if s.ReadOnly {
		fmt.Fprintf(rt.stdout, "Read-only: %s\n", s.ReadOnlyReason)
	}
	for _, r := range s.Reports {
		fmt.Fprintf(rt.stdout, "%s · %s · checked %s\n", r.Model, r.HarnessVersion, r.CheckedAt.Format("2006-01-02T15:04:05Z07:00"))
		for _, t := range r.Tiers {
			if t.Offered && t.PriceMultiplier != nil {
				fmt.Fprintf(rt.stdout, "  %s: ×%g price · %s\n", t.Name, *t.PriceMultiplier, t.Mechanism)
			} else {
				fmt.Fprintf(rt.stdout, "  %s: not offered · %s\n", t.Name, t.Reason)
			}
		}
	}
	return nil
}
