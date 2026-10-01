// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSelfHostingCheckIsScopedAndReadOnly(t *testing.T) {
	w := readWorkflow(t, "self-hosting-check.yml")
	if !reflect.DeepEqual(w.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("installation checks must not acquire write permissions")
	}
	if len(w.On) != 3 || w.On["pull_request"] == nil || w.On["schedule"] == nil {
		t.Fatal("expected path-filtered PRs, weekly schedule and manual runs only")
	}
	if _, ok := w.On["workflow_dispatch"]; !ok {
		t.Fatal("manual check is missing")
	}
	pr := w.On["pull_request"].(map[string]any)
	paths := pr["paths"].([]any)
	if len(paths) == 0 {
		t.Fatal("compose check must not run for every PR")
	}
	for _, path := range paths {
		p := path.(string)
		if p != "deploy/compose/**" && p != "docs/SELF-HOSTING.md" && p != "README.md" &&
			p != "Dockerfile" && p != "scripts/smoke-compose.sh" && p != ".github/workflows/self-hosting-check.yml" {
			t.Fatalf("unrelated path would trigger compose check: %s", p)
		}
	}
	if len(w.On["schedule"].([]any)) != 1 {
		t.Fatal("expected one weekly run")
	}
	for _, j := range w.Jobs {
		if len(j.Permissions) != 0 || j.Environment != "" {
			t.Fatal("compose check must not use privileged tokens or environments")
		}
		for _, s := range j.Steps {
			if s.With["push"] == "true" || s.With["cache-to"] != "" || strings.Contains(s.Uses, "login-action") ||
				strings.Contains(s.Uses, "attest") || strings.Contains(s.Run, "gh release") || strings.Contains(s.Run, "secrets.") {
				t.Fatalf("compose check gains publication or credential access: %s", s.Name)
			}
		}
	}
}

func TestComposeDocumentsParse(t *testing.T) {
	for _, name := range []string{"compose.yaml", "ci/compose.yaml"} {
		b, err := os.ReadFile(filepath.Join(root(t), "deploy/compose", name))
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := yaml.Unmarshal(b, &document); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if document["services"] == nil {
			t.Fatalf("%s: services missing", name)
		}
	}
}
