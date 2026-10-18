// SPDX-License-Identifier: AGPL-3.0-only

package releaseworkflow

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// This guard runs in the existing Go CI shards and required "go" check.
// Parse YAML instead of grepping lines so quoted, flow, and multiline uses
// cannot evade the check and shell-script text is not mistaken for an action.
func TestWorkflowActionPins(t *testing.T) {
	dir := filepath.Join(root(t), ".github")
	files := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml" {
			return nil
		}
		files++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, problem := range actionPinProblems(data) {
			t.Errorf("%s:%s", path, problem)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no workflow YAML found")
	}
}

func actionPinProblems(data []byte) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []string{err.Error()}
	}
	commit := regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+(?:/[a-zA-Z0-9_./-]+)?@[a-fA-F0-9]{40}$`)
	digest := regexp.MustCompile(`^docker://[^\s@]+@sha256:[a-fA-F0-9]{64}$`)
	var problems []string
	seen := make(map[*yaml.Node]bool)
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node == nil || seen[node] {
			return
		}
		seen[node] = true
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Value != "uses" {
					continue
				}
				if value.Kind == yaml.AliasNode {
					value = value.Alias
				}
				ref := strings.TrimSpace(value.Value)
				local := strings.HasPrefix(ref, "./") && filepath.IsLocal(strings.TrimPrefix(ref, "./"))
				if value.Kind != yaml.ScalarNode || !(local || commit.MatchString(ref) || digest.MatchString(ref)) {
					problems = append(problems, fmt.Sprintf("%d: uses %q must pin a full commit SHA (or container digest)", key.Line, ref))
				}
			}
		}
		for _, child := range node.Content {
			walk(child)
		}
		walk(node.Alias)
	}
	walk(&doc)
	return problems
}

func TestActionPinGuardRejectsMutableReferences(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tt := range []struct {
		name, source string
		valid        bool
	}{
		{"commit and version comment", "uses: actions/checkout@" + sha + " # v7.0.1", true},
		{"local workflow", "uses: ./.github/workflows/test-runner-route.yml", true},
		{"local action", "uses: ./actions/local", true},
		{"pinned remote workflow", "uses: owner/repo/.github/workflows/build.yml@" + sha, true},
		{"pinned container", "uses: docker://alpine@sha256:" + strings.Repeat("b", 64), true},
		{"quoted commit", "'uses': 'actions/checkout@" + sha + "'", true},
		{"shell text", "run: |\n  echo 'uses: actions/checkout@v7'", true},
		{"comment", "# uses: actions/checkout@v7\nrun: true", true},
		{"tag", "uses: actions/checkout@v7", false},
		{"branch", "uses: actions/checkout@main", false},
		{"short SHA", "uses: actions/checkout@abcdef0", false},
		{"missing ref", "uses: actions/checkout", false},
		{"long SHA", "uses: actions/checkout@" + sha + "a", false},
		{"nonhex SHA", "uses: actions/checkout@" + strings.Repeat("z", 40), false},
		{"flow mapping", "steps: [{uses: 'actions/checkout@v7'}]", false},
		{"quoted tag", "'uses': 'actions/checkout@v7'", false},
		{"multiline tag", "uses: >-\n  actions/checkout@v7", false},
		{"alias", "ref: &ref actions/checkout@v7\nuses: *ref", false},
		{"merge alias", "ref: &ref {uses: actions/checkout@v7}\nsteps: [{<<: *ref}]", false},
		{"mutable container", "uses: docker://alpine:latest", false},
		{"unpinned remote workflow", "uses: owner/repo/.github/workflows/build.yml@main", false},
		{"escaping local path", "uses: ./../action", false},
		{"null", "uses:", false},
		{"mapping", "uses: {ref: actions/checkout@v7}", false},
		{"invalid YAML", "steps: [", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems := actionPinProblems([]byte(tt.source))
			if got := len(problems) == 0; got != tt.valid {
				t.Fatalf("valid=%t, want %t: %v", got, tt.valid, problems)
			}
		})
	}
}
