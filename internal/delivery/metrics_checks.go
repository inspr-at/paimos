// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"gopkg.in/yaml.v3"
)

const historyActions = "actions"
const historyChecks = "checks"

// Negative run identities reserve a disjoint namespace for check suites. They
// are observations, never an assertion about an Actions run's first attempt.
func checksRun(r metricRun) bool { return r.ID < 0 }

// Exact Actions facts replace only their verified corresponding suite. A
// workflow may run on one SHA on many different nights; those stay distinct.
func preferredMetricRuns(runs []metricRun) []metricRun {
	type identity struct {
		suite          int64
		head, workflow string
	}
	exact := map[identity]bool{}
	key := func(r metricRun) identity {
		suite := r.Suite
		if checksRun(r) {
			suite = -r.ID
		}
		return identity{suite, r.Head, path.Base(r.Workflow)}
	}
	for _, r := range runs {
		if !checksRun(r) && r.Suite > 0 {
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
	Stage     string               `json:"stage"`
	Page      int                  `json:"page"`
	Anchor    string               `json:"anchor,omitempty"`
	Until     time.Time            `json:"until"`
	Targets   []checksTarget       `json:"targets,omitempty"`
	Suites    []metricCheckSuite   `json:"suites,omitempty"`
	Workflows *checksWorkflowPaths `json:"workflows,omitempty"`
}

type checksWorkflowPaths struct {
	Head        string `json:"head"`
	Tree        string `json:"tree,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	CI          string `json:"ci,omitempty"`
	Nightly     string `json:"nightly,omitempty"`
}

func (w checksWorkflowPaths) matches(s metricCheckSuite) bool {
	return w.Head == s.Head || reviewgate.ValidSHA(w.Tree) && w.Tree == s.HeadCommit.Tree
}

const (
	checksWorkflowLimit     = 20
	checksWorkflowByteLimit = 128 * 1024
)

// checksWorkflows resolves shared repository identities at a pinned commit,
// independently of any project's display/filter settings. Known aggregate job
// names are still an inference; multiple matching files withhold that mapping.
// Oversized/unsupported snapshots withhold both paths rather than guessing.
func checksWorkflows(get func(string, any) error, head string, previous *checksWorkflowPaths, readBudget int) (checksWorkflowPaths, bool, error) {
	out := checksWorkflowPaths{Head: head}
	if !reviewgate.ValidSHA(head) {
		return out, false, errRead
	}
	if readBudget < 1 {
		return out, true, nil
	}
	var files []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	if err := get("/contents/.github/workflows?ref="+head, &files); err != nil {
		if errors.Is(err, crossreview.ErrNotFound) {
			return out, false, nil
		}
		return out, false, err
	}
	if len(files) > checksWorkflowLimit {
		return out, false, nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	seen := map[string]bool{}
	snapshot := sha256.New()
	var workflows []string
	for _, file := range files {
		if !strings.HasSuffix(file.Path, ".yml") && !strings.HasSuffix(file.Path, ".yaml") {
			continue
		}
		if file.Type != "file" || !metricWorkflow.MatchString(file.Path) || !reviewgate.ValidSHA(file.SHA) || seen[file.Path] {
			return out, false, nil
		}
		seen[file.Path] = true
		fmt.Fprintf(snapshot, "%s\x00%s\n", file.Path, file.SHA)
		workflows = append(workflows, file.Path)
	}
	fingerprint := fmt.Sprintf("%x", snapshot.Sum(nil))
	if previous != nil && previous.Fingerprint == fingerprint {
		out.Fingerprint, out.CI, out.Nightly = fingerprint, previous.CI, previous.Nightly
		return out, false, nil
	}
	// When the definitions changed, resume before reading any file unless
	// every bounded file fits. Unchanged blobs only cost the directory read.
	if len(workflows) > readBudget-1 {
		return out, true, nil
	}
	ci, nightly := []string{}, []string{}
	for _, workflow := range workflows {
		var body struct {
			Path     string `json:"path"`
			Type     string `json:"type"`
			Encoding string `json:"encoding"`
			Size     int    `json:"size"`
			Content  string `json:"content"`
		}
		if err := get("/contents/"+workflow+"?ref="+head, &body); err != nil {
			return out, false, err
		}
		if body.Path != workflow || body.Type != "file" || body.Size < 0 {
			return out, false, errRead
		}
		if body.Size > checksWorkflowByteLimit || len(body.Content) > 2*checksWorkflowByteLimit || body.Encoding != "base64" {
			return out, false, nil
		}
		raw, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil || len(raw) != body.Size || len(raw) > checksWorkflowByteLimit {
			return out, false, errRead
		}
		// Inspect nodes without decoding aliases into expanded Go structures.
		var document yaml.Node
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return out, false, errRead
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return out, false, nil
		}
		field := func(node *yaml.Node, name string) *yaml.Node {
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Kind == yaml.ScalarNode && node.Content[i].Value == name {
					return node.Content[i+1]
				}
			}
			return nil
		}
		jobs := field(document.Content[0], "jobs")
		if jobs == nil || jobs.Kind != yaml.MappingNode || len(jobs.Content) > 1000 {
			return out, false, nil
		}
		names := map[string]bool{}
		for i := 0; i+1 < len(jobs.Content); i += 2 {
			id, job := jobs.Content[i], jobs.Content[i+1]
			if id.Kind != yaml.ScalarNode || job.Kind != yaml.MappingNode {
				return out, false, nil
			}
			name := id.Value
			if explicit := field(job, "name"); explicit != nil {
				if explicit.Kind != yaml.ScalarNode {
					return out, false, nil
				}
				name = explicit.Value
			}
			names[name] = true
		}
		known := func(prefix string) bool {
			for _, name := range defaultRequiredChecks {
				if !names[prefix+name] {
					return false
				}
			}
			return true
		}
		if known("") {
			ci = append(ci, workflow)
		}
		if known("nightly-") {
			nightly = append(nightly, workflow)
		}
	}
	if len(ci) == 1 && !(len(nightly) == 1 && ci[0] == nightly[0]) {
		out.CI = ci[0]
	}
	if len(nightly) == 1 && !(len(ci) == 1 && ci[0] == nightly[0]) {
		out.Nightly = nightly[0]
	}
	out.Fingerprint = fingerprint
	return out, false, nil
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
	if !metricWorkflow.MatchString(workflow) {
		// Repository definitions did not identify one unambiguous workflow.
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
	return []metricRun{{ID: -suite.ID, Suite: suite.ID, Attempt: 1, Workflow: workflow, Name: name, Event: event, Branch: branch, Head: suite.Head, HeadTree: suite.HeadCommit.Tree,
		PR: pr, Created: started, Started: started, Completed: completed, Conclusion: conclusion}}, false, nil
}

// checksBackfillStep retains the same durable cursor and 40-read limit as the
// Actions reader. Pending heads/suites are bounded pages, committed with their
// facts. The default branch is pinned by SHA so new pushes cannot shift pages.
func checksBackfillStep(get func(string, any) error, repository, _, _ string, cursor backfillCursor, now time.Time) (backfillCursor, backfillBatch, error) {
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
	if c.Stage == "main" && c.Anchor == "" {
		var commit struct {
			SHA    string `json:"sha"`
			Commit struct {
				Tree struct {
					SHA string `json:"sha"`
				} `json:"tree"`
			} `json:"commit"`
		}
		// Scheduled workflows run on the default head even when that commit
		// predates the history window. Suite timestamps, not commit dates,
		// decide whether those observations belong in the window.
		if err := counted("/commits/"+url.PathEscape(cursor.DefaultBranch), &commit); err != nil {
			return cursor, batch, err
		}
		if !reviewgate.ValidSHA(commit.SHA) || !reviewgate.ValidSHA(commit.Commit.Tree.SHA) {
			return cursor, batch, errRead
		}
		c.Anchor = commit.SHA
		c.Targets = append(c.Targets, checksTarget{Head: commit.SHA, Tree: commit.Commit.Tree.SHA})
	}
	for batch.Calls < backfillCallBudget {
		if len(c.Suites) > 0 {
			// A suite may use five pages; never exceed the step budget.
			if batch.Calls > backfillCallBudget-jobPageLimit {
				return cursor, batch, nil
			}
			suite := c.Suites[0]
			if suite.Status == "completed" && (c.Workflows == nil || !c.Workflows.matches(suite)) {
				// Reserve the directory and check pages first. A changed file
				// snapshot resumes if all its files cannot fit in this step.
				if batch.Calls > backfillCallBudget-1-jobPageLimit {
					return cursor, batch, nil
				}
				workflows, pending, err := checksWorkflows(counted, suite.Head, c.Workflows, backfillCallBudget-batch.Calls-jobPageLimit)
				if err != nil {
					return cursor, batch, err
				}
				if pending {
					return cursor, batch, nil
				}
				workflows.Tree = suite.HeadCommit.Tree
				c.Workflows = &workflows
			}
			ci, nightly := "", ""
			if c.Workflows != nil && c.Workflows.matches(suite) {
				ci, nightly = c.Workflows.CI, c.Workflows.Nightly
			}
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
				if commit.SHA == c.Anchor {
					continue
				}
				c.Targets = append(c.Targets, checksTarget{Head: commit.SHA, Tree: commit.Commit.Tree.SHA})
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
