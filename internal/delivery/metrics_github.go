// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
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
	HeadCommit struct {
		Tree string `json:"tree_id"`
	} `json:"head_commit"`
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
	if reviewgate.ValidSHA(r.HeadCommit.Tree) {
		out.HeadTree = r.HeadCommit.Tree
	}
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

// Job facts (AEON-1016). The Delivery numbers need two things from the jobs of
// attempt 1 of a pull-request or merge-queue run: whether the required checks
// passed, and how long the worst job waited for a runner. Nothing else of a job
// is kept: no step, log, runner name or label.
const (
	jobPageSize  = 100
	jobPageLimit = 5
)

type githubJob struct {
	Name       string     `json:"name"`
	Conclusion *string    `json:"conclusion"`
	Created    time.Time  `json:"created_at"`
	Started    *time.Time `json:"started_at"`
}

// jobNameLimit bounds one job name stored in the conclusion map. A longer
// name, or a conclusion longer than 32 characters, fails the read: a partial
// map is not stored.
const jobNameLimit = 200

type jobFacts struct {
	Conclusions map[string]string
	WorstWaitMS *int
	Complete    bool
}

// storedJobFacts is the required_jobs JSON written for a complete read.
// Older rows are a flat name→conclusion map and are not complete.
type storedJobFacts struct {
	Complete    bool              `json:"complete"`
	Conclusions map[string]string `json:"conclusions"`
}

func conclusionRank(conclusion string) int {
	switch conclusion {
	case "failure", "timed_out", "startup_failure", "action_required", "cancelled", "missing":
		return 3
	case "success":
		return 1
	case "skipped", "neutral":
		return 0
	default:
		return 2
	}
}

// worseConclusion keeps the conclusion that hides a failure least. A failed
// matrix leg must not disappear behind an earlier success of the same name.
func worseConclusion(current, next string) string {
	if conclusionRank(next) > conclusionRank(current) {
		return next
	}
	return current
}

func encodeStoredJobs(facts jobFacts) ([]byte, error) {
	if !facts.Complete {
		return nil, errRead
	}
	conclusions := facts.Conclusions
	if conclusions == nil {
		conclusions = map[string]string{}
	}
	return json.Marshal(storedJobFacts{Complete: true, Conclusions: conclusions})
}

func decodeJobFacts(raw []byte) (map[string]string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	var wrapped storedJobFacts
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, false, err
	}
	if wrapped.Complete && wrapped.Conclusions != nil {
		return wrapped.Conclusions, true, nil
	}
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err != nil {
		// A wrapper that is not a complete map is not a legacy flat map either.
		if wrapped.Complete || wrapped.Conclusions != nil {
			return nil, false, nil
		}
		return nil, false, err
	}
	return flat, false, nil
}

// jobRunEvents are the events whose runs the numbers read jobs for.
func jobRunEvent(event string) bool { return event == "pull_request" || event == "merge_group" }

// readJobFacts reads every job of one attempt, up to a bound, and keeps each
// job's conclusion. A partial list is a read error: a fact is stored whole or
// not at all. The map is complete, so any project's required check is either
// in it or truly absent. A job with no conclusion is "missing". Two jobs with
// the same name keep the worse conclusion, so a failed matrix leg is not
// hidden by an earlier success. A skipped job has no runner wait.
func readJobFacts(get func(string, any) error, runID int64, attempt int) (jobFacts, error) {
	var jobs []githubJob
	for page := 1; ; page++ {
		var response struct {
			Total int         `json:"total_count"`
			Jobs  []githubJob `json:"jobs"`
		}
		if err := get(fmt.Sprintf("/actions/runs/%d/attempts/%d/jobs?per_page=%d&page=%d", runID, attempt, jobPageSize, page), &response); err != nil {
			return jobFacts{}, err
		}
		if response.Total < 0 || response.Total > jobPageSize*jobPageLimit || len(response.Jobs) > jobPageSize {
			return jobFacts{}, errRead
		}
		jobs = append(jobs, response.Jobs...)
		if len(jobs) >= response.Total {
			break
		}
		if page >= jobPageLimit || len(response.Jobs) == 0 {
			return jobFacts{}, errRead
		}
	}
	facts := jobFacts{Conclusions: map[string]string{}, Complete: true}
	for _, job := range jobs {
		if job.Name == "" || len(job.Name) > jobNameLimit {
			return jobFacts{}, errRead
		}
		conclusion := "missing"
		if job.Conclusion != nil && *job.Conclusion != "" {
			if len(*job.Conclusion) > 32 {
				return jobFacts{}, errRead
			}
			conclusion = *job.Conclusion
		}
		if old, ok := facts.Conclusions[job.Name]; ok {
			conclusion = worseConclusion(old, conclusion)
		}
		facts.Conclusions[job.Name] = conclusion
		if job.Started == nil || job.Started.IsZero() || job.Created.IsZero() || conclusion == "skipped" {
			continue
		}
		wait := int(max(0, job.Started.Sub(job.Created).Milliseconds()))
		if facts.WorstWaitMS == nil || wait > *facts.WorstWaitMS {
			facts.WorstWaitMS = &wait
		}
	}
	return facts, nil
}

// attachJobFacts reads the jobs of each eligible attempt-1 run into the run. A
// failed read leaves that run without job facts: its numbers are then missing
// and the windows holding it partial, never guessed. eligible decides whether
// the run is one of the CI workflow's.
func attachJobFacts(get func(string, any) error, runs []metricRun, eligible func(metricRun) bool) {
	for i := range runs {
		run := &runs[i]
		if run.Attempt != 1 || !jobRunEvent(run.Event) || !eligible(*run) {
			continue
		}
		facts, err := readJobFacts(get, run.ID, run.Attempt)
		if err != nil {
			continue
		}
		run.JobsRead, run.JobsComplete, run.Required, run.WorstWaitMS = true, facts.Complete, facts.Conclusions, facts.WorstWaitMS
	}
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
	// upserted again on resume. DoneRuns records the highest attempt already
	// stored for each run on this page. A higher attempt in a later listing
	// is read before the page advances. A cursor stored before that record
	// is a list of run ids; those runs are read again.
	AttemptRun int64    `json:"attempt_run,omitempty"`
	Attempt    int      `json:"attempt,omitempty"`
	DoneRuns   doneRuns `json:"done_runs,omitempty"`
	// Workflows is the list this backfill is reading. Empty on a cursor that
	// started before preflight was part of the list: an in-progress runs or
	// pulls phase keeps the old CI and nightly list, and a finished one is
	// caught up once. ResumePhase, when set, is where a one-workflow catch-up
	// returns (jobs or done) instead of entering pulls. ThenWorkflows is the
	// full list stored when that catch-up finishes.
	Workflows     []string `json:"workflows,omitempty"`
	ResumePhase   string   `json:"resume_phase,omitempty"`
	ThenWorkflows []string `json:"then_workflows,omitempty"`
}

// doneRun is one run on the current page and the highest attempt stored for
// it. Attempt 0 is a cursor written when only the run id was stored.
type doneRun struct {
	ID      int64 `json:"id"`
	Attempt int   `json:"attempt"`
}

type doneRuns []doneRun

func (d *doneRuns) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		*d = nil
		return nil
	}
	if len(raw) == 0 || raw[0] != '[' {
		return errRead
	}
	body := bytes.TrimSpace(raw[1:])
	if len(body) == 0 {
		return errRead
	}
	if body[0] == ']' {
		*d = doneRuns{}
		return nil
	}
	if body[0] == '{' {
		var rows []doneRun
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		*d = rows
		return nil
	}
	var ids []int64
	if err := json.Unmarshal(raw, &ids); err != nil {
		return err
	}
	rows := make(doneRuns, len(ids))
	for i, id := range ids {
		rows[i] = doneRun{ID: id}
	}
	*d = rows
	return nil
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
// and the next attempt, and the attempt already stored for each finished run
// on that page, so a retry repeats only the unread attempts. Each visit
// upserts the listed attempt, so a newer attempt that landed between steps is
// kept, including one for a run this page already finished. That gap waits
// until a paused run on the same page is stored, and a pause keeps that run's
// identity. A failed step leaves the stored cursor alone. Pulls into other
// base branches are not facts. Hitting the last pull page while recent pulls
// remain is truncated, not done.
func backfillStep(get func(string, any) error, repository string, workflows []string, cursor backfillCursor, now time.Time) (backfillCursor, backfillBatch, error) {
	batch := backfillBatch{}
	today := dayStart(now)
	for batch.Calls < backfillCallBudget {
		switch cursor.Phase {
		case "runs":
			if cursor.Workflow >= len(workflows) {
				if cursor.ResumePhase != "" {
					// A preflight catch-up returns to jobs or done. It must
					// not walk pulls again.
					return backfillCursor{Since: cursor.Since, Phase: cursor.ResumePhase, Truncated: cursor.Truncated, Workflows: cursor.ThenWorkflows}, batch, nil
				}
				cursor = backfillCursor{Since: cursor.Since, Phase: "pulls", Page: 1, Truncated: cursor.Truncated, Workflows: cursor.Workflows}
				continue
			}
			day, err := time.Parse("2006-01-02", cursor.Day)
			if err != nil {
				return cursor, batch, errRead
			}
			if day.After(today) {
				cursor = backfillCursor{Since: cursor.Since, Phase: "runs", Workflow: cursor.Workflow + 1, Day: dayStart(cursor.Since).Format("2006-01-02"), Page: 1, Truncated: cursor.Truncated, Workflows: cursor.Workflows, ResumePhase: cursor.ResumePhase, ThenWorkflows: cursor.ThenWorkflows}
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
			var pending []githubRun
			for _, r := range response.Runs {
				stored, seen := doneAttempt(cursor.DoneRuns, r.ID)
				if seen && r.Attempt <= stored {
					if r.ID == cursor.AttemptRun {
						foundResume = true
					}
					continue
				}
				// A paused run still owns the only resume slot. Read this
				// newer attempt after that run is stored, in this step when
				// the budget allows, without dropping the pause.
				if seen && cursor.AttemptRun != 0 && cursor.AttemptRun != r.ID {
					pending = append(pending, r)
					continue
				}
				wasPaused := cursor.AttemptRun == r.ID
				var stop bool
				cursor, stop, err = absorbListedRun(get, cursor, &batch, r)
				if err != nil {
					return cursor, batch, err
				}
				if stop {
					return cursor, batch, nil
				}
				if wasPaused {
					foundResume = true
				}
			}
			if !foundResume {
				return cursor, batch, errRead
			}
			for _, r := range pending {
				var stop bool
				cursor, stop, err = absorbListedRun(get, cursor, &batch, r)
				if err != nil {
					return cursor, batch, err
				}
				if stop {
					return cursor, batch, nil
				}
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
				cursor = backfillCursor{Since: cursor.Since, Phase: "done", Truncated: cursor.Truncated, Workflows: cursor.Workflows}
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

func doneAttempt(runs doneRuns, id int64) (int, bool) {
	for _, run := range runs {
		if run.ID == id {
			return run.Attempt, true
		}
	}
	return 0, false
}

func rememberDone(runs doneRuns, id int64, attempt int) doneRuns {
	for i := range runs {
		if runs[i].ID == id {
			if attempt > runs[i].Attempt {
				runs[i].Attempt = attempt
			}
			return runs
		}
	}
	return append(runs, doneRun{ID: id, Attempt: attempt})
}

// absorbListedRun stores attempts of one listed run that this page has not
// stored yet. A stored attempt reads only the gap up to the listing. stop
// means the call budget paused the read; the cursor then names that run and
// the next attempt. The listed attempt is upserted on every visit.
func absorbListedRun(get func(string, any) error, cursor backfillCursor, batch *backfillBatch, r githubRun) (backfillCursor, bool, error) {
	stored, seen := doneAttempt(cursor.DoneRuns, r.ID)
	from := 1
	if seen && stored >= 1 {
		from = stored + 1
	}
	if cursor.AttemptRun == r.ID && cursor.Attempt > from {
		from = cursor.Attempt
	}
	allow := func() bool {
		if batch.Calls >= backfillCallBudget {
			return false
		}
		batch.Calls++
		return true
	}
	runs, next, err := readRunAttempts(get, r, from, true, allow)
	if err != nil {
		return cursor, false, err
	}
	batch.Runs = append(batch.Runs, runs...)
	if next != 0 {
		cursor.AttemptRun, cursor.Attempt = r.ID, next
		return cursor, true, nil
	}
	cursor.DoneRuns = rememberDone(cursor.DoneRuns, r.ID, r.Attempt)
	if r.ID == cursor.AttemptRun {
		cursor.AttemptRun, cursor.Attempt = 0, 0
	}
	return cursor, false, nil
}
