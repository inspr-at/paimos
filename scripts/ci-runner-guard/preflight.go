// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"fmt"
	"reflect"
)

// Narrow draft policy for OPS review. This does not admit the Mac pool: the
// dispatch, main ref, hosted class and current router attempt must all hold.
const preflightRunner = `${{ fromJSON(needs.preflight-route.outputs.runs_on) }}`
const preflightHosted = `github.ref == 'refs/heads/main' && needs.preflight-route.outputs.runner_class == 'hosted' && needs.preflight-route.outputs.run_attempt == github.run_attempt`

func checkPreflight(workflow map[string]any) error {
	triggers := mapping(workflow["on"])
	dispatch := mapping(triggers["workflow_dispatch"])
	inputs := mapping(dispatch["inputs"])
	if len(triggers) != 1 || len(dispatch) != 1 || len(inputs) != 2 || workflow["run-name"] != `preflight:${{ inputs.sha }}` {
		return fmt.Errorf("preflight requires only explicit SHA-bound dispatch")
	}
	for _, key := range []string{"sha", "local-result"} {
		input := mapping(inputs[key])
		if input["type"] != "string" || input["required"] != true || len(input) != 3 || input["description"] == nil {
			return fmt.Errorf("preflight requires exactly the candidate SHA and local receipt")
		}
	}
	if !reflect.DeepEqual(mapping(workflow["permissions"]), map[string]any{"contents": "read", "actions": "read"}) ||
		containsSecret(workflow) || !reflect.DeepEqual(mapping(workflow["concurrency"]), workflowConcurrency["ci-preflight.yml"]) {
		return fmt.Errorf("preflight must retain read-only authority and separate per-head concurrency")
	}
	jobs := mapping(workflow["jobs"])
	route := mapping(jobs["preflight-route"])
	if len(jobs) != 4 || route["uses"] != "./.github/workflows/test-runner-route.yml" ||
		!reflect.DeepEqual(mapping(route["with"]), map[string]any{"required-idle-runners": 12}) || route["secrets"] != nil {
		return fmt.Errorf("preflight must retain its existing router and full browser layout")
	}
	for _, id := range []string{"preflight-setup", "browser", "result"} {
		job := mapping(jobs[id])
		condition := preflightHosted
		if id == "result" {
			condition = "always() && github.ref == 'refs/heads/main' && needs.preflight-route.result == 'success' && needs.preflight-route.outputs.runner_class == 'hosted' && needs.preflight-route.outputs.run_attempt == github.run_attempt"
		}
		if job["if"] != condition || job["runs-on"] != preflightRunner || !hasNeed(job["needs"], "preflight-route") ||
			job["permissions"] != nil || job["environment"] != nil || job["continue-on-error"] != nil {
			return fmt.Errorf("preflight %s must retain main/hosted/current-attempt refusal and read-only authority", id)
		}
	}
	browser := mapping(jobs["browser"])
	groups := make([]any, 12)
	for i := range groups {
		groups[i] = fmt.Sprintf("browser-%d", i+1)
	}
	if browser["name"] != `preflight-${{ matrix.group }}` || !hasNeed(browser["needs"], "preflight-setup") ||
		mapping(browser["strategy"])["fail-fast"] != false ||
		!reflect.DeepEqual(mapping(mapping(browser["strategy"])["matrix"]), map[string]any{"group": groups}) ||
		!hasNeed(mapping(jobs["result"])["needs"], "browser") {
		return fmt.Errorf("preflight must preserve all twelve stable browser groups and result dependency")
	}
	for _, value := range jobs {
		job := mapping(value)
		if job["secrets"] != nil || job["environment"] != nil || job["permissions"] != nil || job["continue-on-error"] != nil {
			return fmt.Errorf("preflight jobs cannot override authority or ignore failures")
		}
		steps, _ := job["steps"].([]any)
		for _, value := range steps {
			if mapping(value)["continue-on-error"] != nil {
				return fmt.Errorf("preflight steps cannot ignore failures")
			}
		}
	}
	return nil
}
