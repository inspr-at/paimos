// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"time"
)

// The Delivery numbers (AEON-993, AEON-1016) follow the Project Arion v5
// measurement contract. Every definition below is the one measure-v2.py uses
// (Arion v5, "Numbers are reproducible"): same population, same exclusions,
// same percentile. A run is counted on the day it was created, as measure-v2
// does, and a pull request on the day it merged.

const defaultPreflightWorkflow = ".github/workflows/ci-preflight.yml"

// The first and second count a metric reports (metricSpec.tagNames, in bit order).
const (
	bitFirst  uint8 = 1 << 0
	bitSecond uint8 = 1 << 1
)

// defaultRequiredChecks are the required checks of Project Arion's ruleset,
// used when a project names none.
var defaultRequiredChecks = []string{"go", "web", "release-check", "e2e", "migration-compat"}

func sameWorkflow(run metricRun, workflow string) bool {
	return run.Workflow == workflow || path.Base(run.Workflow) == path.Base(workflow)
}

func failed(conclusion string) bool {
	return conclusion == "failure" || conclusion == "timed_out" || conclusion == "startup_failure"
}

// round2 is the rounding measure-v2 applies to every duration before it takes a
// percentile. Whole seconds never land on a tie, so half-up equals Python's round.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func minutes2(from, to time.Time) float64 { return round2(minutes(from, to)) }

// notCounted are the conclusions measure-v2 leaves out of a first-attempt count.
func notCounted(conclusion string) bool {
	switch conclusion {
	case "cancelled", "neutral", "action_required", "stale", "skipped":
		return true
	}
	return false
}

// requiredStatus is the verdict of the required checks of one attempt-1 run:
// "red" when any required job did not succeed or was missing, "green" otherwise
// (skipped counts as passing, as GitHub treats it), "unknown" when the jobs were
// not read or were read for other check names. Unknown is never guessed.
func requiredStatus(run metricRun, required []string) string {
	if !run.JobsRead || len(required) == 0 {
		return "unknown"
	}
	red := false
	for _, name := range required {
		conclusion, ok := run.Required[name]
		if !ok {
			return "unknown"
		}
		switch conclusion {
		case "failure", "timed_out", "cancelled", "missing", "action_required", "startup_failure":
			red = true
		}
	}
	if red {
		return "red"
	}
	return "green"
}

// ciIndex groups the CI workflow's attempts once for every number.
type ciIndex struct {
	attempts map[int64][]metricRun
	first    map[string][]metricRun
	pull     []metricRun
}

func indexCI(runs []metricRun, workflow string, now time.Time) ciIndex {
	x := ciIndex{attempts: map[int64][]metricRun{}, first: map[string][]metricRun{}}
	for _, run := range runs {
		// A run that completes after now is not a fact yet.
		if !sameWorkflow(run, workflow) || run.Completed.After(now) {
			continue
		}
		x.attempts[run.ID] = append(x.attempts[run.ID], run)
		if run.Event == "pull_request" {
			x.pull = append(x.pull, run)
		}
		if run.Attempt == 1 && run.Conclusion != "skipped" {
			x.first[run.Event] = append(x.first[run.Event], run)
		}
	}
	for _, rows := range x.first {
		sort.SliceStable(rows, func(i, j int) bool { return runBefore(rows[i], rows[j]) })
	}
	return x
}

// runBefore orders runs by creation, then id, then attempt: a stable order for
// ties, so a recomputation names the same earliest run.
func runBefore(a, b metricRun) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.Before(b.Created)
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	return a.Attempt < b.Attempt
}

// firstGreen is the earliest completion of a successful attempt.
func firstGreen(rows []metricRun) *time.Time {
	var out *time.Time
	for _, run := range rows {
		if run.Conclusion == "success" && (out == nil || run.Completed.Before(*out)) {
			v := run.Completed
			out = &v
		}
	}
	return out
}

// commit is one head commit of a pull-request branch with the runs it got.
type commit struct {
	branch, head string
	first        metricRun
	rows         []metricRun
	tree         string
	green        *time.Time
	superseded   bool
	// a1 is the first attempt of the commit's first run; a1Known when stored.
	a1      metricRun
	a1Known bool
}

type branchHistory struct {
	name    string
	rows    []metricRun
	commits []*commit
}

// pullHistory groups the CI workflow's pull_request attempts by branch and head
// commit. Rows are in creation order.
func pullHistory(x ciIndex) []*branchHistory {
	byBranch := map[string]*branchHistory{}
	byCommit := map[string]*commit{}
	rows := append([]metricRun(nil), x.pull...)
	sort.SliceStable(rows, func(i, j int) bool { return runBefore(rows[i], rows[j]) })
	for _, run := range rows {
		if run.Branch == "" {
			continue
		}
		b := byBranch[run.Branch]
		if b == nil {
			b = &branchHistory{name: run.Branch}
			byBranch[run.Branch] = b
		}
		b.rows = append(b.rows, run)
		key := run.Branch + "\x00" + run.Head
		c := byCommit[key]
		if c == nil {
			c = &commit{branch: run.Branch, head: run.Head, first: run}
			byCommit[key] = c
			b.commits = append(b.commits, c)
		}
		c.rows = append(c.rows, run)
		if c.tree == "" {
			c.tree = run.HeadTree
		}
	}
	out := make([]*branchHistory, 0, len(byBranch))
	for _, b := range byBranch {
		for _, c := range b.commits {
			c.green = firstGreen(c.rows)
			for _, row := range c.rows {
				if row.ID == c.first.ID && row.Attempt == 1 {
					c.a1, c.a1Known = row, true
				}
			}
			if !c.a1Known {
				c.a1 = c.first
			}
			for _, later := range b.rows {
				if later.Created.After(c.first.Created) && later.Head != c.head {
					c.superseded = true
					break
				}
			}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// suspects are the runs whose red first attempt was followed, on the next
// commit of the branch, by a green first attempt on the very same tree: nothing
// changed but the commit, and no re-run was asked. Every such case meets the
// Arion v5 definition (no change in its owner package, spec or fixtures); the
// wider form needs the complete impact map of WP1.4 and is not counted.
func suspects(history []*branchHistory) map[int64]bool {
	out := map[int64]bool{}
	for _, b := range history {
		for i := 0; i+1 < len(b.commits); i++ {
			c, next := b.commits[i], b.commits[i+1]
			if (c.a1.Conclusion == "failure" || c.a1.Conclusion == "timed_out") && c.green == nil &&
				next.a1.Conclusion == "success" && c.tree != "" && c.tree == next.tree {
				out[c.first.ID] = true
			}
		}
	}
	return out
}

func queuePR(run metricRun) (int64, bool) {
	match := queueBranch.FindStringSubmatch(run.Branch)
	if match == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(match[1], 10, 64)
	return n, err == nil
}

// computeMetrics derives the Delivery numbers. It is pure: tests pass fixed
// facts and an injected now.
func computeMetrics(in metricInput, now time.Time) []Metric {
	return computeMetricsFor(in, now, metricWindowDays)
}

// computeMetricsFor is computeMetrics for chosen windows, so a test can hold the
// numbers against a fixed measure-v2 window.
func computeMetricsFor(in metricInput, now time.Time, windows []int) []Metric {
	now = now.UTC()
	ci := path.Base(in.CIWorkflow)
	nightly := path.Base(in.NightlyWorkflow)
	preflight := in.PreflightWorkflow
	if preflight == "" {
		preflight = defaultPreflightWorkflow
	}
	required := in.Required
	if len(required) == 0 {
		required = defaultRequiredChecks
	}
	runSource := func(event string) string {
		return fmt.Sprintf("GitHub Actions workflow %s, event %s (GitHub App check suites and backfill)", ci, event)
	}
	jobSource := func(event string) string {
		return fmt.Sprintf("GitHub Actions jobs of workflow %s, event %s (GitHub App check suites and backfill)", ci, event)
	}
	noRuns := "No completed " + ci + " runs in the last 30 days."
	spec := func(s metricSpec) metricSpec {
		s.coverage, s.coverageEnd, s.readFrom = in.Covered, in.CoveredUntil, in.ReadFrom
		return s
	}
	build := func(s metricSpec, samples []metricPoint) Metric {
		return s.buildFor(now, samples, in.Truncated, windows)
	}
	out := []Metric{}
	x := indexCI(in.Runs, in.CIWorkflow, now)

	// The pull_request first attempts measure-v2 counts: cancelled, neutral,
	// action_required, stale and skipped runs are left out.
	counted := []metricRun{}
	for _, run := range x.first["pull_request"] {
		if !notCounted(run.Conclusion) {
			counted = append(counted, run)
		}
	}
	// Missing job facts: a run whose required checks or waits are unknown. The
	// instant is the run's creation, so a window holding one is partial.
	var requiredGaps, waitGaps, queueGaps, classifyGaps []time.Time
	for _, run := range counted {
		if requiredStatus(run, required) == "unknown" {
			requiredGaps = append(requiredGaps, run.Created)
		}
		if !run.JobsRead {
			waitGaps = append(waitGaps, run.Created)
		}
	}
	for _, run := range x.first["merge_group"] {
		if run.Conclusion != "cancelled" && requiredStatus(run, required) == "unknown" {
			queueGaps = append(queueGaps, run.Created)
		}
	}
	const gapReason = "without job facts yet, so their required checks and runner waits are not counted."

	// 1 and 2: first-attempt wall per run.
	prWall, queueWall := []metricPoint{}, []metricPoint{}
	wall := func(run metricRun) (metricPoint, bool) {
		if run.Started.IsZero() || run.Completed.Before(run.Started) {
			return metricPoint{}, false
		}
		return metricPoint{at: run.Created, value: minutes2(run.Started, run.Completed)}, true
	}
	for _, run := range counted {
		if point, ok := wall(run); ok {
			prWall = append(prWall, point)
		}
	}
	for _, run := range x.first["merge_group"] {
		if run.Conclusion == "cancelled" {
			continue
		}
		if point, ok := wall(run); ok {
			queueWall = append(queueWall, point)
		}
	}
	out = append(out, build(spec(metricSpec{number: 1, key: "pr_ci_wall", label: "PR CI duration (wall)", unit: "minutes", source: runSource("pull_request"),
		def:       "First attempt only: run started → run completed, per pull_request run, counted on the day the run was created. Runner wait before the start, re-runs and cancelled (superseded) runs are not included; time to green is number 4.",
		aggregate: "p50", target: arion(7, "max", "≤ 10 after Phases 1–2a, ≤ 7 if capacity and flake gates pass (conditional)"), noDataReason: noRuns}), prWall))
	out = append(out, build(spec(metricSpec{number: 2, key: "queue_run_wall", label: "Merge-queue run duration (wall)", unit: "minutes", source: runSource("merge_group"),
		def:       "First attempt only: run started → run completed, per merge_group run, counted on the day the run was created; cancelled runs are not included.",
		aggregate: "p50", target: arion(7, "max", "≤ 10 after Phases 1–2a, ≤ 7 if capacity and flake gates pass (conditional)"), noDataReason: "No completed " + ci + " merge-queue runs in the last 30 days."}), queueWall))

	// 3: first-attempt green, workflow and required checks side by side.
	green, requiredGreen := []metricPoint{}, []metricPoint{}
	history := pullHistory(x)
	suspect := suspects(history)
	flaky := []metricPoint{}
	for _, run := range counted {
		green = append(green, metricPoint{at: run.Created, value: boolValue(run.Conclusion == "success")})
		switch requiredStatus(run, required) {
		case "green":
			requiredGreen = append(requiredGreen, metricPoint{at: run.Created, value: 1})
		case "red":
			requiredGreen = append(requiredGreen, metricPoint{at: run.Created, value: 0})
		}
		// A confirmed flaky run: red on its first attempt, green on a later
		// attempt of the same run (same commit, same workflow).
		point := metricPoint{at: run.Created}
		if run.Conclusion == "failure" || run.Conclusion == "timed_out" {
			for _, later := range x.attempts[run.ID] {
				if later.Attempt > 1 && later.Conclusion == "success" {
					point.value, point.tags = 1, bitFirst
					break
				}
			}
		}
		if suspect[run.ID] {
			point.tags |= bitSecond
		}
		flaky = append(flaky, point)
	}
	out = append(out, build(spec(metricSpec{number: 3, key: "first_attempt_green", label: "First-attempt green rate", unit: "percent", source: runSource("pull_request"),
		def:       "Share of first attempts of pull_request runs that concluded success, counted on the day the run was created. Cancelled, neutral, action-required, stale and skipped runs are left out.",
		aggregate: "share", target: arion(80, "min", "≥ 70 % after Phases 1–2a (D4″)"), noDataReason: noRuns}), green))
	out = append(out, build(spec(metricSpec{number: 3, key: "required_checks_green", label: "Required checks green on the first attempt", unit: "percent", source: jobSource("pull_request"),
		def:       "Share of the same first attempts whose required checks (" + joinNames(required) + ") all succeeded or were skipped, the way GitHub's merge rule counts them. A run red only outside the required checks is green here. Runs whose jobs were not read yet are not counted and make the window partial.",
		aggregate: "share", target: arion(80, "min", "≥ 70 % after Phases 1–2a (D4″)"), noDataReason: noRuns, gaps: sortedTimes(requiredGaps), gapReason: gapReason}), requiredGreen))

	// 4 and 11: time to first green, per branch and per commit.
	branchPoints, commitPoints := []metricPoint{}, []metricPoint{}
	for _, b := range history {
		if len(b.commits) == 0 {
			continue
		}
		firstRun := b.rows[0]
		var branchGreen *time.Time
		for _, c := range b.commits {
			if c.green != nil && (branchGreen == nil || c.green.Before(*branchGreen)) {
				branchGreen = c.green
			}
			point := metricPoint{at: c.first.Created}
			if c.green == nil {
				point.none, point.tags = true, bitFirst
				if c.superseded {
					point.tags |= bitSecond
				}
			} else {
				point.value = minutes2(c.first.Created, *c.green)
			}
			commitPoints = append(commitPoints, point)
		}
		point := metricPoint{at: firstRun.Created}
		// first_run_green: attempt 1 of the branch's earliest run. Without a
		// stored attempt 1 the verdict is unknown and never claimed green.
		for _, row := range b.rows {
			if row.ID == firstRun.ID && row.Attempt == 1 && row.Conclusion == "success" {
				point.tags |= bitSecond
			}
		}
		if branchGreen == nil {
			point.none, point.tags = true, point.tags|bitFirst
		} else {
			point.value = minutes2(firstRun.Created, *branchGreen)
		}
		branchPoints = append(branchPoints, point)
	}
	out = append(out, build(spec(metricSpec{number: 4, key: "time_to_first_green", label: "Time to first green (per branch)", unit: "minutes", source: runSource("pull_request"),
		def:       "Per pull request branch, counted on the day its first run was created: first run created → first successful run completed on any commit, including runner wait, failed runs, fix pushes and re-runs. Branches that never went green are counted separately, never as a time; so is how many went green on their very first run.",
		aggregate: "p50", target: arion(20, "max", "p50 ≤ 30 min and p90 ≤ 240 after Phases 1–2a; p50 ≤ 20 and p90 ≤ 120 later"), tagNames: []string{"never_green", "first_run_green"},
		noDataReason: "No pull request branch reached a green " + ci + " run in the last 30 days."}), branchPoints))
	out = append(out, build(spec(metricSpec{number: 11, key: "time_to_first_green_commit", label: "Time to first green (per commit)", unit: "minutes", source: runSource("pull_request"),
		def:       "Per head commit of a pull request branch, counted on the day its first run was created: first run created → first successful attempt completed on that same commit. Commits that never went green are counted separately, and how many of them a later commit on the branch superseded.",
		aggregate: "p50", target: nil, tagNames: []string{"never_green", "superseded"},
		noDataReason: "No pull request commit reached a green " + ci + " run in the last 30 days."}), commitPoints))

	// 12: confirmed flaky runs, and the suspects beside them.
	out = append(out, build(spec(metricSpec{number: 12, key: "flaked_failures", label: "Runs with a confirmed flaky execution", unit: "percent", source: runSource("pull_request"),
		def:       "Share of first attempts of pull_request runs that failed and then passed on a later attempt of the same run, so same commit and same workflow: a confirmed flaky execution. Beside it, suspects: a run that failed on a commit whose next commit passed on the very same tree. A suspect is not confirmed; the wider form (no change in the failing owner package) needs the complete impact map of WP1.4 and is not counted.",
		aggregate: "share", target: arion(2, "max", "Measured first; then ≤ 2 % of runs"), tagNames: []string{"confirmed", "suspect"}, noDataReason: noRuns}), flaky))

	// 5: pull request opened → merged, counted on the merge day.
	merged := []metricPoint{}
	for _, pull := range in.Pulls {
		if pull.Merged != nil && !pull.Merged.Before(pull.Opened) {
			merged = append(merged, metricPoint{at: *pull.Merged, value: minutes2(pull.Opened, *pull.Merged)})
		}
	}
	out = append(out, build(spec(metricSpec{number: 5, key: "pr_open_to_merged", label: "PR opened → merged", unit: "minutes", source: "GitHub pull requests (GitHub App pull_request events and backfill)",
		def:       "Per merged pull request: created → merged, counted on the merge day.",
		aggregate: "p50", target: arion(60, "max", "p50 ≤ 80 min and p90 ≤ 360 after Phases 1–2a; p50 ≤ 60 and p90 ≤ 180 later (hypothesis)"), noDataReason: "No pull request was merged in the last 30 days."}), merged))

	// 6, 13, 14: the merge queue, per merged pull request.
	queued := map[int64][]metricRun{}
	queuedFirst := map[int64][]metricRun{}
	for _, rows := range x.attempts {
		for _, run := range rows {
			if run.Event != "merge_group" {
				continue
			}
			if n, ok := queuePR(run); ok {
				queued[n] = append(queued[n], run)
				if run.Attempt == 1 {
					queuedFirst[n] = append(queuedFirst[n], run)
				}
			}
		}
	}
	perPR, bases, unclassified := []metricPoint{}, []metricPoint{}, []metricPoint{}
	for _, pull := range in.Pulls {
		if pull.Merged == nil {
			continue
		}
		var attempts int
		for _, run := range queued[pull.Number] {
			if !run.Created.After(*pull.Merged) {
				attempts++
			}
		}
		if attempts == 0 {
			continue
		}
		perPR = append(perPR, metricPoint{at: *pull.Merged, value: float64(attempts)})
		// Queue runs before the one that merged: what made the earlier ones go?
		runs := []metricRun{}
		for _, run := range queuedFirst[pull.Number] {
			if !run.Created.After(*pull.Merged) {
				runs = append(runs, run)
			}
		}
		sort.SliceStable(runs, func(i, j int) bool { return runBefore(runs[i], runs[j]) })
		other, unknown := 0, false
		for i := 0; i+1 < len(runs); i++ {
			run := runs[i]
			if run.Conclusion == "cancelled" {
				other++
				continue
			}
			switch requiredStatus(run, required) {
			case "red": // an inferred ejection: a required check failed
			case "green":
				other++
			default:
				unknown = true
			}
		}
		if unknown {
			classifyGaps = append(classifyGaps, *pull.Merged)
		}
		bases = append(bases, metricPoint{at: *pull.Merged, den: 1})
		unclassified = append(unclassified, metricPoint{at: *pull.Merged, value: float64(other), den: 1})
	}
	out = append(out, build(spec(metricSpec{number: 6, key: "queue_runs_per_pr", label: "Queue runs per PR", unit: "runs", source: runSource("merge_group"),
		def:       "Per merged pull request named in the merge-queue branch (gh-readonly-queue/…/pr-N-…): every queue run attempt created up to its merge, counted on the day it merged. A pull request that was queued and did not merge is not counted. The value is the mean; 1.0 means every counted pull request merged on its first queue run. A group run counts for the pull request its branch names.",
		aggregate: "mean", target: arion(1.1, "max", "≤ 1.25 after Phases 1–2a"), noDataReason: "No merged pull request had a merge-queue run in the last 30 days."}), perPR))
	ejections := []metricPoint{}
	for _, run := range x.first["merge_group"] {
		if run.Conclusion != "cancelled" && requiredStatus(run, required) == "red" {
			ejections = append(ejections, metricPoint{at: run.Created, value: 1})
		}
	}
	ejections = append(ejections, bases...)
	out = append(out, build(spec(metricSpec{number: 13, key: "queue_ejections", label: "Inferred ejections per 100 merged PRs", unit: "per100", source: jobSource("merge_group"),
		def:       "Merge-queue runs whose required check was red (a pull request GitHub took out of the queue, inferred: the queue's own events are not joined yet), per 100 merged pull requests that had a queue run. Runs are counted on the day they were created, pull requests on the day they merged. Runs whose jobs were not read yet are not counted and make the window partial.",
		aggregate: "per100", target: arion(10, "max", "≤ 25 after Phases 1–2a"), gaps: sortedTimes(queueGaps), gapReason: gapReason,
		noDataReason: "No merged pull request had a merge-queue run in the last 30 days."}), ejections))
	out = append(out, build(spec(metricSpec{number: 14, key: "queue_unclassified", label: "Additional non-required-red queue runs per 100 merged PRs, cause unclassified", unit: "per100", source: jobSource("merge_group"),
		def:       "Per 100 merged pull requests that had a queue run: the queue runs before the one that merged that were not red in a required check (cancelled, or required checks green), so predecessor failure, reordering or manual removal cannot be told apart yet. Queue event correlation (WP1.6) classifies them; until then the cause stays unclassified.",
		aggregate: "per100", target: nil, gaps: sortedTimes(classifyGaps), gapReason: gapReason,
		noDataReason: "No merged pull request had a merge-queue run in the last 30 days."}), unclassified))

	// 7: review time and the share of "changes" verdicts.
	reviewTime, changes := []metricPoint{}, []metricPoint{}
	var reviewCovered *time.Time
	reported := map[string]bool{}
	for _, mark := range in.Marks {
		if mark.Kind != "review" || mark.Started == nil {
			continue
		}
		if mark.Head != "" {
			reported[mark.Head] = true
		}
		reviewTime = append(reviewTime, metricPoint{at: mark.At, value: minutes(*mark.Started, mark.At)})
		changes = append(changes, metricPoint{at: mark.At, value: boolValue(mark.Outcome == "changes")})
		reviewCovered = earlier(reviewCovered, mark.At)
	}
	statuses := map[string]map[string]time.Time{}
	for _, mark := range in.Marks {
		if mark.Kind == "review_status" && !reported[mark.Head] {
			if statuses[mark.Head] == nil {
				statuses[mark.Head] = map[string]time.Time{}
			}
			statuses[mark.Head][mark.Outcome] = mark.At
			reviewCovered = earlier(reviewCovered, mark.At)
		}
	}
	for _, states := range statuses {
		pending, ok := states["pending"]
		var verdict *time.Time
		outcome := ""
		for _, state := range []string{"success", "failure"} {
			if at, found := states[state]; found && !at.Before(pending) && (verdict == nil || at.Before(*verdict)) {
				v := at
				verdict, outcome = &v, state
			}
		}
		if verdict == nil {
			continue
		}
		if ok {
			reviewTime = append(reviewTime, metricPoint{at: *verdict, value: minutes(pending, *verdict)})
		}
		changes = append(changes, metricPoint{at: *verdict, value: boolValue(outcome == "failure")})
	}
	reviewPartial := "Counts only reviews that post the aeon/review commit status or are reported through POST …/delivery/metrics/facts; gates that run elsewhere and are not reported are missing."
	reviewNone := "No review verdict was seen in the last 30 days: no aeon/review status and no reported review."
	out = append(out, build(metricSpec{number: 7, key: "review_time", label: "Review time", unit: "minutes", source: "aeon/review commit status (GitHub App status events) and reported reviews",
		def:       "Per reviewed head: review requested (aeon/review pending, or the reported start) → first verdict.",
		aggregate: "p50", target: arion(8, "max", "≤ 10 min after Phases 1–2a (WP1.8)"), coverage: reviewCovered, alwaysPartial: true, partialReason: reviewPartial, noDataReason: reviewNone, readFrom: in.ReadFrom}, reviewTime))
	out = append(out, build(metricSpec{number: 7, key: "review_changes_share", label: "Share of “changes” verdicts", unit: "percent", source: "aeon/review commit status (GitHub App status events) and reported reviews",
		def:       "Share of first verdicts per head that asked for changes (aeon/review failure, or a reported “changes”).",
		aggregate: "share", target: arion(25, "max", "≤ 40 % after Phases 1–2a (WP1.8)"), coverage: reviewCovered, alwaysPartial: true, partialReason: reviewPartial, noDataReason: reviewNone, readFrom: in.ReadFrom}, changes))

	// 8, 9: reported facts only; coverage starts with the first report.
	rounds, releases := []metricPoint{}, []metricPoint{}
	var roundsCovered, releasesCovered *time.Time
	for _, mark := range in.Marks {
		switch mark.Kind {
		case "merge_round":
			rounds = append(rounds, metricPoint{at: mark.At, value: boolValue(mark.Outcome == "model")})
			roundsCovered = earlier(roundsCovered, mark.At)
		case "release":
			if mark.Started != nil && mark.Outcome == "live" {
				releases = append(releases, metricPoint{at: mark.At, value: minutes(*mark.Started, mark.At)})
			}
			releasesCovered = earlier(releasesCovered, mark.At)
		}
	}
	out = append(out, build(metricSpec{number: 8, key: "merge_rounds_model_share", label: "Merge rounds solved by a model", unit: "percent", source: "Merge rounds reported through POST …/delivery/metrics/facts (kind merge_round)",
		def:       "Share of merge rounds (a PR conflicting with a moved main) that needed a model instead of the scripted merge. PAIMOS cannot see merge rounds itself; it counts what the merge tooling reports.",
		aggregate: "share", target: arion(20, "max", "WP1.7"), coverage: roundsCovered, noDataReason: "No merge round has been reported yet.", readFrom: in.ReadFrom}, rounds))
	release := build(metricSpec{number: 9, key: "release_queue_to_live", label: "Release: merge queue → live", unit: "minutes", source: "Releases reported through POST …/delivery/metrics/facts (kind release)",
		def:       "Per release: the release PR enters the merge queue → the version is live on the production host, as reported by the release tooling. The wait for a person (W) is inside this time. PAIMOS cannot observe the host itself.",
		aggregate: "p50", target: arion(54, "max", "about 61 min + W now, about 54 min + W after Phases 2 and 4; W is the wait for a person"), coverage: releasesCovered, noDataReason: "No release has been reported yet.", readFrom: in.ReadFrom}, releases)
	facts := releaseFacts(in.Marks, now)
	release.Releases = &facts
	out = append(out, release)

	// 10: nightly full run, one verdict per night.
	nights := map[string]metricRun{}
	finals := map[int64]metricRun{}
	for _, run := range in.Runs {
		if !sameWorkflow(run, in.NightlyWorkflow) || run.Completed.After(now) {
			continue
		}
		if old, ok := finals[run.ID]; !ok || run.Attempt > old.Attempt {
			finals[run.ID] = run
		}
	}
	for _, run := range finals {
		night := dayStart(run.Created).Format("2006-01-02")
		old, ok := nights[night]
		scheduled := run.Event == "schedule"
		if !ok || scheduled && old.Event != "schedule" || scheduled == (old.Event == "schedule") && run.Created.Before(old.Created) {
			nights[night] = run
		}
	}
	nightPoints := []metricPoint{}
	for _, run := range nights {
		nightPoints = append(nightPoints, metricPoint{at: run.Created, value: boolValue(run.Conclusion == "success")})
	}
	out = append(out, build(spec(metricSpec{number: 10, key: "nightly_green", label: "Nightly full run green", unit: "percent", source: fmt.Sprintf("GitHub Actions workflow %s (GitHub App check suites and backfill)", nightly),
		def:       "One verdict per night (UTC day the run was created): the scheduled run's last attempt, else the night's first run; green only on success. The value is the share of green nights; nights without a run are not counted.",
		aggregate: "share", target: arion(100, "min", "7 green nights in a row (WP1.2)"), noDataReason: "No completed " + nightly + " run in the last 30 days."}), nightPoints))

	// 15: the longest wait for a runner of any job, per run.
	waits := []metricPoint{}
	for _, run := range counted {
		if run.JobsRead && run.WorstWaitMS != nil {
			waits = append(waits, metricPoint{at: run.Created, value: round2(float64(*run.WorstWaitMS) / 60000)})
		}
	}
	out = append(out, build(spec(metricSpec{number: 15, key: "runner_wait", label: "Runner wait, worst job per run", unit: "minutes", source: jobSource("pull_request"),
		def:       "Per first attempt of a pull_request run, counted on the day it was created: the longest wait of any job that ran, job created → job started. The value is p90 of that wait across runs; p50 sits beside it. Skipped jobs have no wait. Runs whose jobs were not read yet are not counted and make the window partial.",
		aggregate: "p90", target: arion(1, "max", "p90 ≤ 3 min after Phases 1–2a"), gaps: sortedTimes(waitGaps), gapReason: gapReason, noDataReason: noRuns}), waits))

	// 16: preflight runs that ended red, on their own line.
	pre := []metricPoint{}
	for _, run := range in.Runs {
		if sameWorkflow(run, preflight) && run.Attempt == 1 && !notCounted(run.Conclusion) && !run.Completed.After(now) {
			pre = append(pre, metricPoint{at: run.Created, value: boolValue(run.Conclusion != "success")})
		}
	}
	out = append(out, build(spec(metricSpec{number: 16, key: "preflight_red_rate", label: "Preflight red rate", unit: "percent", source: fmt.Sprintf("GitHub Actions workflow %s (GitHub App check suites and backfill)", path.Base(preflight)),
		def:       "Share of exact-commit preflight runs (first attempt) that ended red, counted on the day the run was created. Preflight runs before a pull request opens; they are their own line and never count as CI greens.",
		aggregate: "share", target: nil, noDataReason: "No completed " + path.Base(preflight) + " run in the last 30 days."}), pre))

	// 17, 18: reported audits and defects, and rollout incidents.
	audits, defects := []metricPoint{}, []metricPoint{}
	var auditCovered, defectCovered *time.Time
	for _, report := range in.Reports {
		switch report.Kind {
		case "review_audit":
			audits = append(audits, metricPoint{at: report.At, value: 1, tags: severityTags(report.Severity, "none")})
			auditCovered = earlier(auditCovered, report.At)
		case "escaped_defect":
			defects = append(defects, metricPoint{at: report.At, value: 1, tags: severityTags(report.Severity, "")})
			defectCovered = earlier(defectCovered, report.At)
		}
	}
	for _, incident := range in.Incidents {
		defects = append(defects, metricPoint{at: incident.At, value: 1, tags: severityTags(incident.Severity, "")})
	}
	if in.IncidentCovered != nil {
		defectCovered = earlier(defectCovered, *in.IncidentCovered)
	}
	out = append(out, build(metricSpec{number: 17, key: "review_audits", label: "Review audit findings", unit: "audits", source: "Review audits reported through POST …/delivery/metrics/facts (kind review_audit)",
		def:       "Merged pull requests re-reviewed by a second model of another family, counted on the day the audit was reported, with the highest severity of each audit's findings: clean, low, medium or high. PAIMOS cannot see audits itself; it counts what the audit tooling reports.",
		aggregate: "count", target: nil, tagNames: []string{"high", "medium", "low", "clean"}, coverage: auditCovered, noDataReason: "No review audit has been reported yet.", readFrom: in.ReadFrom}, audits))
	out = append(out, build(metricSpec{number: 18, key: "escaped_defects", label: "Escaped defects", unit: "defects", source: "Defects reported through POST …/delivery/metrics/facts (kind escaped_defect) and rollout incidents",
		def:       "Bug or incident tickets linked as caused by a merged pull request or a release (reported with the link), plus rollout incidents of releases (down counts as high, degraded as medium), counted on the day they were reported or began, with their severity. Main-branch red is not a defect that escaped.",
		aggregate: "count", target: nil, tagNames: []string{"high", "medium", "low"}, coverage: defectCovered, noDataReason: "No escaped defect has been reported and no rollout incident recorded yet.", readFrom: in.ReadFrom}, defects))
	return out
}

func sortedTimes(times []time.Time) []time.Time {
	out := append([]time.Time(nil), times...)
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func joinNames(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}

// severityTags sets the bit of a severity: high, medium, low and, when named,
// the clean bit last.
func severityTags(severity, clean string) uint8 {
	switch severity {
	case "high":
		return 1 << 0
	case "medium":
		return 1 << 1
	case "low":
		return 1 << 2
	}
	if clean != "" && severity == clean {
		return 1 << 3
	}
	return 0
}
