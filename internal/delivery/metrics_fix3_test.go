// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDeliveryMetricsFix3ResumeIncludesNewerAttempt(t *testing.T) {
	// Risk: resuming a run suppresses the listed latest attempt. If another
	// attempt completes between steps, the backfill finishes untruncated and
	// never stores that verdict.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	day := dayStart(now)
	head := strings.Repeat("d", 40)
	list := "/actions/workflows/ci.yml/runs?created=" + day.Format("2006-01-02") + "&status=completed&per_page=100&page=1"
	listed := 0
	get := func(path string, out any) error {
		switch {
		case path == list:
			listed++
			attempt, conclusion := 45, "failure"
			if listed > 1 {
				attempt, conclusion = 46, "success"
			}
			return writeMetricJSON(out, map[string]any{"total_count": 1, "workflow_runs": []any{
				githubRunBody(800, attempt, defaultCIWorkflow, "pull_request", "work/a", head, day.Add(time.Hour), 8*time.Minute, conclusion, 0),
			}})
		case strings.HasPrefix(path, "/actions/runs/800/attempts/"):
			n, err := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
			if err != nil || n < 1 || n > 46 {
				return fmt.Errorf("unexpected attempt %s", path)
			}
			conclusion := "failure"
			if n == 46 {
				conclusion = "success"
			}
			return writeMetricJSON(out, githubRunBody(800, n, defaultCIWorkflow, "pull_request", "work/a", head, day.Add(time.Duration(n)*time.Minute), 8*time.Minute, conclusion, 0))
		case path == "":
			return writeMetricJSON(out, map[string]any{"full_name": "example/delivery", "default_branch": "main"})
		case strings.HasPrefix(path, "/pulls"):
			return writeMetricJSON(out, []any{})
		case strings.HasPrefix(path, "/actions/workflows/"):
			return writeMetricJSON(out, map[string]any{"total_count": 0, "workflow_runs": []any{}})
		default:
			return fmt.Errorf("unexpected GitHub read %s", path)
		}
	}
	cursor := backfillCursor{Since: day, Phase: "runs", Day: day.Format("2006-01-02"), Page: 1}
	seen := map[int]string{}
	for steps := 0; cursor.Phase == "runs"; steps++ {
		if steps > 3 {
			t.Fatal("attempt backfill did not finish")
		}
		next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Calls > backfillCallBudget {
			t.Fatalf("step used %d calls", batch.Calls)
		}
		for _, run := range batch.Runs {
			if run.ID == 800 {
				seen[run.Attempt] = run.Conclusion
			}
		}
		if steps == 0 && (next.AttemptRun != 800 || next.Attempt != 40 || seen[45] != "failure" || seen[46] != "" || seen[40] != "") {
			t.Fatalf("first step did not pause before the newer attempt: cursor %+v seen[45]=%q seen[46]=%q", next, seen[45], seen[46])
		}
		cursor = next
	}
	if cursor.Phase != "done" || cursor.Truncated {
		t.Fatalf("backfill finished as phase %s truncated %v", cursor.Phase, cursor.Truncated)
	}
	if seen[45] != "failure" || seen[40] != "failure" || seen[46] != "success" {
		t.Fatalf("resumed attempts missed the newer verdict: 40=%q 45=%q 46=%q", seen[40], seen[45], seen[46])
	}
}

func TestDeliveryMetricsFix3InsertedRunKeepsPausedResume(t *testing.T) {
	// Risk: a completed run inserted ahead of the paused run clears the pause.
	// The paused run no longer matches, the step errors, and the stored cursor
	// retries that same failure.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	day := dayStart(now)
	head := strings.Repeat("e", 40)
	list := "/actions/workflows/ci.yml/runs?created=" + day.Format("2006-01-02") + "&status=completed&per_page=100&page=1"
	calls := map[string]int{}
	get := func(path string, out any) error {
		calls[path]++
		switch {
		case path == list:
			return writeMetricJSON(out, map[string]any{"total_count": 2, "workflow_runs": []any{
				githubRunBody(900, 1, defaultCIWorkflow, "pull_request", "work/new", head, day.Add(3*time.Hour), 8*time.Minute, "success", 0),
				githubRunBody(800, 3, defaultCIWorkflow, "pull_request", "work/paused", head, day.Add(2*time.Hour), 8*time.Minute, "failure", 0),
			}})
		case path == "/actions/runs/800/attempts/2":
			return writeMetricJSON(out, githubRunBody(800, 2, defaultCIWorkflow, "pull_request", "work/paused", head, day.Add(time.Hour), 8*time.Minute, "success", 0))
		case path == "/actions/runs/800/attempts/1":
			return writeMetricJSON(out, githubRunBody(800, 1, defaultCIWorkflow, "pull_request", "work/paused", head, day.Add(30*time.Minute), 8*time.Minute, "failure", 0))
		case path == "":
			return writeMetricJSON(out, map[string]any{"full_name": "example/delivery", "default_branch": "main"})
		case strings.HasPrefix(path, "/pulls"):
			return writeMetricJSON(out, []any{})
		case strings.HasPrefix(path, "/actions/workflows/"):
			return writeMetricJSON(out, map[string]any{"total_count": 0, "workflow_runs": []any{}})
		default:
			return fmt.Errorf("unexpected GitHub read %s", path)
		}
	}
	cursor := backfillCursor{Since: day, Phase: "runs", Day: day.Format("2006-01-02"), Page: 1, AttemptRun: 800, Attempt: 2}
	next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
	if err != nil {
		t.Fatalf("inserted run stalled the paused resume: %v", err)
	}
	if calls["/actions/runs/800/attempts/1"] != 0 {
		t.Fatalf("paused run restarted at attempt 1: %v", calls)
	}
	got := map[string]bool{}
	for _, run := range batch.Runs {
		got[fmt.Sprintf("%d:%d:%s", run.ID, run.Attempt, run.Conclusion)] = true
	}
	for _, want := range []string{"900:1:success", "800:2:success", "800:3:failure"} {
		if !got[want] {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
	if next.Truncated || (next.AttemptRun == 800 && next.Attempt == 2) {
		t.Fatalf("resume did not move: %+v", next)
	}
	if _, _, err = backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, next, now); err != nil {
		t.Fatalf("following step still fails: %v", err)
	}
}
