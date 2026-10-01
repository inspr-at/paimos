// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/inspr-at/paimos/internal/workqueue"
)

type cliQueueEntry struct {
	NodeID string            `json:"node_id"`
	Key    string            `json:"key"`
	Title  string            `json:"title"`
	State  string            `json:"state"`
	Queued *workqueue.Queued `json:"queued"`
}
type cliQueuePage struct {
	Items    []cliQueueEntry `json:"items"`
	Count    int             `json:"count"`
	Manual   bool            `json:"manual_order"`
	Capacity struct {
		QueuedHours float64  `json:"queued_hours"`
		Parallel    int      `json:"parallel_runs"`
		WorkHours   *float64 `json:"work_hours"`
		Warning     bool     `json:"warning"`
	} `json:"capacity"`
}

func (rt *runtime) cmdQueue() *Command {
	return &Command{Name: "queue", Short: "Manage the ticket work queue", Use: "queue <list|add|remove|move|reset|next|readiness>", subs: []*Command{
		rt.queueListCommand(), rt.queueAddCommand(), rt.queueNodeCommand("remove"), rt.queueNodeCommand("move"), rt.queueNodeCommand("readiness"), rt.queueNextCommand(),
		{Name: "reset", Short: "Restore priority then FIFO order", Use: "queue reset", maxArgs: 0, run: func([]string) error {
			var out cliQueuePage
			if err := rt.do(http.MethodPost, "/api/queue/reset", map[string]any{}, &out); err != nil {
				return err
			}
			return rt.printQueue(out)
		}},
	}}
}
func (rt *runtime) queueListCommand() *Command {
	var project string
	return &Command{Name: "list", Short: "List queued ticket runs", Use: "queue list [--project KEY]", maxArgs: 0, addFlags: func(fs *flagSet) { fs.string(&project, "project", 'p', "project key or UUID") }, run: func([]string) error {
		path := "/api/queue"
		if project != "" {
			n, err := rt.nodeRef(project)
			if err != nil {
				return err
			}
			path += "?project_id=" + url.QueryEscape(n.ID)
		}
		var out cliQueuePage
		if err := rt.do(http.MethodGet, path, nil, &out); err != nil {
			return err
		}
		return rt.printQueue(out)
	}}
}
func queueTargetFlags(fs *flagSet, agent, profile, account *string) {
	fs.string(agent, "agent", 0, "target agent principal UUID")
	fs.string(profile, "profile", 0, "model profile UUID")
	fs.string(account, "account", 0, "optional requested account UUID")
}
func queueTargetBody(agent, profile, account string) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range map[string]string{"agent_principal_id": agent, "model_profile_id": profile, "requested_account_id": account} {
		if v != "" {
			if !validUUID(v) {
				return nil, usagef("%s must be a UUID", k)
			}
			out[k] = v
		}
	}
	if agent != "" && profile == "" || account != "" && agent == "" {
		return nil, usagef("--agent requires --profile; --account requires --agent")
	}
	return out, nil
}
func (rt *runtime) queueAddCommand() *Command {
	var agent, profile, account string
	return &Command{Name: "add", Short: "Queue ready work; optionally Start now on an agent", Use: "queue add <ticket-ref> [--agent UUID --profile UUID --account UUID]", minArgs: 1, maxArgs: 1, addFlags: func(fs *flagSet) { queueTargetFlags(fs, &agent, &profile, &account) }, run: func(args []string) error {
		body, err := queueTargetBody(agent, profile, account)
		if err != nil {
			return err
		}
		if profile != "" && agent == "" {
			return usagef("--profile requires --agent when adding")
		}
		n, err := rt.nodeRef(args[0])
		if err != nil {
			return err
		}
		body["node_id"] = n.ID
		var out cliQueueEntry
		if err = rt.do(http.MethodPost, "/api/queue", body, &out); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(out)
		}
		return rt.printQueueEntry(out)
	}}
}
func (rt *runtime) queueNodeCommand(action string) *Command {
	c := &Command{Name: action, Short: map[string]string{"remove": "Remove queued work", "move": "Move queued work to a one-based position", "readiness": "Explain missing readiness fields and suggest an estimate"}[action], Use: "queue " + action + " <ticket-ref>", minArgs: 1, maxArgs: 1}
	if action == "move" {
		c.Use += " <position>"
		c.minArgs = 2
		c.maxArgs = 2
	}
	c.run = func(args []string) error {
		position := 0
		if action == "move" {
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 {
				return usagef("position must be a positive integer")
			}
			position = n
		}
		n, err := rt.nodeRef(args[0])
		if err != nil {
			return err
		}
		path := "/api/queue/" + n.ID
		switch action {
		case "move":
			var out cliQueuePage
			if err = rt.do(http.MethodPost, path+"/move", map[string]int{"position": position}, &out); err != nil {
				return err
			}
			return rt.printQueue(out)
		case "readiness":
			var out workqueue.Readiness
			if err = rt.do(http.MethodGet, path+"/readiness", nil, &out); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			_, err = fmt.Fprintf(rt.stdout, "%s: ready=%t; missing=%v; suggested estimate=%g h\n", n.Key, out.Ready, out.Missing, out.SuggestedEstimateHours)
			return err
		default:
			var out struct {
				Removed bool `json:"removed"`
			}
			if err = rt.do(http.MethodDelete, path, nil, &out); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			_, err = fmt.Fprintf(rt.stdout, "%s removed=%t\n", n.Key, out.Removed)
			return err
		}
	}
	return c
}
func (rt *runtime) queueNextCommand() *Command {
	var agent, profile, account string
	return &Command{Name: "next", Short: "Route the next ready ticket to eligible capacity", Use: "queue next [--agent UUID --profile UUID --account UUID]", maxArgs: 0, addFlags: func(fs *flagSet) { queueTargetFlags(fs, &agent, &profile, &account) }, run: func([]string) error {
		body, err := queueTargetBody(agent, profile, account)
		if err != nil {
			return err
		}
		var out struct {
			Entry *cliQueueEntry `json:"entry"`
		}
		if err = rt.do(http.MethodPost, "/api/queue/next", body, &out); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(out)
		}
		if out.Entry == nil {
			_, err = fmt.Fprintln(rt.stdout, "No ready work with available capacity")
			return err
		}
		return rt.printQueueEntry(*out.Entry)
	}}
}
func (rt *runtime) printQueue(out cliQueuePage) error {
	if rt.jsonOut {
		return rt.printJSON(out)
	}
	for _, e := range out.Items {
		if err := rt.printQueueEntry(e); err != nil {
			return err
		}
	}
	if out.Capacity.WorkHours != nil {
		_, err := fmt.Fprintf(rt.stdout, "%d queued; ~%g h of work at current capacity\n", out.Count, *out.Capacity.WorkHours)
		return err
	}
	_, err := fmt.Fprintf(rt.stdout, "%d queued; capacity unavailable\n", out.Count)
	return err
}
func (rt *runtime) printQueueEntry(e cliQueueEntry) error {
	if e.Queued == nil {
		_, err := fmt.Fprintf(rt.stdout, "%s %s\n", e.Key, e.Title)
		return err
	}
	_, err := fmt.Fprintf(rt.stdout, "#%d %s %s · queued by %s\n", e.Queued.Position, e.Key, e.Title, e.Queued.By.Name)
	return err
}
