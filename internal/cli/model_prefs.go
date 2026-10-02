// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (rt *runtime) cmdModelPrefs() *Command {
	var project string
	return &Command{Name: "prefs", Short: "Read effective model preferences", Use: "model prefs [--project KEY]", maxArgs: 0, addFlags: func(fs *flagSet) { fs.string(&project, "project", 0, "project key or UUID") }, run: func([]string) error {
		path := "/api/model-preferences"
		if strings.TrimSpace(project) != "" {
			node, err := rt.projectNode(project)
			if err != nil {
				return err
			}
			path += "?project_id=" + url.QueryEscape(node.ID)
		}
		var out struct {
			Views map[string]*struct {
				Residency struct {
					Value, SetBy string
					LoosenedLock bool `json:"loosened_lock"`
				}
				Rows []struct {
					KindID          string `json:"kind_id"`
					SetBy           string `json:"set_by"`
					LockedBy        string `json:"locked_by"`
					Normal, Complex struct {
						Label             string
						UnavailableReason string `json:"unavailable_reason"`
					}
				}
			} `json:"views"`
			Kinds []struct{ ID, Label string } `json:"kinds"`
		}
		if rt.jsonOut {
			var raw map[string]any
			if err := rt.do(http.MethodGet, path, nil, &raw); err != nil {
				return err
			}
			return rt.printJSON(raw)
		}
		if err := rt.do(http.MethodGet, path, nil, &out); err != nil {
			return err
		}
		labels := map[string]string{}
		for _, kind := range out.Kinds {
			labels[kind.ID] = kind.Label
		}
		for _, level := range []string{"default", "person", "project"} {
			view := out.Views[level]
			if view == nil {
				continue
			}
			fmt.Fprintf(rt.stdout, "%s · providers %s\n", level, view.Residency.Value)
			if view.Residency.LoosenedLock {
				fmt.Fprintln(rt.stdout, "Warning: this choice loosens a provider lock")
			}
			for _, row := range view.Rows {
				fmt.Fprintf(rt.stdout, "%s\t%s\t%s\tset by %s", labels[row.KindID], row.Normal.Label, row.Complex.Label, row.SetBy)
				if row.LockedBy != "" {
					fmt.Fprintf(rt.stdout, " · locked by %s", row.LockedBy)
				}
				fmt.Fprintln(rt.stdout)
				for _, reason := range []string{row.Normal.UnavailableReason, row.Complex.UnavailableReason} {
					if reason != "" {
						fmt.Fprintf(rt.stdout, "  Warning: %s\n", reason)
					}
				}
			}
		}
		return nil
	}}
}
