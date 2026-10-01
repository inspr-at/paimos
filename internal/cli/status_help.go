// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func (rt *runtime) cmdStatus() *Command {
	var project string
	return &Command{Name: "status", Short: "Ticket status definitions", Use: "status <command>", subs: []*Command{
		{Name: "help", Short: "Read status meanings and live workspace limits", Use: "status help [--project KEY] [--json]", addFlags: func(fs *flagSet) { fs.string(&project, "project", 'p', "resolve a project's autopilot override") }, run: func(args []string) error {
			path := "/api/status/help"
			if project != "" {
				node, err := rt.projectNode(project)
				if err != nil {
					return err
				}
				path += "?project_id=" + url.QueryEscape(node.ID)
			}
			var help json.RawMessage
			if err := rt.do("GET", path, nil, &help); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(help)
			}
			var view struct {
				Definitions []struct {
					Label   string `json:"label"`
					Meaning string `json:"meaning"`
					SetBy   string `json:"set_by"`
				} `json:"definitions"`
				Queued struct {
					Label   string `json:"label"`
					Meaning string `json:"meaning"`
				} `json:"queued"`
				Autopilot struct {
					EffectiveEnabled bool `json:"effective_enabled"`
					Rules            map[string]struct {
						Enabled bool `json:"enabled"`
						Days    int  `json:"days"`
					} `json:"rules"`
				} `json:"autopilot"`
			}
			if err := json.Unmarshal(help, &view); err != nil {
				return err
			}
			for _, def := range view.Definitions {
				fmt.Fprintf(rt.stdout, "%s — %s (set by: %s)\n", def.Label, def.Meaning, def.SetBy)
			}
			fmt.Fprintf(rt.stdout, "%s — %s\n", view.Queued.Label, view.Queued.Meaning)
			fmt.Fprintf(rt.stdout, "Status autopilot: %t\n", view.Autopilot.EffectiveEnabled)
			for _, key := range []string{"new", "backlog", "blocked", "progress", "done", "publish", "accept"} {
				rule := view.Autopilot.Rules[key]
				word := "Off"
				if view.Autopilot.EffectiveEnabled && rule.Enabled {
					word = "On"
				}
				limit := "on publish"
				if rule.Days > 0 {
					limit = fmt.Sprintf("%d days", rule.Days)
				}
				fmt.Fprintf(rt.stdout, "  %s: %s, %s\n", strings.ToUpper(key[:1])+key[1:], word, limit)
			}
			return nil
		}},
	}}
}
