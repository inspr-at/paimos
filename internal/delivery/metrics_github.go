// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

// metricsReader runs bounded reads with the App's existing short-lived
// installation token, the same read channel the queue quarantine uses for
// workflow runs (Actions reads of a private repository need the App's
// actions permission, as QueueChecks notes). No new credential exists.
type metricsReader interface {
	MetricsRead(context.Context, func(get func(string, any) error) error) error
}

func (g AppReader) MetricsRead(ctx context.Context, read func(func(string, any) error) error) error {
	return g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		return read(get)
	})
}

var metricWorkflow = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9._-]{1,100}\.ya?ml$`)

type githubRun struct {
	ID         int64      `json:"id"`
	Attempt    int        `json:"run_attempt"`
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Event      string     `json:"event"`
	Branch     string     `json:"head_branch"`
	Head       string     `json:"head_sha"`
	Status     string     `json:"status"`
	Conclusion *string    `json:"conclusion"`
	Created    time.Time  `json:"created_at"`
	Started    *time.Time `json:"run_started_at"`
	Updated    time.Time  `json:"updated_at"`
	Suite      int64      `json:"check_suite_id"`
	Pulls      []struct {
		Number int64 `json:"number"`
	} `json:"pull_requests"`
}

// metricRunFrom validates one workflow run attempt. An incomplete run is not
// a fact yet (ok=false, no error); a malformed one is a read error.
func metricRunFrom(r githubRun) (metricRun, bool, error) {
	if r.Status != "completed" {
		return metricRun{}, false, nil
	}
	if r.ID <= 0 || r.Attempt < 1 || r.Attempt > 1000 || r.Name == "" || len(r.Name) > 200 || len(r.Path) > 255 || r.Event == "" || len(r.Event) > 64 || len(r.Branch) > 255 ||
		!reviewgate.ValidSHA(r.Head) || r.Conclusion == nil || *r.Conclusion == "" || len(*r.Conclusion) > 32 || r.Created.IsZero() || r.Updated.IsZero() || len(r.Pulls) > 100 {
		return metricRun{}, false, errRead
	}
	started := r.Created
	if r.Started != nil && !r.Started.IsZero() {
		started = *r.Started
	}
	out := metricRun{ID: r.ID, Attempt: r.Attempt, Workflow: r.Path, Name: r.Name, Event: r.Event, Branch: r.Branch, Head: r.Head, Created: r.Created.UTC(), Started: started.UTC(), Completed: r.Updated.UTC(), Conclusion: *r.Conclusion}
	if len(r.Pulls) > 0 && r.Pulls[0].Number > 0 {
		n := r.Pulls[0].Number
		out.PR = &n
	} else if match := queueBranch.FindStringSubmatch(r.Branch); match != nil {
		if n, err := strconv.ParseInt(match[1], 10, 64); err == nil {
			out.PR = &n
		}
	}
	return out, true, nil
}

// suiteRuns resolves a completed GitHub Actions check suite to its workflow
// run, plus the run's first attempt when the suite reports a re-run: the
// Arion first-attempt numbers need attempt 1 even if its own event was lost.
func suiteRuns(get func(string, any) error, suite int64, head string) ([]metricRun, error) {
	if suite <= 0 || !reviewgate.ValidSHA(head) {
		return nil, errRead
	}
	var response struct {
		Total int         `json:"total_count"`
		Runs  []githubRun `json:"workflow_runs"`
	}
	if err := get(fmt.Sprintf("/actions/runs?check_suite_id=%d&head_sha=%s&per_page=2", suite, url.QueryEscape(head)), &response); err != nil {
		return nil, err
	}
	if response.Total != 1 || len(response.Runs) != 1 || response.Runs[0].Suite != suite || response.Runs[0].Head != head {
		return nil, errRead
	}
	return runWithFirstAttempt(get, response.Runs[0])
}

func runWithFirstAttempt(get func(string, any) error, r githubRun) ([]metricRun, error) {
	run, ok, err := metricRunFrom(r)
	if err != nil || !ok {
		return nil, err
	}
	out := []metricRun{run}
	if run.Attempt > 1 {
		var first githubRun
		if err := get(fmt.Sprintf("/actions/runs/%d/attempts/1", run.ID), &first); err != nil {
			return nil, err
		}
		if first.ID != run.ID || first.Attempt != 1 {
			return nil, errRead
		}
		attempt, ok, err := metricRunFrom(first)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, attempt)
		}
	}
	return out, nil
}

type githubPullFact struct {
	Number  int64      `json:"number"`
	Created time.Time  `json:"created_at"`
	Updated time.Time  `json:"updated_at"`
	Merged  *time.Time `json:"merged_at"`
	Closed  *time.Time `json:"closed_at"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			Name string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

func metricPullFrom(p githubPullFact, repository string) (metricPull, error) {
	if p.Number < 1 || p.Created.IsZero() || len(p.Head.Ref) > 255 || p.Base.Repo.Name != repository {
		return metricPull{}, errRead
	}
	out := metricPull{Number: p.Number, Branch: p.Head.Ref, Opened: p.Created.UTC()}
	if p.Merged != nil && !p.Merged.IsZero() {
		v := p.Merged.UTC()
		out.Merged = &v
	}
	if p.Closed != nil && !p.Closed.IsZero() {
		v := p.Closed.UTC()
		out.Closed = &v
	}
	return out, nil
}

// backfillCursor is the resumable position of a backfill. Runs are read per
// workflow and UTC day (GitHub caps one filtered listing at 1 000 results),
// then pull requests by last update until one is older than Since.
type backfillCursor struct {
	Since    time.Time `json:"since"`
	Phase    string    `json:"phase"`
	Workflow int       `json:"workflow"`
	Day      string    `json:"day"`
	Page     int       `json:"page"`
	// Truncated records a day with more runs than GitHub lists (1 000).
	Truncated bool `json:"truncated,omitempty"`
}

type backfillBatch struct {
	Runs  []metricRun
	Pulls []metricPull
	Calls int
}

const (
	backfillCallBudget = 40
	backfillMaxPages   = 10
)

// backfillStep reads from the cursor until the call budget is spent or the
// backfill is done. Each listing page is consumed whole, so the returned
// cursor never skips facts; a failed step leaves the stored cursor alone and
// a retry repeats the same pages idempotently.
func backfillStep(get func(string, any) error, repository string, workflows []string, cursor backfillCursor, now time.Time) (backfillCursor, backfillBatch, error) {
	batch := backfillBatch{}
	today := dayStart(now)
	for batch.Calls < backfillCallBudget {
		switch cursor.Phase {
		case "runs":
			if cursor.Workflow >= len(workflows) {
				cursor = backfillCursor{Since: cursor.Since, Phase: "pulls", Page: 1, Truncated: cursor.Truncated}
				continue
			}
			day, err := time.Parse("2006-01-02", cursor.Day)
			if err != nil {
				return cursor, batch, errRead
			}
			if day.After(today) {
				cursor = backfillCursor{Since: cursor.Since, Phase: "runs", Workflow: cursor.Workflow + 1, Day: dayStart(cursor.Since).Format("2006-01-02"), Page: 1, Truncated: cursor.Truncated}
				continue
			}
			var response struct {
				Total int         `json:"total_count"`
				Runs  []githubRun `json:"workflow_runs"`
			}
			batch.Calls++
			file := url.PathEscape(path.Base(workflows[cursor.Workflow]))
			if err := get(fmt.Sprintf("/actions/workflows/%s/runs?created=%s&status=completed&per_page=100&page=%d", file, cursor.Day, cursor.Page), &response); err != nil {
				return cursor, batch, err
			}
			if response.Total < 0 || len(response.Runs) > 100 {
				return cursor, batch, errRead
			}
			if response.Total > 100*backfillMaxPages {
				cursor.Truncated = true
			}
			for _, r := range response.Runs {
				runs, err := runWithFirstAttempt(get, r)
				if err != nil {
					return cursor, batch, err
				}
				if len(runs) > 1 {
					batch.Calls++
				}
				batch.Runs = append(batch.Runs, runs...)
			}
			if len(response.Runs) < 100 || cursor.Page >= backfillMaxPages {
				cursor.Day, cursor.Page = day.AddDate(0, 0, 1).Format("2006-01-02"), 1
			} else {
				cursor.Page++
			}
		case "pulls":
			var pulls []githubPullFact
			batch.Calls++
			if err := get(fmt.Sprintf("/pulls?state=all&sort=updated&direction=desc&per_page=100&page=%d", cursor.Page), &pulls); err != nil {
				return cursor, batch, err
			}
			if len(pulls) > 100 {
				return cursor, batch, errRead
			}
			older := false
			for _, p := range pulls {
				pull, err := metricPullFrom(p, repository)
				if err != nil {
					return cursor, batch, err
				}
				if p.Updated.Before(cursor.Since) {
					older = true
					continue
				}
				batch.Pulls = append(batch.Pulls, pull)
			}
			if older || len(pulls) < 100 || cursor.Page >= 100 {
				cursor = backfillCursor{Since: cursor.Since, Phase: "done", Truncated: cursor.Truncated}
				return cursor, batch, nil
			}
			cursor.Page++
		case "done":
			return cursor, batch, nil
		default:
			return cursor, batch, errRead
		}
	}
	return cursor, batch, nil
}
