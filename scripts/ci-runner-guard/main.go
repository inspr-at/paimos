// SPDX-License-Identifier: AGPL-3.0-only
// ci-runner-guard enforces repository-wide CI and runner workflow policy. The NIX-600
// runner-side boundary enforces admission even when a PR edits this guard.
// Unknown dynamic selections fail closed.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const routedRunner = `${{ fromJSON(contains(fromJSON('["push","workflow_dispatch"]'), github.event_name) && github.ref == 'refs/heads/main' && needs.runner-route.outputs.run_attempt == github.run_attempt && needs.runner-route.outputs.runs_on || '["ubuntu-latest"]') }}`
const routedGoShards = `${{ fromJSON(contains(fromJSON('["push","workflow_dispatch"]'), github.event_name) && github.ref == 'refs/heads/main' && needs.runner-route.outputs.run_attempt == github.run_attempt && needs.runner-route.outputs.runner_class == 'mbp2606' && '[1, 2, 3, 4]' || '[1, 2, 3, 4, 5, 6, 7]') }}`

const ciConcurrencyGroup = `ci-${{ github.event_name }}-${{ github.event.pull_request.number || github.run_id }}`

// These exact mappings are the sole authority for workflow concurrency. Jobs
// never own concurrency, and reusable workflows cannot enter a caller's group.
var workflowConcurrency = map[string]map[string]any{
	"ci.yml": {
		"group": ciConcurrencyGroup, "cancel-in-progress": `${{ github.event_name == 'pull_request' }}`,
	},
	"release.yml": {
		"group": `release-${{ github.ref }}`, "cancel-in-progress": false,
	},
	"homebrew-tap.yml": {
		"group": `homebrew-tap-${{ github.event.release.tag_name }}`, "cancel-in-progress": false,
	},
	"release-image-check.yml": {
		"group": `release-image-check-${{ github.ref }}`, "cancel-in-progress": true,
	},
}

var reservedCIJobs = map[string]bool{
	"go": true, "web": true, "release-check": true, "e2e": true,
	"cross-family": true, "gate/cross-family": true,
	"go-test": true, "go-static": true, "go-timing": true, "runner-route": true,
}

var matrixRunner = regexp.MustCompile(`^\$\{\{\s*matrix\.([a-zA-Z_][a-zA-Z0-9_-]*)\s*\}\}$`)

// Exact labels used by this repository; changes require an explicit review.
var hostedRunners = map[string]bool{
	"ubuntu-latest": true, "ubuntu-24.04": true, "ubuntu-24.04-arm": true,
	"macos-15": true, "macos-15-intel": true,
}
var secretContext = regexp.MustCompile(`(?i)\bsecrets\b`)

// Artifact separators include underscores; x64 must be a token so harmless
// names such as linux64 are not mistaken for an x86 architecture.
var x86Artifact = regexp.MustCompile(`(?i)amd64|x86[_-]64|i[3-6]86|(?:^|[^a-z0-9])x64(?:$|[^a-z0-9])`)

func main() {
	dir := ".github/workflows"
	if len(os.Args) == 2 {
		dir = os.Args[1]
	} else if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: ci-runner-guard [workflow-directory]")
		os.Exit(2)
	}
	problems, err := checkDirectory(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) != 0 {
		os.Exit(1)
	}
	fmt.Println("workflow guard: CI identities, triggers and concurrency are isolated; reviewed runner boundaries hold")
}

func checkDirectory(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var problems []string
	count := 0
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if entry.IsDir() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		count++
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		found, err := checkWorkflow(entry.Name(), body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		problems = append(problems, found...)
	}
	if count == 0 {
		return nil, fmt.Errorf("no workflows in %s", dir)
	}
	sort.Strings(problems)
	return problems, nil
}

func checkWorkflow(name string, body []byte) ([]string, error) {
	var workflow map[string]any
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		return nil, err
	}
	jobs := mapping(workflow["jobs"])
	if len(jobs) == 0 {
		return nil, fmt.Errorf("workflow has no jobs")
	}
	problems := checkWorkflowPolicy(name, workflow)
	return append(problems, checkRunnerJobs(name, workflow)...), nil
}

// Apply before runner selection, including uses: jobs; runner admission must
// never short-circuit repository-wide identity, trigger or concurrency rules.
func checkWorkflowPolicy(name string, workflow map[string]any) []string {
	var problems []string
	reject := func(reason string) { problems = append(problems, name+": "+reason) }
	if hasEvent(workflow["on"], "pull_request_target") {
		reject("pull_request_target is forbidden")
	}
	if concurrency, exists := workflow["concurrency"]; exists {
		if hasEvent(workflow["on"], "workflow_call") {
			reject("reusable workflows must not define concurrency")
		}
		group, _ := mapping(concurrency)["group"].(string)
		if scalar, ok := concurrency.(string); ok {
			group = scalar
		}
		if name != "ci.yml" && strings.EqualFold(compact(group), compact(ciConcurrencyGroup)) {
			reject("other workflows must not copy ci.yml's concurrency group")
		}
		if expected, allowed := workflowConcurrency[name]; !allowed {
			reject("workflow-level concurrency is not allowlisted")
		} else if !reflect.DeepEqual(mapping(concurrency), expected) {
			reject("must retain the exact reviewed concurrency policy")
		}
	} else if name == "ci.yml" {
		reject("must retain the exact reviewed concurrency policy")
	}
	for id, value := range mapping(workflow["jobs"]) {
		job := mapping(value)
		jobName, _ := job["name"].(string)
		if strings.EqualFold(strings.TrimSpace(id), "gate/cross-family") ||
			strings.EqualFold(strings.TrimSpace(jobName), "gate/cross-family") {
			reject("gate/cross-family is reserved to the external App, never an Actions job")
		}
		if name != "cross-family-preview.yml" && (id == "policy-preview" || jobName == "gate/policy-preview") {
			reject("gate/policy-preview is reserved to cross-family-preview.yml")
		}
		if _, exists := job["concurrency"]; exists {
			reject(fmt.Sprintf("job %q must not override workflow-level concurrency", id))
		}
		if name != "ci.yml" {
			jobName, _ := job["name"].(string)
			if reservedCIJobs[strings.ToLower(strings.TrimSpace(id))] || reservedCIJobs[strings.ToLower(strings.TrimSpace(jobName))] {
				reject(fmt.Sprintf("job %q id and name are reserved to ci.yml", id))
			}
		}
	}
	if name == "ci.yml" {
		if err := checkCITriggersAndRequiredChecks(workflow); err != nil {
			reject(err.Error())
		}
	} else if name == "cross-family-preview.yml" {
		if err := checkGatePreview(workflow); err != nil {
			reject(err.Error())
		}
	} else if name == "full-ui-qa.yml" {
		if err := checkFullUIQA(workflow); err != nil {
			reject(err.Error())
		}
	} else if hasEvent(workflow["on"], "pull_request") {
		pr := mapping(mapping(workflow["on"])["pull_request"])
		_, paths := pr["paths"]
		_, ignored := pr["paths-ignore"]
		if paths == ignored || !(validPaths(pr["paths"]) || validPaths(pr["paths-ignore"])) {
			reject("pull_request outside ci.yml requires nonempty paths or paths-ignore")
		}
	}
	return problems
}

// Full QA is an explicit label opt-in across all changed paths. This exception
// does not admit another broad PR workflow or any self-hosted runner selection.
func checkFullUIQA(workflow map[string]any) error {
	expected := map[string]any{
		"schedule": []any{map[string]any{"cron": "37 2 * * *"}},
		"workflow_dispatch": map[string]any{"inputs": map[string]any{
			"ref": map[string]any{
				"description": "Branch, tag, or commit to test (blank means main)",
				"required":    false, "type": "string", "default": "main",
			},
		}},
		"pull_request": map[string]any{"types": []any{"opened", "reopened", "synchronize", "labeled"}},
	}
	if !reflect.DeepEqual(mapping(workflow["on"]), expected) {
		return fmt.Errorf("full UI QA must retain nightly, manual-ref and label PR triggers")
	}
	if !reflect.DeepEqual(mapping(workflow["permissions"]), map[string]any{"contents": "read"}) || containsSecret(workflow) {
		return fmt.Errorf("full UI QA requires a read-only token and no secrets")
	}
	jobs := mapping(workflow["jobs"])
	source := mapping(jobs["qa-source"])
	condition := "(github.event_name == 'schedule' && github.ref == 'refs/heads/main') || " +
		"github.event_name == 'workflow_dispatch' || " +
		"(github.event_name == 'pull_request' && contains(github.event.pull_request.labels.*.name, 'full-qa') && " +
		"(github.event.action != 'labeled' || github.event.label.name == 'full-qa'))"
	if actual, _ := source["if"].(string); compact(actual) != compact(condition) {
		return fmt.Errorf("full UI QA source must require main schedule, manual dispatch or full-qa PR label")
	}
	shard := mapping(jobs["qa-shard"])
	gate := mapping(jobs["qa-full"])
	if len(jobs) != 3 || !hasNeed(shard["needs"], "qa-source") ||
		gate["name"] != "full-ui-qa" || gate["if"] != "${{ always() && needs.qa-source.result != 'skipped' }}" ||
		!reflect.DeepEqual(gate["needs"], []any{"qa-source", "qa-shard"}) {
		return fmt.Errorf("full UI QA must bind source, shards and aggregate failure gate")
	}
	strategy := mapping(shard["strategy"])
	if strategy["fail-fast"] != false || !reflect.DeepEqual(mapping(strategy["matrix"]), map[string]any{"shard": []any{1, 2, 3, 4, 5}}) {
		return fmt.Errorf("full UI QA must run all five shards without fail-fast")
	}
	for _, value := range jobs {
		job := mapping(value)
		if job["runs-on"] != "ubuntu-latest" || job["permissions"] != nil || job["environment"] != nil || job["continue-on-error"] != nil {
			return fmt.Errorf("full UI QA jobs must stay hosted, read-only and fail closed without environments")
		}
	}
	return nil
}

func checkGatePreview(workflow map[string]any) error {
	expected := map[string]any{"pull_request": nil, "merge_group": map[string]any{"types": []any{"checks_requested"}}}
	if !reflect.DeepEqual(mapping(workflow["on"]), expected) {
		return fmt.Errorf("gate preview must run only on unfiltered PR and merge-group check requests")
	}
	jobs := mapping(workflow["jobs"])
	job := mapping(jobs["policy-preview"])
	if len(jobs) != 1 || job == nil || job["name"] != "gate/policy-preview" {
		return fmt.Errorf("gate preview must retain its diagnostic identity")
	}
	for _, key := range []string{"if", "needs", "continue-on-error"} {
		if _, exists := job[key]; exists {
			return fmt.Errorf("gate preview must not set %s", key)
		}
	}
	if !reflect.DeepEqual(mapping(workflow["permissions"]), map[string]any{"contents": "read"}) ||
		!reflect.DeepEqual(mapping(job["permissions"]), map[string]any{"contents": "read", "statuses": "read", "pull-requests": "read"}) {
		return fmt.Errorf("gate preview token must be read-only")
	}
	return nil
}

func validPaths(value any) bool {
	paths, ok := value.([]any)
	if !ok || len(paths) == 0 {
		return false
	}
	for _, value := range paths {
		path, ok := value.(string)
		if !ok || strings.TrimSpace(path) == "" {
			return false
		}
	}
	return true
}

func checkRunnerJobs(name string, workflow map[string]any) []string {
	jobs := mapping(workflow["jobs"])
	var problems []string
	routeID := "runner-route"
	if name == "test-runner-smoke.yml" {
		routeID = "smoke-route"
	}
	// The smoke caller has a distinct identity but identical event/ref/attempt
	// guards. No other caller can rename the reviewed router dependency.
	runnerExpression := strings.ReplaceAll(routedRunner, "needs.runner-route.", "needs."+routeID+".")
	for id, value := range jobs {
		job := mapping(value)
		reject := func(reason string) { problems = append(problems, name+"/"+id+": "+reason) }
		if uses, ok := job["uses"].(string); ok {
			// Only the hosted-only router has a reviewed reuse boundary. Other
			// reusable workflows need caller-sensitive analysis before admission:
			// a hosted-looking release caller could otherwise call routed tests.
			if uses != "./.github/workflows/test-runner-route.yml" {
				reject("opaque reusable workflow runner selection")
			}
			continue
		}
		selection, ok := job["runs-on"].(string)
		if ok && compact(selection) == compact(runnerExpression) {
			if shards, exists := mapping(mapping(job["strategy"])["matrix"])["shard"]; exists {
				selection, ok := shards.(string)
				if !ok || compact(selection) != compact(routedGoShards) {
					reject("routed shard count must use the same event/ref/attempt guards as runner selection")
				}
			}
			if protectedJob(name, id, job) {
				reject("release/pairing/image/attestation/pin evidence must use hosted runners")
			}
			if containsAMD64Artifact(job) || containsAMD64Artifact(workflow["env"]) || containsAMD64Artifact(workflow["defaults"]) {
				reject("routed jobs run on Linux ARM64 and must not reference amd64/x86_64/x86-64/i[3-6]86/x64 artifacts")
			}
			if !hasNeed(job["needs"], routeID) || mapping(jobs[routeID])["uses"] != "./.github/workflows/test-runner-route.yml" {
				reject("routed tests must depend on the hosted runner-route workflow")
			}
			if job["environment"] != nil || containsSecret(job) || containsSecret(workflow["env"]) {
				reject("routed tests must not receive environments or secrets")
			}
			permissions := job["permissions"]
			if permissions == nil {
				permissions = workflow["permissions"]
			}
			if !readOnlyPermissions(permissions) {
				reject("routed tests require explicit read-only token permissions")
			}
			continue
		}
		if ok {
			if axis := matrixRunner.FindStringSubmatch(selection); axis != nil {
				labels, valid := matrixLabels(job, axis[1])
				if !valid || !allHosted(labels) {
					reject("matrix runner values must all be literal hosted labels")
				}
				continue
			}
		}
		if !allHosted(job["runs-on"]) {
			reject("runner selection is not proven hosted or event-guarded; mbp2606 is forbidden on PRs")
		}
	}
	return problems
}

func checkCITriggersAndRequiredChecks(workflow map[string]any) error {
	events := mapping(workflow["on"])
	var names []string
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"merge_group", "pull_request", "push", "workflow_dispatch"}) {
		return fmt.Errorf("CI must cover main, PRs, the merge queue and manual runs exactly once: %v", names)
	}
	if !reflect.DeepEqual(mapping(events["push"]), map[string]any{"branches": []any{"main"}}) {
		return fmt.Errorf("branch and tag pushes outside main must not duplicate PR CI: %v", events["push"])
	}
	if events["pull_request"] != nil {
		return fmt.Errorf("required PR checks must run without path or activity filters: %v", events["pull_request"])
	}
	if !reflect.DeepEqual(mapping(events["merge_group"]), map[string]any{"types": []any{"checks_requested"}}) {
		return fmt.Errorf("merge queue check requests must run CI: %v", events["merge_group"])
	}

	// These are the active main ruleset's contexts. Renaming or conditionally
	// skipping them would strand a PR or merge queue waiting for its checks.
	jobs := mapping(workflow["jobs"])
	// A proof outage must remain an unset reuse output, never a failed need
	// that skips the full suite. This includes checkout failures/timeouts.
	proof := mapping(jobs["tree-reuse"])
	if proof == nil || proof["continue-on-error"] != true {
		return fmt.Errorf("tree-reuse must continue on error so proof failure cannot skip required CI jobs")
	}
	if timeout, ok := proof["timeout-minutes"].(int); !ok || timeout < 5 || timeout > 10 {
		return fmt.Errorf("tree-reuse needs a bounded 5–10 minute timeout for checkout plus the 10 second proof")
	}
	if proof["if"] != nil || mapping(proof["outputs"])["reuse"] != "${{ steps.proof.outputs.reuse }}" {
		return fmt.Errorf("tree-reuse must always run and expose only its proof step output")
	}
	for _, id := range []string{"go-test", "go-static", "go-timing", "runner-route"} {
		job := mapping(jobs[id])
		if job == nil {
			return fmt.Errorf("required CI job %q is missing", id)
		}
		if id != "runner-route" {
			if _, exists := job["if"]; exists {
				return fmt.Errorf("required CI job %q must run without an if condition", id)
			}
			if !hasNeed(job["needs"], "tree-reuse") {
				return fmt.Errorf("required CI job %q must use the non-failing tree-reuse dependency", id)
			}
		}
	}
	for _, context := range []string{"go", "web", "release-check", "e2e", "migration-compat"} {
		job := mapping(jobs[context])
		if job == nil {
			return fmt.Errorf("required check %q is missing", context)
		}
		if name, exists := job["name"]; exists && name != context {
			return fmt.Errorf("required check %q renamed to %v", context, name)
		}
		if context == "go" {
			if job["if"] != "always()" {
				return fmt.Errorf("go must report failures even when its dependencies fail: %v", job["if"])
			}
			if !reflect.DeepEqual(job["needs"], []any{"go-test", "go-static", "go-timing"}) {
				return fmt.Errorf("go must gate every shard, static checks and timing: %v", job["needs"])
			}
		} else if _, exists := job["if"]; exists {
			return fmt.Errorf("required check %q must run for every CI event: %v", context, job["if"])
		} else if !hasNeed(job["needs"], "tree-reuse") {
			return fmt.Errorf("required check %q must use the non-failing tree-reuse dependency", context)
		}
	}
	return nil
}

func mapping(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func compact(s string) string { return strings.Join(strings.Fields(s), "") }

func hasEvent(events any, event string) bool {
	if m := mapping(events); m != nil {
		_, ok := m[event]
		return ok
	}
	return hasNeed(events, event)
}

func hasNeed(needs any, name string) bool {
	if s, ok := needs.(string); ok {
		return s == name
	}
	if values, ok := needs.([]any); ok {
		for _, v := range values {
			if v == name {
				return true
			}
		}
	}
	return false
}

func allHosted(v any) bool {
	if s, ok := v.(string); ok {
		return hostedRunners[s]
	}
	if values, ok := v.([]any); ok && len(values) > 0 {
		for _, value := range values {
			if !allHosted(value) {
				return false
			}
		}
		return true
	}
	return false
}

func matrixLabels(job map[string]any, axis string) ([]any, bool) {
	matrix := mapping(mapping(job["strategy"])["matrix"])
	if matrix == nil {
		return nil, false
	}
	var labels []any
	if values, exists := matrix[axis]; exists {
		list, ok := values.([]any)
		if !ok {
			return nil, false
		}
		labels = append(labels, list...)
	}
	if includes, exists := matrix["include"]; exists {
		list, ok := includes.([]any)
		if !ok {
			return nil, false
		}
		for _, row := range list {
			if label, exists := mapping(row)[axis]; exists {
				labels = append(labels, label)
			}
		}
	}
	return labels, len(labels) > 0
}

func protectedJob(file, id string, job map[string]any) bool {
	file = strings.ToLower(file)
	switch strings.TrimSuffix(file, filepath.Ext(file)) {
	case "release", "pairing-platform", "test-runner-route":
		return true
	}
	name, _ := job["name"].(string)
	identity := strings.ToLower(id + " " + name)
	for _, word := range []string{"release", "image", "attest", "pin-gate", "pin_gate", "homebrew"} {
		if strings.Contains(identity, word) {
			return true
		}
	}
	// A renamed job must not sneak image or attestation operations onto tests.
	steps, _ := job["steps"].([]any)
	for _, value := range steps {
		step := mapping(value)
		uses, _ := step["uses"].(string)
		run, _ := step["run"].(string)
		if strings.HasPrefix(uses, "docker/") || strings.Contains(uses, "attest") {
			return true
		}
		for _, word := range []string{"smoke-image.sh", "gh attestation", "docker build", "docker push", "docker buildx", "cosign ", "pin-gate", "pin_gate"} {
			if strings.Contains(run, word) {
				return true
			}
		}
	}
	return false
}

// Inspect parsed values (including inherited settings and matrix entries), not
// YAML comments. Hosted jobs may still download or cross-build amd64 artifacts.
func containsAMD64Artifact(v any) bool {
	switch value := v.(type) {
	case string:
		return x86Artifact.MatchString(value)
	case map[string]any:
		for key, child := range value {
			if containsAMD64Artifact(key) || containsAMD64Artifact(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsAMD64Artifact(child) {
				return true
			}
		}
	}
	return false
}

func containsSecret(v any) bool {
	switch value := v.(type) {
	case string:
		return (strings.Contains(value, "${{") && secretContext.MatchString(value)) || value == "inherit"
	case map[string]any:
		for key, child := range value {
			if key == "secrets" || containsSecret(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsSecret(child) {
				return true
			}
		}
	}
	return false
}

func readOnlyPermissions(v any) bool {
	if s, ok := v.(string); ok {
		return s == "read-all"
	}
	m := mapping(v)
	if m == nil {
		return false
	}
	for _, level := range m {
		if level != "read" && level != "none" {
			return false
		}
	}
	return true
}
