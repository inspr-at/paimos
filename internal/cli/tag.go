// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var tagColors = map[string]bool{"gray": true, "slate": true, "blue": true, "indigo": true, "purple": true, "pink": true, "red": true, "orange": true, "yellow": true, "green": true, "teal": true, "cyan": true}

type tagView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	System      bool   `json:"system"`
	CreatedAt   string `json:"created_at,omitempty"`
}

func tagShape(n apiNode) tagView {
	f := fieldMap(n.Fields)
	return tagView{ID: n.ID, Name: n.Title, Color: fieldString(f, "color"), Description: n.Body, CreatedAt: n.CreatedAt.Format("2006-01-02T15:04:05Z07:00")}
}

func (rt *runtime) tagKind(create bool) (apiKind, error) {
	t, err := rt.loadKinds()
	if err != nil {
		return apiKind{}, err
	}
	if k, ok := t.bySlug["tag"]; ok {
		return k, nil
	}
	if !create {
		return apiKind{}, nil
	}
	var k apiKind
	if err := rt.do(http.MethodPost, "/api/kinds", map[string]any{"slug": "tag", "label": "Tag", "short_prefix": "TAG", "icon": "tag", "field_schema": map[string]any{}}, &k); err != nil {
		return apiKind{}, err
	}
	rt.kinds = nil
	return k, nil
}

func (rt *runtime) tags() ([]apiNode, error) {
	k, err := rt.tagKind(false)
	if err != nil {
		return nil, err
	}
	if k.ID == "" {
		return []apiNode{}, nil
	}
	return rt.walkNodes(url.Values{"kind_id": {k.ID}}, nil)
}

func (rt *runtime) tagByID(id string) (apiNode, error) {
	if !validUUID(id) {
		return apiNode{}, usagef("tag id must be an Aeon UUID")
	}
	var n apiNode
	if err := rt.do(http.MethodGet, "/api/nodes/"+id, nil, &n); err != nil {
		return n, err
	}
	k, err := rt.tagKind(false)
	if err != nil {
		return n, err
	}
	if k.ID == "" || n.KindID != k.ID {
		return n, rt.fail(fmt.Errorf("tag %s not found", id), "")
	}
	return n, nil
}

func (rt *runtime) cmdTag() *Command {
	return &Command{Name: "tag", Short: "List and manage tenant tags", Use: "tag <list|create|update|delete>", subs: []*Command{rt.cmdTagList(), rt.cmdTagCreate(), rt.cmdTagUpdate(), rt.cmdTagDelete()}}
}

func (rt *runtime) cmdTagList() *Command {
	var project string
	return &Command{Name: "list", Short: "List tags", Use: "tag list [--project KEY]", addFlags: func(fs *flagSet) { fs.string(&project, "project", 0, "project key") }, run: func([]string) error {
		var projectTags map[string]bool
		if project != "" {
			p, err := rt.projectNode(project)
			if err != nil {
				return err
			}
			projectTags = map[string]bool{}
			for _, s := range fieldStrings(fieldMap(p.Fields), "tags") {
				projectTags[s] = true
			}
		}
		nodes, err := rt.tags()
		if err != nil {
			return err
		}
		views := make([]tagView, 0, len(nodes))
		for _, n := range nodes {
			if projectTags == nil || projectTags[n.Title] {
				views = append(views, tagShape(n))
			}
		}
		if rt.jsonOut {
			return rt.printJSON(views)
		}
		if len(views) == 0 {
			_, err = fmt.Fprintln(rt.stdout, "(no tags)")
			return err
		}
		for _, v := range views {
			if _, err = fmt.Fprintf(rt.stdout, "%s  %s  %s\n", v.ID, v.Name, v.Color); err != nil {
				return err
			}
		}
		return nil
	}}
}

func (rt *runtime) cmdTagCreate() *Command {
	var project, name, color, desc, descFile string
	return &Command{Name: "create", Short: "Create a tenant tag", Use: "tag create --name NAME [--color COLOR] [--project KEY]", addFlags: func(fs *flagSet) {
		fs.string(&project, "project", 0, "attach to project")
		fs.string(&name, "name", 0, "tag name")
		fs.string(&color, "color", 0, "tag color")
		fs.string(&desc, "description", 0, "description")
		fs.string(&descFile, "description-file", 0, "description file")
	}, run: func([]string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return usagef("--name is required")
		}
		if color != "" && !tagColors[color] {
			return usagef("invalid tag color %q", color)
		}
		bodyText, err := rt.readText(desc, descFile, "description")
		if err != nil {
			return err
		}
		existing, err := rt.tags()
		if err != nil {
			return err
		}
		for _, tag := range existing {
			if strings.EqualFold(tag.Title, name) {
				return rt.fail(fmt.Errorf("tag %q already exists", name), "")
			}
		}
		var p apiNode
		if project != "" {
			p, err = rt.projectNode(project)
			if err != nil {
				return err
			}
		}
		k, err := rt.tagKind(true)
		if err != nil {
			return err
		}
		var n apiNode
		if err := rt.do(http.MethodPost, "/api/nodes", map[string]any{"kind_id": k.ID, "title": name, "body": bodyText, "fields": map[string]any{"color": color}}, &n); err != nil {
			return err
		}
		attached := false
		if project != "" {
			fields := fieldMap(p.Fields)
			tags := fieldStrings(fields, "tags")
			found := false
			for _, tag := range tags {
				if tag == name {
					found = true
				}
			}
			if !found {
				tags = append(tags, name)
				fields["tags"] = tags
				if err := rt.patchNode(p, map[string]any{"fields": fields}, nil); err != nil {
					fmt.Fprintf(rt.stderr, "created tag %s (#%s), but attachment to project %s failed; the tag remains created\n", name, n.ID, project)
					return err
				}
			}
			attached = true
		}
		v := tagShape(n)
		if rt.jsonOut {
			out := map[string]any{"tag": v, "attached": attached}
			if attached {
				out["project_id"] = p.ID
			}
			return rt.printJSON(out)
		}
		_, err = fmt.Fprintf(rt.stdout, "✓ created tag %s (#%s)\n", v.Name, v.ID)
		return err
	}}
}

func (rt *runtime) cmdTagUpdate() *Command {
	var name, color, desc, descFile string
	return &Command{Name: "update", Short: "Update a tag", Use: "tag update <tag-id> [--name NAME] [--color COLOR] [--description TEXT]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&name, "name", 0, "new name")
			fs.string(&color, "color", 0, "new color")
			fs.string(&desc, "description", 0, "new description")
			fs.string(&descFile, "description-file", 0, "new description file")
		},
		run: func(args []string) error {
			if name == "" && color == "" && desc == "" && descFile == "" {
				return usagef("at least one tag field is required")
			}
			if color != "" && !tagColors[color] {
				return usagef("invalid tag color %q", color)
			}
			n, err := rt.tagByID(args[0])
			if err != nil {
				return err
			}
			body := map[string]any{}
			if name != "" {
				body["name"] = strings.TrimSpace(name)
			}
			if color != "" {
				body["color"] = color
			}
			if desc != "" || descFile != "" {
				text, err := rt.readText(desc, descFile, "description")
				if err != nil {
					return err
				}
				body["description"] = text
			}
			var changed apiNode
			if err := rt.do(http.MethodPatch, "/api/tags/"+n.ID, body, &changed); err != nil {
				return err
			}
			v := tagShape(changed)
			if rt.jsonOut {
				return rt.printJSON(v)
			}
			_, err = fmt.Fprintf(rt.stdout, "✓ updated tag %s (#%s)\n", v.Name, v.ID)
			return err
		}}
}

func (rt *runtime) cmdTagDelete() *Command {
	var yes bool
	return &Command{Name: "delete", Short: "Delete a tag", Use: "tag delete <tag-id> --yes", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) { fs.bool(&yes, "yes", 0, "confirm deletion") }, run: func(args []string) error {
			if !yes {
				return usagef("pass --yes to delete a tag")
			}
			n, err := rt.tagByID(args[0])
			if err != nil {
				return err
			}
			if err := rt.do(http.MethodDelete, "/api/tags/"+n.ID, nil, nil); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(map[string]any{"ok": true, "tag_id": n.ID, "tag": n.Title, "action": "delete"})
			}
			_, err = fmt.Fprintf(rt.stdout, "✓ deleted tag %s (#%s)\n", n.Title, n.ID)
			return err
		}}
}
