// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var estimatePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?|\.[0-9]+)(h|m)?$`)

func parseEstimate(value string) (float64, error) {
	match := estimatePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, usagef("estimate must be hours (2h or 1.5) or minutes (90m), greater than 0 and at most 200 hours")
	}
	hours, err := strconv.ParseFloat(match[1], 64)
	if match[2] == "m" {
		hours /= 60
	}
	if err != nil || math.IsNaN(hours) || math.IsInf(hours, 0) || hours <= 0 || hours > 200 {
		return 0, usagef("estimate must be greater than 0 and at most 200 hours")
	}
	return hours, nil
}
func estimateFields(fields map[string]any, hours float64, source string) {
	for _, key := range []string{"estimate_source", "estimate_by", "estimate_at", "estimate_confirmed"} {
		delete(fields, key)
	}
	fields["estimate_hours"] = hours
	if source != "" {
		fields["estimate_source"] = source
	}
}
func withEstimateHints(warnings []string) []string {
	out := make([]string, 0, len(warnings)+1)
	hinted := false
	for _, warning := range warnings {
		out = append(out, warning)
		if hinted || !strings.Contains(warning, "fields.estimate_hours") || strings.Contains(warning, "--estimate") {
			continue
		}
		out = append(out, "pass --estimate-hours (for example 2 or 0.5), or --estimate 2h")
		hinted = true
	}
	return out
}
func validEstimate(value any) bool {
	h, ok := value.(float64)
	return ok && h > 0 && h <= 200 && !math.IsInf(h, 0) && !math.IsNaN(h)
}

func (rt *runtime) cmdIssueEstimate() *Command {
	var hours, source, project, file string
	var missing, dryRun, apply bool
	return &Command{Name: "estimate", Short: "Set agent work hours or apply a missing-estimate plan", Use: "issue estimate <KEY> --hours N [--source agent|person] | --missing --project KEY --from-file plan.json --dry-run|--apply", maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&hours, "hours", 0, "agent hours: 2h, 90m or 1.5")
			fs.string(&source, "source", 0, "source assertion: agent or person (must match the caller)")
			fs.bool(&missing, "missing", 0, "only fill missing ticket and task estimates in this project")
			fs.string(&project, "project", 'p', "project key for a bulk plan")
			fs.string(&file, "from-file", 0, `JSON array of {"key":"AEON-1","hours":2}; - for stdin`)
			fs.bool(&dryRun, "dry-run", 0, "validate and preview without writing")
			fs.bool(&apply, "apply", 0, "apply the bulk plan with revision preconditions")
		}, run: func(args []string) error {
			if source != "" && source != "agent" && source != "person" {
				return usagef("--source must be agent or person")
			}
			if missing {
				if len(args) != 0 || hours != "" || project == "" || file == "" || dryRun == apply || source == "person" {
					return usagef("use --missing --project KEY --from-file plan.json with exactly one of --dry-run or --apply; bulk estimates have source agent")
				}
				return rt.estimateMissing(project, file, apply)
			}
			if len(args) != 1 || hours == "" || project != "" || file != "" || apply {
				return usagef("use issue estimate KEY --hours N [--source agent|person] [--dry-run]")
			}
			if _, err := normalizeIssueRef(args[0]); err != nil {
				return err
			}
			value, err := parseEstimate(hours)
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(rt.stdout, "dry-run: would estimate %s at %gh\n", args[0], value)
				return nil
			}
			return rt.updateIssue(issuePatch{Ref: args[0], Estimate: hours, EstimateSource: source})
		}}
}

type estimatePlanEntry struct {
	Key   string  `json:"key"`
	Hours float64 `json:"hours"`
}
type estimateAction struct {
	Key    string  `json:"key"`
	Hours  float64 `json:"hours"`
	Status string  `json:"status"`
	node   apiNode
}

func (rt *runtime) estimateMissing(project, file string, apply bool) error {
	raw, err := rt.readText("", file, "from")
	if err != nil {
		return err
	}
	var plan []estimatePlanEntry
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return usagef("invalid estimate plan: %v", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return usagef("estimate plan must contain one JSON array")
	}
	if len(plan) == 0 || len(plan) > 10000 {
		return usagef("estimate plan must have 1–10000 entries")
	}
	seen := map[string]bool{}
	for i := range plan {
		plan[i].Key = strings.TrimSpace(plan[i].Key)
		if _, err := normalizeIssueRef(plan[i].Key); err != nil {
			return err
		}
		if seen[plan[i].Key] {
			return usagef("duplicate estimate for %s", plan[i].Key)
		}
		seen[plan[i].Key] = true
		if !validEstimate(plan[i].Hours) {
			return usagef("invalid estimate for %s: hours must be greater than 0 and at most 200", plan[i].Key)
		}
	}
	proj, err := rt.projectNode(project)
	if err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	nodes, err := rt.walkNodes(url.Values{"within": {proj.ID}, "kind": {"ticket,task"}}, nil)
	if err != nil {
		return err
	}
	byKey := map[string]apiNode{}
	for _, n := range nodes {
		if kind := kinds.slug(n.KindID); kind == "ticket" || kind == "task" {
			byKey[n.Key] = n
		}
	}
	actions := make([]estimateAction, 0, len(plan))
	// Validate every target before the first write; never use a key-prefix guess
	// for project membership, and never silently patch a stale revision.
	for _, entry := range plan {
		n, ok := byKey[entry.Key]
		if !ok {
			return usagef("%s is not a visible ticket or task in project %s", entry.Key, project)
		}
		action := estimateAction{Key: entry.Key, Hours: entry.Hours, Status: "planned", node: n}
		if validEstimate(fieldMap(n.Fields)["estimate_hours"]) {
			action.Status = "skipped: already estimated"
		} else if n.UpdatedAt.IsZero() {
			return usagef("%s has no revision timestamp", entry.Key)
		}
		actions = append(actions, action)
	}
	for i := range actions {
		action := &actions[i]
		if !apply || action.Status != "planned" {
			continue
		}
		fields := fieldMap(action.node.Fields)
		estimateFields(fields, action.Hours, "agent")
		var updated apiNode
		if err := rt.doHeaders(http.MethodPatch, "/api/nodes/"+url.PathEscape(action.node.ID), map[string]any{"fields": fields}, &updated, map[string]string{"If-Unmodified-Since": action.node.UpdatedAt.Format(time.RFC3339Nano)}); err != nil {
			fmt.Fprintf(rt.stderr, "estimate plan stopped at %s; earlier applied entries remain applied\n", action.Key)
			return err
		}
		action.Status = "applied"
		if !rt.jsonOut {
			fmt.Fprintf(rt.stdout, "%s: %gh (applied)\n", action.Key, action.Hours)
		}
	}
	if rt.jsonOut {
		return rt.printJSON(actions)
	}
	for _, action := range actions {
		if action.Status != "applied" {
			fmt.Fprintf(rt.stdout, "%s: %gh (%s)\n", action.Key, action.Hours, action.Status)
		}
	}
	return nil
}
