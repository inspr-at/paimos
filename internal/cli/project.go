// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// project list retains the classic inventory shape for agent routing. Project
// identity is the Aeon node UUID; project_key is the imported classic key.
type projectView struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

func (rt *runtime) cmdProject() *Command {
	return &Command{Name: "project", Short: "Projects", Use: "project <list|show|create|update|repos|releases|anchors|tags|decisions>", subs: []*Command{
		rt.cmdProjectList(), rt.cmdProjectCreate(), rt.cmdProjectShow(), rt.cmdProjectUpdate(),
		rt.cmdProjectResource("repos"), rt.cmdProjectResource("releases"), rt.cmdProjectResource("anchors"), rt.cmdProjectResource("tags"),
		rt.cmdProjectDecisions(),
	}}
}

func projectShape(n apiNode) projectView {
	state := n.State
	if state == "" {
		state = "active"
	}
	return projectView{ID: n.ID, Key: projectDisplayKey(n, n.Key), Name: n.Title, Description: n.Body, Status: state}
}

func (rt *runtime) cmdProjectCreate() *Command {
	var name, key, desc, descFile string
	var dryRun bool
	return &Command{Name: "create", Short: "Create a project node", Use: "project create --name NAME [--key KEY] [--description TEXT|--description-file PATH] [--dry-run]",
		addFlags: func(fs *flagSet) {
			fs.string(&name, "name", 0, "project name")
			fs.string(&key, "key", 0, "short project key")
			fs.string(&desc, "description", 0, "project description")
			fs.string(&descFile, "description-file", 0, "description file or - for stdin")
			fs.bool(&dryRun, "dry-run", 0, "show the request without sending")
		}, run: func([]string) error {
			if strings.TrimSpace(name) == "" {
				return usagef("--name is required")
			}
			bodyText, err := rt.readText(desc, descFile, "description")
			if err != nil {
				return err
			}
			if dryRun {
				preview := map[string]any{"name": name}
				if key != "" {
					preview["key"] = strings.ToUpper(key)
				}
				if bodyText != "" {
					preview["description"] = bodyText
				}
				return rt.printJSON(map[string]any{"dry_run": true, "method": "POST", "path": "/api/projects", "body": preview})
			}
			kind, err := rt.kind("project")
			if err != nil {
				return err
			}
			body := map[string]any{"kind_id": kind.ID, "title": name, "body": bodyText, "state": "active", "fields": map[string]any{}}
			if key != "" {
				body["key_prefix"] = strings.ToUpper(key)
				body["fields"] = map[string]any{"project_key": strings.ToUpper(key)}
			}
			var n apiNode
			if err := rt.do(http.MethodPost, "/api/nodes", body, &n); err != nil {
				return err
			}
			view := projectShape(n)
			if rt.jsonOut {
				return rt.printJSON(view)
			}
			_, err = fmt.Fprintf(rt.stdout, "✓ created %s — %s\n", view.Key, view.Name)
			return err
		}}
}

func (rt *runtime) cmdProjectShow() *Command {
	return &Command{Name: "show", Short: "Show a project", Use: "project show <key|id>", minArgs: 1, maxArgs: 1, run: func(args []string) error {
		n, err := rt.projectNode(args[0])
		if err != nil {
			return err
		}
		v := projectShape(n)
		if rt.jsonOut {
			return rt.printJSON(v)
		}
		_, err = fmt.Fprintf(rt.stdout, "%s — %s\nstatus: %s\ndescription: %s\n", v.Key, v.Name, v.Status, v.Description)
		return err
	}}
}

func (rt *runtime) cmdProjectUpdate() *Command {
	var state string
	var dryRun bool
	return &Command{Name: "update", Short: "Update a project's lifecycle state", Use: "project update <key|id> --status STATE [--dry-run]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&state, "status", 0, "active, frozen, archived, or deleted")
			fs.bool(&dryRun, "dry-run", 0, "show request")
		},
		run: func(args []string) error {
			state = strings.ToLower(strings.TrimSpace(state))
			switch state {
			case "active", "frozen", "archived", "deleted":
			case "":
				return usagef("--status is required")
			default:
				return usagef("--status must be active, frozen, archived, or deleted")
			}
			n, err := rt.projectNode(args[0])
			if err != nil {
				return err
			}
			path := "/api/nodes/" + url.PathEscape(n.ID)
			body := map[string]any{"state": state}
			if dryRun {
				return rt.printJSON(map[string]any{"dry_run": true, "method": "PATCH", "path": path, "body": body})
			}
			var changed apiNode
			if err := rt.do(http.MethodPatch, path, body, &changed); err != nil {
				return err
			}
			v := projectShape(changed)
			if rt.jsonOut {
				return rt.printJSON(v)
			}
			_, err = fmt.Fprintf(rt.stdout, "✓ %s is now %s\n", v.Key, v.Status)
			return err
		}}
}

func (rt *runtime) cmdProjectResource(resource string) *Command {
	return &Command{Name: resource, Short: "List project " + resource, Use: "project " + resource + " <key|id>", minArgs: 1, maxArgs: 1, run: func(args []string) error {
		p, err := rt.projectNode(args[0])
		if err != nil {
			return err
		}
		var values []any
		fields := fieldMap(p.Fields)
		if raw, ok := fields[resource].([]any); ok {
			values = raw
		}
		if values == nil {
			if classic, ok := fields["classic"].(map[string]any); ok {
				if raw, ok := classic[resource].([]any); ok {
					values = raw
				}
			}
		}
		if resource == "tags" {
			values = []any{}
			for _, s := range fieldStrings(fields, "tags") {
				values = append(values, map[string]any{"name": s})
			}
		}
		if values == nil {
			values = []any{}
		}
		if rt.jsonOut {
			return rt.printJSON(values)
		}
		if len(values) == 0 {
			_, err = fmt.Fprintln(rt.stdout, "(none)")
			return err
		}
		for _, v := range values {
			if _, err = fmt.Fprintln(rt.stdout, v); err != nil {
				return err
			}
		}
		return nil
	}}
}

func (rt *runtime) cmdProjectList() *Command {
	var status string
	var archived, all bool
	return &Command{Name: "list", Short: "List projects on the current instance", Use: "project list [--status active|frozen|archived|deleted|all]", addFlags: func(fs *flagSet) {
		fs.bool(&archived, "archived", 0, "list archived projects")
		fs.bool(&all, "all", 0, "list every project status")
		fs.string(&status, "status", 0, "active, frozen, archived, deleted, or all")
	}, run: func([]string) error {
		if (archived && all) || (status != "" && (archived || all)) {
			return usagef("--archived, --all, and --status are mutually exclusive")
		}
		status = strings.ToLower(strings.TrimSpace(status))
		if archived {
			status = "archived"
		}
		if all {
			status = "all"
		}
		switch status {
		case "", "active", "frozen", "archived", "deleted", "all":
		default:
			return usagef("--status must be active, frozen, archived, deleted, or all")
		}
		kind, err := rt.kind("project")
		if err != nil {
			return err
		}
		nodes, err := rt.walkNodes(url.Values{"kind_id": {kind.ID}}, nil)
		if err != nil {
			return err
		}
		views := make([]projectView, 0, len(nodes))
		for _, n := range nodes {
			state := n.State
			if state == "" {
				state = "active"
			}
			if status == "" || status == "active" {
				if state != "active" {
					continue
				}
			} else if status != "all" && state != status {
				continue
			}
			views = append(views, projectView{ID: n.ID, Key: projectDisplayKey(n, n.Key), Name: n.Title, Description: n.Body, Status: state})
		}
		sort.Slice(views, func(i, j int) bool { return views[i].Key < views[j].Key })
		if rt.jsonOut {
			return rt.printJSON(views)
		}
		if len(views) == 0 {
			_, err = fmt.Fprintln(rt.stdout, "(no projects)")
			return err
		}
		if _, err = fmt.Fprintln(rt.stdout, "KEY           STATUS     NAME"); err != nil {
			return err
		}
		for _, view := range views {
			if _, err = fmt.Fprintf(rt.stdout, "%-13s %-10s %s\n", view.Key, view.Status, view.Name); err != nil {
				return err
			}
		}
		return nil
	}}
}
