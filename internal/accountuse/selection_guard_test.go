// SPDX-License-Identifier: AGPL-3.0-only
package accountuse_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Risk: a new selection or daily-start call silently bypasses the project
// boundary. Counts inventory every existing site, including unchanged
// residency re-stamps and model-version propagation (not start authority).
func TestAccountUseSelectionCallSiteGuard(t *testing.T) {
	expected := map[string]int{
		"internal/agentaccounts/engine_admission.go":  1,
		"internal/agentaccounts/groups.go":            1,
		"internal/agentaccounts/model_allowance.go":   1,
		"internal/agentaccounts/readiness.go":         1,
		"internal/agentaccounts/route.go":             1,
		"internal/agentaccounts/routing_plan.go":      1,
		"internal/agentruns/queue.go":                 2,
		"internal/agentruns/queue_list.go":            1,
		"internal/agentruns/review.go":                1,
		"internal/agentruns/runs.go":                  3,
		"internal/engineadmission/module.go":          1,
		"internal/harness/session_requests.go":        1,
		"internal/modelregistry/board_write.go":       1,
		"internal/modelregistry/daily.go":             2,
		"internal/modelregistry/preferences_write.go": 1,
		"internal/modelregistry/refresh_successor.go": 2,
		"internal/workqueue/route.go":                 1,
	}
	root := filepath.Join("..", "..")
	actual := map[string]int{}
	pattern := regexp.MustCompile(`aeon_account_allows_profile|AccountMeetsResidency\(|agentplan\.DailyStart\(`)
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var code []string
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.HasPrefix(line, "func AccountMeetsResidency(") {
				continue
			}
			code = append(code, line)
		}
		n := len(pattern.FindAllString(strings.Join(code, "\n"), -1))
		if n > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			actual[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("selection call-site inventory changed; bind each new site to the matrix predicate before updating inventory: expected %v, got %v", expected, actual)
	}
	for file, required := range map[string][]string{
		"internal/agentaccounts/groups.go":           {"applyUse(", "RequireRun(", "RequireProject("},
		"internal/agentaccounts/residency.go":        {"applyUse("},
		"internal/agentaccounts/review.go":           {"applyUse("},
		"internal/agentaccounts/route.go":            {"aeon_account_use_allowed(id", "RequireRun("},
		"internal/agentruns/runs.go":                 {"RequireRun(", "RequireProject(", "accountuse.LockShared("},
		"internal/agentruns/queue.go":                {"RequireProject("},
		"internal/agentruns/review.go":               {"RequireProject("},
		"internal/agentruns/queue_list.go":           {"aeon_account_use_allowed_for_project("},
		"internal/agentaccounts/engine_admission.go": {"AllowedForProject("},
		"internal/modelregistry/daily.go":            {"ProjectDailySnapshot(", "allDenied[harness]"},
		"internal/engineadmission/module.go":         {"ProjectDailySnapshot(", "allDenied[in.Harness]"},
		"internal/harness/session_requests.go":       {"RequireProject("},
		"internal/workqueue/route.go":                {"aeon_account_use_allowed_for_project("},
	} {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, boundary := range required {
			if !strings.Contains(string(raw), boundary) {
				t.Errorf("%s lost boundary %s", file, boundary)
			}
		}
	}
}
