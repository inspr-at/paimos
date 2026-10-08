// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
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

func TestDeliveryMetricsFix4DoneRunGainsAttemptWhilePaused(t *testing.T) {
	// Risk: DoneRuns remembers only the run id. A run that already finished
	// on this page gains an attempt while another run is paused. Resume skips
	// the id, the page finishes untruncated, and the new verdict is missing.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	day := dayStart(now)
	head := strings.Repeat("f", 40)
	list := "/actions/workflows/ci.yml/runs?created=" + day.Format("2006-01-02") + "&status=completed&per_page=100&page=1"
	listed := 0
	calls := map[string]int{}
	get := func(path string, out any) error {
		calls[path]++
		switch {
		case path == list:
			listed++
			attempt, conclusion := 1, "failure"
			if listed > 1 {
				attempt, conclusion = 3, "success"
			}
			return writeMetricJSON(out, map[string]any{"total_count": 2, "workflow_runs": []any{
				githubRunBody(700, attempt, defaultCIWorkflow, "pull_request", "work/done", head, day.Add(time.Hour), 8*time.Minute, conclusion, 0),
				githubRunBody(800, 45, defaultCIWorkflow, "pull_request", "work/paused", head, day.Add(2*time.Hour), 8*time.Minute, "failure", 0),
			}})
		case strings.HasPrefix(path, "/actions/runs/700/attempts/"):
			n, err := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
			if err != nil || n < 1 || n > 3 {
				return fmt.Errorf("unexpected attempt %s", path)
			}
			conclusion := "failure"
			if n == 3 {
				conclusion = "success"
			}
			return writeMetricJSON(out, githubRunBody(700, n, defaultCIWorkflow, "pull_request", "work/done", head, day.Add(time.Duration(n)*time.Minute), 8*time.Minute, conclusion, 0))
		case strings.HasPrefix(path, "/actions/runs/800/attempts/"):
			n, err := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
			if err != nil || n < 1 || n >= 45 {
				return fmt.Errorf("unexpected attempt %s", path)
			}
			return writeMetricJSON(out, githubRunBody(800, n, defaultCIWorkflow, "pull_request", "work/paused", head, day.Add(time.Duration(n)*time.Minute), 8*time.Minute, "failure", 0))
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
	seen := map[string]bool{}
	for steps := 0; cursor.Phase != "done"; steps++ {
		if steps > 6 {
			t.Fatalf("backfill did not finish: %+v", cursor)
		}
		next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Calls > backfillCallBudget {
			t.Fatalf("step used %d calls", batch.Calls)
		}
		for _, run := range batch.Runs {
			seen[fmt.Sprintf("%d:%d:%s", run.ID, run.Attempt, run.Conclusion)] = true
		}
		if steps == 0 {
			if next.AttemptRun != 800 || next.Attempt != 40 || !seen["700:1:failure"] || seen["700:2:failure"] || seen["700:3:success"] {
				t.Fatalf("first step did not pause after storing run 700 once: cursor %+v", next)
			}
			raw, err := json.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				DoneRuns json.RawMessage `json:"done_runs"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			var rows []struct {
				ID      int64 `json:"id"`
				Attempt int   `json:"attempt"`
			}
			if err := json.Unmarshal(wire.DoneRuns, &rows); err != nil || len(rows) != 1 || rows[0].ID != 700 || rows[0].Attempt != 1 {
				t.Fatalf("persisted done run = %s (%v)", wire.DoneRuns, err)
			}
			if err := json.Unmarshal(raw, &cursor); err != nil {
				t.Fatalf("stored cursor did not load: %v", err)
			}
			continue
		}
		cursor = next
	}
	if cursor.Truncated {
		t.Fatal("backfill finished truncated")
	}
	for _, want := range []string{"700:1:failure", "700:2:failure", "700:3:success"} {
		if !seen[want] {
			t.Fatalf("missing %s in %v", want, seen)
		}
	}
	if calls["/actions/runs/700/attempts/1"] != 0 || calls["/actions/runs/700/attempts/2"] != 1 {
		t.Fatalf("done run was reread or its new attempt was skipped: %v", calls)
	}
}

func TestDeliveryMetricsFix4LegacyDoneRunIDStillReadsNewerAttempt(t *testing.T) {
	// Risk: a cursor stored before attempts were recorded is a list of run
	// ids. Resume treats that id as finished and drops an attempt that
	// completed while another run was paused.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	day := dayStart(now)
	head := strings.Repeat("a", 40)
	list := "/actions/workflows/ci.yml/runs?created=" + day.Format("2006-01-02") + "&status=completed&per_page=100&page=1"
	calls := map[string]int{}
	get := func(path string, out any) error {
		calls[path]++
		switch {
		case path == list:
			return writeMetricJSON(out, map[string]any{"total_count": 2, "workflow_runs": []any{
				githubRunBody(700, 2, defaultCIWorkflow, "pull_request", "work/done", head, day.Add(2*time.Hour), 8*time.Minute, "success", 0),
				githubRunBody(800, 45, defaultCIWorkflow, "pull_request", "work/paused", head, day.Add(time.Hour), 8*time.Minute, "failure", 0),
			}})
		case path == "/actions/runs/700/attempts/1":
			return writeMetricJSON(out, githubRunBody(700, 1, defaultCIWorkflow, "pull_request", "work/done", head, day.Add(time.Hour), 8*time.Minute, "failure", 0))
		case strings.HasPrefix(path, "/actions/runs/800/attempts/"):
			n, err := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
			if err != nil || n < 44 || n >= 45 {
				return fmt.Errorf("unexpected attempt %s", path)
			}
			return writeMetricJSON(out, githubRunBody(800, n, defaultCIWorkflow, "pull_request", "work/paused", head, day.Add(time.Duration(n)*time.Minute), 8*time.Minute, "failure", 0))
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
	var cursor backfillCursor
	raw := []byte(`{"since":"2026-10-08T00:00:00Z","phase":"runs","workflow":0,"day":"2026-10-08","page":1,"attempt_run":800,"attempt":44,"done_runs":[700]}`)
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	next, batch, err := backfillStep(get, "example/delivery", []string{defaultCIWorkflow}, cursor, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, run := range batch.Runs {
		got[fmt.Sprintf("%d:%d:%s", run.ID, run.Attempt, run.Conclusion)] = true
	}
	for _, want := range []string{"700:1:failure", "700:2:success", "800:44:failure", "800:45:failure"} {
		if !got[want] {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
	if calls["/actions/runs/800/attempts/1"] != 0 || (next.AttemptRun == 800 && next.Attempt <= 44) {
		t.Fatalf("legacy cursor restarted or stuck the paused run: calls %v cursor %+v", calls, next)
	}
}
