// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type compatApplyPlan struct {
	Project string `yaml:"project" json:"project"`
	Create  []struct {
		Name               string         `yaml:"name" json:"name"`
		Title              string         `yaml:"title" json:"title"`
		Type               string         `yaml:"type" json:"type"`
		Status             string         `yaml:"status" json:"status"`
		Priority           string         `yaml:"priority" json:"priority"`
		Description        string         `yaml:"description" json:"description"`
		AcceptanceCriteria string         `yaml:"acceptance_criteria" json:"acceptance_criteria"`
		Notes              string         `yaml:"notes" json:"notes"`
		CostUnit           string         `yaml:"cost_unit" json:"cost_unit"`
		Release            string         `yaml:"release" json:"release"`
		Parent             string         `yaml:"parent" json:"parent"`
		Fields             map[string]any `yaml:"fields" json:"fields"`
	} `yaml:"create" json:"create"`
	Update []struct {
		Ref    string         `yaml:"ref" json:"ref"`
		Fields map[string]any `yaml:"fields" json:"fields"`
	} `yaml:"update" json:"update"`
	Relations []struct {
		Source string `yaml:"source" json:"source"`
		Type   string `yaml:"type" json:"type"`
		Target string `yaml:"target" json:"target"`
	} `yaml:"relations" json:"relations"`
}

func (rt *runtime) cmdApply() *Command {
	var path string
	var dryRun bool
	return &Command{Name: "apply", Short: "Apply a declarative YAML plan", Use: "apply --from-file PATH [--dry-run]", addFlags: func(fs *flagSet) {
		fs.string(&path, "from-file", 'f', "YAML plan path, or - for stdin")
		fs.bool(&dryRun, "dry-run", 0, "parse without sending")
	}, run: func([]string) error {
		if path == "" {
			return usagef("--from-file is required")
		}
		var raw []byte
		var err error
		if path == "-" {
			raw, err = io.ReadAll(io.LimitReader(rt.stdin, 8<<20+1))
		} else {
			raw, err = os.ReadFile(path)
		}
		if err != nil {
			return rt.fail(err, "")
		}
		if len(raw) > 8<<20 {
			return usagef("plan is too large")
		}
		var plan compatApplyPlan
		if err := yaml.Unmarshal(raw, &plan); err != nil {
			return usagef("parse plan: %v", err)
		}
		if len(plan.Create) > 0 && strings.TrimSpace(plan.Project) == "" {
			return usagef("plan.create requires a top-level project")
		}
		for _, c := range plan.Create {
			if strings.TrimSpace(c.Title) == "" {
				return usagef("create title is required")
			}
			if c.Type != "" && !issueKinds[c.Type] {
				return usagef("unknown create type %q", c.Type)
			}
		}
		if dryRun {
			return rt.printJSON(map[string]any{"dry_run": true, "plan": plan})
		}
		var project apiNode
		if len(plan.Create) > 0 {
			project, err = rt.projectNode(plan.Project)
			if err != nil {
				return err
			}
		}
		created := map[string]apiNode{}
		for _, c := range plan.Create {
			kindName := c.Type
			if kindName == "" {
				kindName = "ticket"
			}
			kind, err := rt.kind(kindName)
			if err != nil {
				return err
			}
			parent := project.ID
			if c.Parent != "" {
				if local, ok := created[c.Parent]; ok {
					parent = local.ID
				} else {
					n, err := rt.nodeRef(c.Parent)
					if err != nil {
						return err
					}
					parent = n.ID
				}
			}
			fields := c.Fields
			if fields == nil {
				fields = map[string]any{}
			}
			for k, v := range map[string]string{"priority": c.Priority, "acceptance_criteria": c.AcceptanceCriteria, "notes": c.Notes, "cost_unit": c.CostUnit, "release": c.Release} {
				if v != "" {
					fields[k] = v
				}
			}
			body := map[string]any{"kind_id": kind.ID, "title": c.Title, "body": c.Description, "parent_id": parent, "fields": fields}
			if c.Status != "" {
				body["state"] = c.Status
			}
			var n apiNode
			if err := rt.do(http.MethodPost, "/api/nodes", body, &n); err != nil {
				return err
			}
			if c.Name != "" {
				created[c.Name] = n
			}
		}
		for _, u := range plan.Update {
			n, err := rt.nodeRef(u.Ref)
			if err != nil {
				return err
			}
			body := map[string]any{}
			fields := fieldMap(n.Fields)
			for k, v := range u.Fields {
				switch k {
				case "title", "description", "status":
					name := k
					if k == "description" {
						name = "body"
					}
					if k == "status" {
						name = "state"
					}
					body[name] = v
				default:
					fields[k] = v
				}
			}
			body["fields"] = fields
			if err := rt.patchNode(n, body, nil); err != nil {
				fmt.Fprintf(rt.stderr, "plan stopped at %s; earlier successful changes remain applied\n", u.Ref)
				return err
			}
		}
		for _, rel := range plan.Relations {
			typ, reverse := aeonRelationType(rel.Type)
			switch typ {
			case "blocks", "relates", "implements", "cites", "duplicates", "customer_of", "contact_for":
			default:
				return usagef("unknown relation type %q", rel.Type)
			}
			src, ok := created[rel.Source]
			if !ok {
				src, err = rt.nodeRef(rel.Source)
				if err != nil {
					return err
				}
			}
			tgt, ok := created[rel.Target]
			if !ok {
				tgt, err = rt.nodeRef(rel.Target)
				if err != nil {
					return err
				}
			}
			if reverse {
				src, tgt = tgt, src
			}
			if err := rt.do(http.MethodPost, "/api/relations", map[string]any{"source_node_id": src.ID, "target_node_id": tgt.ID, "type": typ}, nil); err != nil {
				return err
			}
		}
		if rt.jsonOut {
			return rt.printJSON(map[string]any{"ok": true, "created": len(plan.Create), "updated": len(plan.Update), "relations": len(plan.Relations)})
		}
		_, err = fmt.Fprintf(rt.stdout, "✓ created %d issues\n✓ updated %d issues\n✓ added %d relations\n", len(plan.Create), len(plan.Update), len(plan.Relations))
		return err
	}}
}
