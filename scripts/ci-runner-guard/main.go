// SPDX-License-Identifier: AGPL-3.0-only
// ci-runner-guard checks the reviewed workflows' runner selections. The NIX-600
// runner-side boundary enforces admission even when a PR edits this guard.
// Unknown dynamic selections fail closed.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const routedRunner = `${{ fromJSON(contains(fromJSON('["push","workflow_dispatch"]'), github.event_name) && github.ref == 'refs/heads/main' && needs.runner-route.outputs.run_attempt == github.run_attempt && needs.runner-route.outputs.runs_on || '["ubuntu-latest"]') }}`

var matrixRunner = regexp.MustCompile(`^\$\{\{\s*matrix\.([a-zA-Z_][a-zA-Z0-9_-]*)\s*\}\}$`)

// Exact labels used by this repository; changes require an explicit review.
var hostedRunners = map[string]bool{
	"ubuntu-latest": true, "ubuntu-24.04": true, "ubuntu-24.04-arm": true,
	"macos-15": true, "macos-15-intel": true,
}
var secretContext = regexp.MustCompile(`(?i)\bsecrets\b`)

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
	fmt.Println("runner guard: reviewed workflows keep PRs and release/image/attestation/pin jobs GitHub-hosted")
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
	var problems []string
	if hasEvent(workflow["on"], "pull_request_target") {
		problems = append(problems, name+": pull_request_target is forbidden")
	}
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
		if ok && compact(selection) == compact(routedRunner) {
			if protectedJob(name, id, job) {
				reject("release/pairing/image/attestation/pin evidence must use hosted runners")
			}
			if containsAMD64Artifact(job) || containsAMD64Artifact(workflow["env"]) || containsAMD64Artifact(workflow["defaults"]) {
				reject("routed jobs run on Linux ARM64 and must not reference amd64/x86_64/x64 artifacts")
			}
			if !hasNeed(job["needs"], "runner-route") || mapping(jobs["runner-route"])["uses"] != "./.github/workflows/test-runner-route.yml" {
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
	return problems, nil
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
		value = strings.ToLower(value)
		return strings.Contains(value, "amd64") || strings.Contains(value, "x86_64") || strings.Contains(value, "x64")
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
