// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func composeHealthcheck(t *testing.T, service string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root(t), "deploy/compose/compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Healthcheck struct{ Test []string }
		}
	}
	if err := yaml.Unmarshal(b, &document); err != nil {
		t.Fatal(err)
	}
	probe := document.Services[service].Healthcheck.Test
	if len(probe) < 2 {
		t.Fatalf("%s healthcheck is missing", service)
	}
	return probe
}

func TestComposeAppProbesReadiness(t *testing.T) {
	probe := composeHealthcheck(t, "aeon")
	want := []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/api/ready"}
	if !reflect.DeepEqual(probe, want) {
		t.Fatalf("healthcheck must reject database-down HTTP 503, got %q", probe)
	}
}

func TestComposePostgresHealthRequiresDatabaseAndVector(t *testing.T) {
	probe := composeHealthcheck(t, "postgres")
	if len(probe) != 2 || probe[0] != "CMD-SHELL" {
		t.Fatalf("expected a shell probe, got %q", probe)
	}
	// Compose unescapes $$ before executing CMD-SHELL in the container.
	script := strings.ReplaceAll(probe[1], "$$", "$")
	bin := t.TempDir()
	// These command fixtures validate the real probe's arguments and simulate
	// PostgreSQL's distinct acceptance/query results, including a partial error.
	fixtures := map[string]string{
		"pg_isready": `#!/bin/sh
[ "$*" = '-h 127.0.0.1 -U postgres -d aeon -t 2' ] || exit 90
exit "$ACCEPT_STATUS"
`,
		"psql": `#!/bin/sh
[ "$*" = "-X -U postgres -d aeon -At -v ON_ERROR_STOP=1 -c SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')" ] || exit 91
printf '%s\n' "$VECTOR_RESULT"
exit "$QUERY_STATUS"
`,
	}
	for name, body := range fixtures {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, acceptStatus, queryStatus, vectorResult string
		wantHealthy                                   bool
	}{
		{"TCP not accepting during init", "1", "0", "t", false},
		{"database absent despite TCP acceptance", "0", "2", "", false},
		{"vector extension absent", "0", "0", "f", false},
		{"query failure after output", "0", "1", "t", false},
		{"initialized database", "0", "0", "t", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", script)
			cmd.Env = []string{"PATH=" + bin, "ACCEPT_STATUS=" + tc.acceptStatus,
				"QUERY_STATUS=" + tc.queryStatus, "VECTOR_RESULT=" + tc.vectorResult}
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			if len(output) != 0 {
				t.Fatalf("probe must not print query results: %s", output)
			}
			if (err == nil) != tc.wantHealthy {
				t.Fatalf("healthy=%v, want %v: %v", err == nil, tc.wantHealthy, err)
			}
		})
	}
}
