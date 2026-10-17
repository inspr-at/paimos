// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/crossreview"
)

func TestDeploymentAncestryRequiresRepositoryAndExactMergeBase(t *testing.T) {
	// Risk: dates, same-named branches, a truncated comparison or a missing
	// private repository are mistaken for deployed commit ancestry.
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for _, scenario := range []string{"ahead", "identical", "behind", "diverged", "wrong base", "wrong merge base", "wrong repository", "wrong commit", "unknown SHA", "missing repository", "partial read", "unknown status"} {
		t.Run(scenario, func(t *testing.T) {
			status, mergeBase := scenario, a
			base, repo, commit := a, "example/delivery", b
			var upstream error
			switch scenario {
			case "behind", "diverged":
				mergeBase = c
			case "wrong base":
				status, base = "ahead", c
			case "wrong merge base":
				status, mergeBase = "ahead", c
			case "wrong repository":
				repo = "example/other"
			case "wrong commit":
				commit = c
			case "partial read":
				upstream = errRead
			}
			calls := 0
			get := func(path string, out any) error {
				calls++
				var value any
				switch path {
				case "":
					if scenario == "missing repository" {
						return crossreview.ErrNotFound
					}
					value = map[string]string{"full_name": repo}
				case "/git/commits/" + b:
					if scenario == "unknown SHA" {
						return crossreview.ErrNotFound
					}
					value = map[string]string{"sha": commit}
				case "/compare/" + a + "..." + b + "?per_page=1":
					if upstream != nil {
						return upstream
					}
					value = map[string]any{"status": status, "base_commit": map[string]string{"sha": base}, "merge_base_commit": map[string]string{"sha": mergeBase}}
				default:
					t.Fatalf("unexpected read: %s", path)
				}
				raw, _ := json.Marshal(value)
				return json.Unmarshal(raw, out)
			}
			out, err := deploymentAncestors(get, "example/delivery", b, []string{a, b})
			switch scenario {
			case "ahead", "identical", "behind", "diverged":
				if err != nil || out[a] != (scenario == "ahead" || scenario == "identical") || !out[b] || calls != 3 {
					t.Fatalf("ancestry=%v calls=%d err=%v", out, calls, err)
				}
			case "unknown SHA":
				var failure *apiError
				if !errors.As(err, &failure) || failure.status != 422 {
					t.Fatalf("unknown SHA must be 422: %v", err)
				}
			default:
				if err == nil || out != nil {
					t.Fatalf("unverified ancestry accepted: %v", out)
				}
				var failure *apiError
				if errors.As(err, &failure) && failure.status == 422 {
					t.Fatal("read failure misreported as an unknown SHA")
				}
			}
		})
	}
}
