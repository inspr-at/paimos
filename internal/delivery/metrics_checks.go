// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

const historyActions = "actions"
const historyChecks = "checks"

// Negative run identities reserve a disjoint namespace for check suites. They
// are observations, never an assertion about an Actions run's first attempt.
func checksRun(r metricRun) bool { return r.ID < 0 }

// Exact Actions facts win over a suite-derived sample for the same head and
// workflow. This also makes permission changes idempotent without deleting facts.
func preferredMetricRuns(runs []metricRun) []metricRun {
	exact := map[string]bool{}
	key := func(r metricRun) string { return r.Head + "/" + path.Base(r.Workflow) }
	for _, r := range runs {
		if !checksRun(r) {
			exact[key(r)] = true
		}
	}
	out := make([]metricRun, 0, len(runs))
	for _, r := range runs {
		if !checksRun(r) || !exact[key(r)] {
			out = append(out, r)
		}
	}
	return out
}

func annotateChecksMetrics(metrics []Metric, onlyChecks bool) {
	for i := range metrics {
		m := &metrics[i]
		switch m.Key {
		case "pr_ci_wall", "queue_run_wall":
			m.Definition = "Observed check span: earliest check start to latest completion in a suite identified by the known CI aggregate names. Approximates workflow wall time; first attempts, older PR heads and deleted queue heads cannot be recovered."
		case "nightly_green":
			m.Definition = "Share of observed green nightly suites, one observation per UTC day of check start. Known nightly aggregate names identify the workflow; schedule events and complete rerun history are unavailable."
		case "first_attempt_green", "required_checks_green", "time_to_first_green", "time_to_first_green_commit", "flaked_failures", "queue_runs_per_pr", "queue_ejections", "queue_unclassified", "runner_wait", "preflight_red_rate":
			if onlyChecks {
				m.Status, m.Latest = "no_data", nil
				for j := range m.Windows {
					w := &m.Windows[j]
					w.Status, w.N, w.Value, w.P50, w.P90, w.Counts = "no_data", 0, nil, nil, nil, nil
					w.Coverage.Full = false
					if p := w.Previous; p != nil {
						p.Status, p.N, p.Value, p.P50, p.P90, p.Coverage.Full = "no_data", 0, nil, nil, nil, false
					}
				}
				for j := range m.Daily {
					m.Daily[j].Status, m.Daily[j].N, m.Daily[j].Value, m.Daily[j].P50, m.Daily[j].P90 = "no_data", 0, nil, nil, nil
				}
				for j := range m.Series {
					m.Series[j].Status, m.Series[j].N, m.Series[j].Value, m.Series[j].P50, m.Series[j].P90 = "no_data", 0, nil, nil, nil
				}
			}
		default:
			// PR timestamps, reported facts and production incidents retain their
			// exact definitions and values.
			continue
		}
		m.Source = "GitHub App checks + contents read (known aggregate names; partial history)"
		if m.Status == "ok" {
			m.Status = "partial"
		}
		for j := range m.Windows {
			w := &m.Windows[j]
			if w.Status == "ok" {
				w.Status = "partial"
			}
			w.Coverage.Full = false
			if p := w.Previous; p != nil {
				if p.Status == "ok" {
					p.Status = "partial"
				}
				p.Coverage.Full = false
			}
		}
		for j := range m.Daily {
			if m.Daily[j].Status == "ok" {
				m.Daily[j].Status = "partial"
			}
		}
		for j := range m.Series {
			if m.Series[j].Status == "ok" {
				m.Series[j].Status = "partial"
			}
		}
		note := "Checks-derived history is partial. Workflow attempts, first green, runner wait and deleted queue heads are unavailable; check spans and nightly verdicts are observations."
		m.Reason = &note
	}
}

type checksTarget struct {
	Head string `json:"head"`
	Tree string `json:"tree,omitempty"`
}

type metricCheckSuite struct {
	ID      int64     `json:"id"`
	Head    string    `json:"head_sha"`
	Branch  string    `json:"head_branch"`
	Status  string    `json:"status"`
	Created time.Time `json:"created_at"`
	App     struct {
		Slug string `json:"slug"`
	} `json:"app"`
	Pulls []struct {
		Number int64 `json:"number"`
	} `json:"pull_requests"`
	HeadCommit struct {
		Tree string `json:"tree_id"`
	} `json:"head_commit"`
}

type checksCursor struct {
	Stage   string             `json:"stage"`
	Page    int                `json:"page"`
	Anchor  string             `json:"anchor,omitempty"`
	Until   time.Time          `json:"until"`
	Targets []checksTarget     `json:"targets,omitempty"`
	Suites  []metricCheckSuite `json:"suites,omitempty"`
}

type metricCheckRun struct {
	ID         int64      `json:"id"`
	Head       string     `json:"head_sha"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Conclusion *string    `json:"conclusion"`
	Started    *time.Time `json:"started_at"`
	Completed  *time.Time `json:"completed_at"`
	Suite      struct {
		ID int64 `json:"id"`
	} `json:"check_suite"`
	App struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

func validMetricSuite(s metricCheckSuite) bool {
	if len(s.Pulls) > 100 {
		return false
	}
	for _, p := range s.Pulls {
		if p.Number <= 0 || p.Number > 2147483647 {
			return false
		}
	}
	return s.ID > 0 && reviewgate.ValidSHA(s.Head) && len(s.Branch) <= 255 &&
		!s.Created.IsZero() && len(s.Status) <= 32 && len(s.App.Slug) <= 100 &&
		(s.HeadCommit.Tree == "" || reviewgate.ValidSHA(s.HeadCommit.Tree))
}

// suiteCheckRuns uses the latest check verdicts in one suite. It can identify
// the known aggregate checks, but cannot recover attempts or job-created time.
// Lists that exceed the bound produce no guessed run and report truncation.
func suiteCheckRuns(get func(string, any) error, suite metricCheckSuite, ci, nightly, defaultBranch string, now time.Time) ([]metricRun, bool, error) {
	if !validMetricSuite(suite) {
		return nil, false, errRead
	}
	if suite.App.Slug != "github-actions" || suite.Status != "completed" {
		return nil, false, nil
	}
	var checks []metricCheckRun
	for page := 1; page <= jobPageLimit; page++ {
		var body struct {
			Total int              `json:"total_count"`
			Runs  []metricCheckRun `json:"check_runs"`
		}
		if err := get(fmt.Sprintf("/check-suites/%d/check-runs?filter=latest&per_page=100&page=%d", suite.ID, page), &body); err != nil {
			return nil, false, err
		}
		if body.Total < len(body.Runs) || len(body.Runs) > 100 || body.Total < 0 {
			return nil, false, errRead
		}
		if body.Total > jobPageLimit*100 {
			return nil, true, nil
		}
		checks = append(checks, body.Runs...)
		if len(checks) == body.Total {
			break
		}
		if len(checks) > body.Total || len(body.Runs) == 0 {
			return nil, false, errRead
		}
		if page == jobPageLimit {
			return nil, true, nil
		}
	}
	names := map[string]bool{}
	for _, check := range checks {
		if check.ID <= 0 || check.Suite.ID != suite.ID || check.Head != suite.Head || check.App.Slug != "github-actions" || check.Name == "" || len(check.Name) > jobNameLimit {
			return nil, false, errRead
		}
		names[check.Name] = true
	}
	known := func(prefix string) bool {
		for _, name := range defaultRequiredChecks {
			if !names[prefix+name] {
				return false
			}
		}
		return true
	}
	workflow, name := "", ""
	switch {
	case known("") && !known("nightly-"):
		workflow, name = ci, "CI (observed checks)"
	case known("nightly-") && !known(""):
		workflow, name = nightly, "Nightly (observed checks)"
	default:
		// Unknown or ambiguous job names cannot identify a workflow.
		return nil, false, nil
	}
	var started, completed time.Time
	conclusion := ""
	for _, check := range checks {
		if check.Status != "completed" || check.Conclusion == nil || *check.Conclusion == "" || len(*check.Conclusion) > 32 {
			return nil, false, nil
		}
		if conclusion == "" {
			conclusion = *check.Conclusion
		} else {
			conclusion = worseConclusion(conclusion, *check.Conclusion)
		}
		if check.Started == nil || check.Completed == nil || check.Started.IsZero() || check.Completed.Before(*check.Started) {
			// Skipped checks have no elapsed execution time.
			if *check.Conclusion == "skipped" || *check.Conclusion == "neutral" {
				continue
			}
			return nil, false, errRead
		}
		if started.IsZero() || check.Started.Before(started) {
			started = check.Started.UTC()
		}
		if check.Completed.After(completed) {
			completed = check.Completed.UTC()
		}
	}
	if started.IsZero() || completed.After(now) {
		return nil, false, nil
	}
	event := "observed"
	branch := strings.TrimPrefix(suite.Branch, "refs/heads/")
	var pr *int64
	if match := queueBranch.FindStringSubmatch(branch); match != nil {
		event = "merge_group"
		n, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, false, errRead
		}
		pr = &n
	} else if branch == defaultBranch && defaultBranch != "" {
		event = "push"
	} else if len(suite.Pulls) > 0 {
		if suite.Pulls[0].Number <= 0 {
			return nil, false, errRead
		}
		event, pr = "pull_request", &suite.Pulls[0].Number
	}
	return []metricRun{{ID: -suite.ID, Attempt: 1, Workflow: workflow, Name: name, Event: event, Branch: branch, Head: suite.Head, HeadTree: suite.HeadCommit.Tree,
		PR: pr, Created: started, Started: started, Completed: completed, Conclusion: conclusion}}, false, nil
}

// checksBackfillStep retains the same durable cursor and 40-read limit as the
// Actions reader. Pending heads/suites are bounded pages, committed with their
// facts. The default branch is pinned by SHA so new pushes cannot shift pages.
func checksBackfillStep(get func(string, any) error, repository, ci, nightly string, cursor backfillCursor, now time.Time) (backfillCursor, backfillBatch, error) {
	batch := backfillBatch{}
	if cursor.Checks == nil {
		cursor.Checks = &checksCursor{Stage: "main", Page: 1, Until: now.UTC()}
	}
	c := *cursor.Checks
	cursor.Checks = &c
	cursor.ReadSource, cursor.Truncated = historyChecks, true
	counted := func(path string, out any) error {
		if batch.Calls >= backfillCallBudget {
			return errRead
		}
		batch.Calls++
		return get(path, out)
	}
	if len(c.Targets) > 100 || len(c.Suites) > 100 || c.Page < 1 || c.Page > backfillPullPageCap || c.Until.IsZero() {
		return cursor, batch, errRead
	}
	if cursor.DefaultBranch == "" {
		var repo struct {
			Name    string `json:"full_name"`
			Default string `json:"default_branch"`
		}
		if err := counted("", &repo); err != nil {
			return cursor, batch, err
		}
		if repo.Name != repository || !metricDefaultBranch(repo.Default) {
			return cursor, batch, errRead
		}
		cursor.DefaultBranch = repo.Default
	}
	for batch.Calls < backfillCallBudget {
		if len(c.Suites) > 0 {
			// A suite may use five pages; never exceed the step budget.
			if batch.Calls > backfillCallBudget-jobPageLimit {
				return cursor, batch, nil
			}
			suite := c.Suites[0]
			runs, _, err := suiteCheckRuns(counted, suite, ci, nightly, cursor.DefaultBranch, now)
			if err != nil {
				return cursor, batch, err
			}
			batch.Runs = append(batch.Runs, runs...)
			c.Suites = c.Suites[1:]
			continue
		}
		if len(c.Targets) > 0 {
			head := c.Targets[0]
			if !reviewgate.ValidSHA(head.Head) {
				return cursor, batch, errRead
			}
			var body struct {
				Total  int                `json:"total_count"`
				Suites []metricCheckSuite `json:"check_suites"`
			}
			if err := counted("/commits/"+head.Head+"/check-suites?per_page=100&page=1", &body); err != nil {
				// Fork/deleted PR heads may be absent from this repository. The
				// repository itself was checked above; absence stays partial.
				if errors.Is(err, crossreview.ErrNotFound) {
					c.Targets = c.Targets[1:]
					continue
				}
				return cursor, batch, err
			}
			if len(body.Suites) > 100 || body.Total < len(body.Suites) {
				return cursor, batch, errRead
			}
			for _, suite := range body.Suites {
				if !validMetricSuite(suite) || suite.Head != head.Head {
					return cursor, batch, errRead
				}
				if suite.App.Slug != "github-actions" || suite.Created.Before(cursor.Since) || suite.Created.After(c.Until) {
					continue
				}
				if reviewgate.ValidSHA(head.Tree) {
					suite.HeadCommit.Tree = head.Tree
				}
				c.Suites = append(c.Suites, suite)
			}
			c.Targets = c.Targets[1:]
			continue
		}
		switch c.Stage {
		case "main":
			ref := c.Anchor
			if ref == "" {
				ref = cursor.DefaultBranch
			}
			var commits []struct {
				SHA    string `json:"sha"`
				Commit struct {
					Tree struct {
						SHA string `json:"sha"`
					} `json:"tree"`
				} `json:"commit"`
			}
			query := url.Values{"sha": {ref}, "since": {cursor.Since.Format(time.RFC3339)}, "until": {c.Until.Format(time.RFC3339)}, "per_page": {"100"}, "page": {strconv.Itoa(c.Page)}}
			if err := counted("/commits?"+query.Encode(), &commits); err != nil {
				return cursor, batch, err
			}
			if len(commits) > 100 {
				return cursor, batch, errRead
			}
			for _, commit := range commits {
				if !reviewgate.ValidSHA(commit.SHA) || !reviewgate.ValidSHA(commit.Commit.Tree.SHA) {
					return cursor, batch, errRead
				}
				c.Targets = append(c.Targets, checksTarget{Head: commit.SHA, Tree: commit.Commit.Tree.SHA})
			}
			if c.Anchor == "" && len(commits) > 0 {
				c.Anchor = commits[0].SHA
			}
			if len(commits) < 100 || c.Page == backfillPullPageCap {
				c.Stage, c.Page = "queues", 1
			} else {
				c.Page++
			}
		case "queues":
			var refs []struct {
				Ref    string `json:"ref"`
				Object struct {
					SHA string `json:"sha"`
				} `json:"object"`
			}
			prefix := "refs/heads/gh-readonly-queue/" + cursor.DefaultBranch + "/"
			if err := counted("/git/matching-refs/heads/gh-readonly-queue/"+url.PathEscape(cursor.DefaultBranch)+"/", &refs); err != nil && !errors.Is(err, crossreview.ErrNotFound) {
				return cursor, batch, err
			}
			if len(refs) > 100 {
				refs = refs[:100]
			}
			for _, ref := range refs {
				if !strings.HasPrefix(ref.Ref, prefix) || !reviewgate.ValidSHA(ref.Object.SHA) {
					return cursor, batch, errRead
				}
				c.Targets = append(c.Targets, checksTarget{Head: ref.Object.SHA})
			}
			c.Stage, c.Page, cursor.Phase = "pulls", 1, "pulls"
		case "pulls":
			var pulls []githubPullFact
			if err := counted(fmt.Sprintf("/pulls?state=all&sort=updated&direction=desc&per_page=100&page=%d", c.Page), &pulls); err != nil {
				return cursor, batch, err
			}
			if len(pulls) > 100 {
				return cursor, batch, errRead
			}
			older := false
			for _, p := range pulls {
				if p.Updated.IsZero() {
					return cursor, batch, errRead
				}
				if p.Updated.Before(cursor.Since) {
					older = true
					continue
				}
				if p.Base.Ref != cursor.DefaultBranch {
					continue
				}
				pull, err := metricPullFrom(p, repository)
				if err != nil || !reviewgate.ValidSHA(p.Head.SHA) {
					return cursor, batch, errRead
				}
				batch.Pulls = append(batch.Pulls, pull)
				c.Targets = append(c.Targets, checksTarget{Head: p.Head.SHA})
			}
			if older || len(pulls) < 100 || c.Page == backfillPullPageCap {
				c.Stage = "done"
			} else {
				c.Page++
			}
		case "done":
			cursor.Phase = "done"
			return cursor, batch, nil
		default:
			return cursor, batch, errRead
		}
	}
	return cursor, batch, nil
}
