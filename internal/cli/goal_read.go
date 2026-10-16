// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
)

func (rt *runtime) goalRead(path string) error {
	var out json.RawMessage
	if err := rt.do(http.MethodGet, path, nil, &out); err != nil {
		return err
	}
	return rt.printJSON(out)
}
func (rt *runtime) cmdGoalGet() *Command {
	return &Command{Name: "get", Short: "Read a visible goal and its current rollup", Use: "goal get KEY [--json]", minArgs: 1, maxArgs: 1, run: func(args []string) error { return rt.goalRead("/api/goals/" + url.PathEscape(args[0])) }}
}
func (rt *runtime) cmdGoalHistory() *Command {
	var outcome, cursor, limit string
	return &Command{Name: "history", Short: "Read immutable goal or outcome history", Use: "goal history KEY [--outcome UUID] [--cursor ID] [--limit N]", minArgs: 1, maxArgs: 1, addFlags: func(fs *flagSet) {
		fs.string(&outcome, "outcome", 0, "immutable outcome UUID, including after a move")
		fs.string(&cursor, "cursor", 0, "exclusive event cursor")
		fs.string(&limit, "limit", 0, "1–50")
	}, run: func(args []string) error {
		q := url.Values{}
		for key, value := range map[string]string{"outcome": outcome, "cursor": cursor, "limit": limit} {
			if value != "" {
				q.Set(key, value)
			}
		}
		return rt.goalRead("/api/goals/" + url.PathEscape(args[0]) + "/history?" + q.Encode())
	}}
}
func (rt *runtime) cmdGoalList() *Command {
	var project, group, search, state, sortBy, dir, cursor, limit string
	var hideDone bool
	return &Command{Name: "list", Short: "List visible goals with totals and project badges", Use: "goal list [--project KEY] [--group UUID|none] [--q TEXT] [--sort eta|name|progress|project|moving|queued|done] [--json]", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "home or contributing project")
			fs.string(&group, "group", 0, "project group UUID or none")
			fs.string(&search, "q", 0, "goal, next step, project, outcome, ticket key or note")
			fs.string(&state, "state", 0, "outcome state filter")
			fs.string(&sortBy, "sort", 0, "eta, name, progress, project, moving, queued or done")
			fs.string(&dir, "dir", 0, "asc or desc")
			fs.string(&cursor, "cursor", 0, "opaque continuation from the same filters")
			fs.string(&limit, "limit", 0, "1–100")
			fs.bool(&hideDone, "hide-done", 0, "hide goals whose visible outcomes are all done")
		},
		run: func([]string) error {
			q := url.Values{}
			for key, value := range map[string]string{"project": project, "group": group, "q": search, "state": state, "sort": sortBy, "dir": dir, "cursor": cursor, "limit": limit} {
				if value != "" {
					q.Set(key, value)
				}
			}
			if hideDone {
				q.Set("hide_done", "true")
			}
			return rt.goalRead("/api/goals?" + q.Encode())
		}}
}

func (rt *runtime) cmdGoalSeries() *Command {
	var span string
	return &Command{Name: "series", Short: "Read event-backed goal progress", Use: "goal series KEY --range 24h|7d|30d [--json]", minArgs: 1, maxArgs: 1, addFlags: func(fs *flagSet) { fs.string(&span, "range", 0, "24h, 7d or 30d (default 24h)") }, run: func(args []string) error {
		q := url.Values{}
		if span != "" {
			q.Set("range", span)
		}
		return rt.goalRead("/api/goals/" + url.PathEscape(args[0]) + "/series?" + q.Encode())
	}}
}
