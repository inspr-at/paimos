// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

// metricsReader runs bounded reads with the App's existing short-lived
// installation token. tenantID is the authenticated tenant (or the App's
// configured tenant for a webhook it already accepted). Reads of a private
// repository's workflow runs need actions:read, which the shipping
// installation token already requests. No new credential exists.
type metricsReader interface {
	MetricsRead(context.Context, string, func(get func(string, any) error) error) error
}

func (g AppReader) MetricsRead(ctx context.Context, tenantID string, read func(func(string, any) error) error) error {
	if !g.App.Configured(tenantID, g.App.Config.Repository) {
		return errRead
	}
	return g.App.ReadShippingInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
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
// run and every earlier attempt. The Arion first-attempt, flake and queue
// numbers need an intermediate success even when the suite event is the
// latest attempt.
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
	runs, next, err := readRunAttempts(get, r, 1, true, func() bool { return true })
	if err != nil {
		return nil, err
	}
	if next != 0 {
		return nil, errRead
	}
	return runs, nil
}

// readRunAttempts returns attempts of one completed run. from is the next
// attempt number still to fetch (1 reads every earlier attempt). The latest
// attempt is r itself and is included only when includeLatest is set.
// Backfill always includes it: the row is an idempotent upsert, and a newer
// attempt may have completed since the cursor was stored. allow gates each
// fetch; a false allow returns the attempt number to resume from.
func readRunAttempts(get func(string, any) error, r githubRun, from int, includeLatest bool, allow func() bool) ([]metricRun, int, error) {
	latest, ok, err := metricRunFrom(r)
	if err != nil || !ok {
		return nil, 0, err
	}
	if from < 1 {
		from = 1
	}
	var out []metricRun
	if includeLatest {
		out = append(out, latest)
	}
	for n := from; n < latest.Attempt; n++ {
		if !allow() {
			return out, n, nil
		}
		var body githubRun
		if err := get(fmt.Sprintf("/actions/runs/%d/attempts/%d", latest.ID, n), &body); err != nil {
			return nil, 0, err
		}
		if body.ID != latest.ID || body.Attempt != n {
			return nil, 0, errRead
		}
		attempt, ok, err := metricRunFrom(body)
		if err != nil {
			return nil, 0, err
		}
		if ok {
			out = append(out, attempt)
		}
	}
	return out, 0, nil
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
			Name    string `json:"full_name"`
			Default string `json:"default_branch"`
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
	// Truncated records a day with more runs than GitHub lists (1 000), or a
	// pull-request listing that hit GitHub's last page while recent pulls remain.
	Truncated bool `json:"truncated,omitempty"`
	// DefaultBranch is the repository default branch, read once when pulls
	// begin. Backfill keeps the same pulls the webhook does: base == default.
	DefaultBranch string `json:"default_branch,omitempty"`
	// AttemptRun is the listed run whose earlier attempts are still unread.
	// Attempt is the next attempt number to fetch. The listed attempt is
	// upserted again on resume. DoneRuns are runs on this page whose
	// attempts are already stored.
	AttemptRun int64   `json:"attempt_run,omitempty"`
	Attempt    int     `json:"attempt,omitempty"`
	DoneRuns   []int64 `json:"done_runs,omitempty"`
}

type backfillBatch struct {
	Runs  []metricRun
	Pulls []metricPull
	Calls int
}

const (
	backfillCallBudget  = 40
	backfillMaxPages    = 10
	backfillPullPageCap = 100
)

// backfillStep reads from the cursor until the call budget is spent or the
// backfill is done. A page of runs can span steps: the cursor keeps the run
// and the next attempt, and runs already stored on that page, so a retry
// repeats only the unread attempts. Each visit upserts the listed attempt, so
// a newer attempt that landed between steps is kept. Finishing another run
// on the page leaves the paused run's identity in place until that run is
// read. A failed step leaves the stored cursor alone. Pulls into other base
// branches are not facts. Hitting the last pull page while recent pulls
// remain is truncated, not done.
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
			if len(cursor.DoneRuns) > 100 {
				return cursor, batch, errRead
			}
			foundResume := cursor.AttemptRun == 0
			for _, r := range response.Runs {
				if containsRun(cursor.DoneRuns, r.ID) {
					if r.ID == cursor.AttemptRun {
						foundResume = true
					}
					continue
				}
				from := 1
				if cursor.AttemptRun == r.ID && cursor.Attempt > 0 {
					from = cursor.Attempt
					foundResume = true
				}
				allow := func() bool {
					if batch.Calls >= backfillCallBudget {
						return false
					}
					batch.Calls++
					return true
				}
				// The listed attempt is upserted on every visit, including a
				// resume. Suppressing it would drop an attempt that completed
				// between steps and still report the backfill as complete.
				runs, next, err := readRunAttempts(get, r, from, true, allow)
				if err != nil {
					return cursor, batch, err
				}
				batch.Runs = append(batch.Runs, runs...)
				if next != 0 {
					cursor.AttemptRun, cursor.Attempt = r.ID, next
					return cursor, batch, nil
				}
				cursor.DoneRuns = append(cursor.DoneRuns, r.ID)
				// A different run that finished on this page must not clear the
				// paused run. Losing that identity makes the step fail, and the
				// stored cursor then retries the same page forever.
				if r.ID == cursor.AttemptRun {
					foundResume = true
					cursor.AttemptRun, cursor.Attempt = 0, 0
				}
			}
			if !foundResume {
				return cursor, batch, errRead
			}
			cursor.AttemptRun, cursor.Attempt, cursor.DoneRuns = 0, 0, nil
			if len(response.Runs) < 100 || cursor.Page >= backfillMaxPages {
				cursor.Day, cursor.Page = day.AddDate(0, 0, 1).Format("2006-01-02"), 1
			} else {
				cursor.Page++
			}
		case "pulls":
			if cursor.DefaultBranch == "" {
				var repo struct {
					FullName string `json:"full_name"`
					Default  string `json:"default_branch"`
				}
				batch.Calls++
				if err := get("", &repo); err != nil {
					return cursor, batch, err
				}
				if repo.FullName != repository || !metricDefaultBranch(repo.Default) {
					return cursor, batch, errRead
				}
				cursor.DefaultBranch = repo.Default
				if batch.Calls >= backfillCallBudget {
					return cursor, batch, nil
				}
			}
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
				if p.Updated.Before(cursor.Since) {
					older = true
					continue
				}
				// Same population as webhook ingestion: only pulls into the default branch.
				if p.Base.Ref != cursor.DefaultBranch {
					continue
				}
				pull, err := metricPullFrom(p, repository)
				if err != nil {
					return cursor, batch, err
				}
				batch.Pulls = append(batch.Pulls, pull)
			}
			capped := !older && len(pulls) == 100 && cursor.Page >= backfillPullPageCap
			if older || len(pulls) < 100 || capped {
				if capped {
					cursor.Truncated = true
				}
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

func metricDefaultBranch(name string) bool {
	return queueBase.MatchString(name) && !strings.Contains(name, "..") && !strings.HasSuffix(name, "/")
}

func containsRun(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
