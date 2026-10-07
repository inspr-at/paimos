// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

func shipRef(get func(string, any) error, branch string) (string, error) {
	var ref struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := get("/git/ref/heads/"+url.PathEscape(branch), &ref); err != nil {
		return "", err
	}
	if ref.Ref != "refs/heads/"+branch || ref.Object.Type != "commit" || !reviewgate.ValidSHA(ref.Object.SHA) {
		return "", errRead
	}
	return ref.Object.SHA, nil
}

func (g AppReader) Shipping(ctx context.Context, branch, head string) (ShipFacts, error) {
	out := emptyShipFacts()
	if !strings.HasPrefix(branch, "work/") || !queueSlug.MatchString(strings.TrimPrefix(branch, "work/")) || !reviewgate.ValidSHA(head) {
		return out, errRead
	}
	err := g.App.ReadShippingInstallation(ctx, func(get func(string, any) error, queue func(int64, string) (bool, string, error)) error {
		var repo struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		}
		if err := get("", &repo); err != nil {
			return err
		}
		if repo.FullName != g.App.Config.Repository || !queueBase.MatchString(repo.DefaultBranch) || strings.Contains(repo.DefaultBranch, "..") || strings.HasSuffix(repo.DefaultBranch, "/") {
			return errRead
		}
		var err error
		if out.Base, err = shipRef(get, repo.DefaultBranch); err != nil {
			return err
		}
		out.PushedHead, err = shipRef(get, branch)
		if errors.Is(err, crossreview.ErrNotFound) {
			out.PushedHead = ""
		} else if err != nil {
			return err
		}
		// Query only the bound repository branch, including merged/closed PRs.
		// Multiple open PRs are ambiguous and cannot prove safe admission.
		owner, _, _ := strings.Cut(repo.FullName, "/")
		var open *crossreview.GitHubPull
		var latest *crossreview.GitHubPull
		for page := 1; page <= 10; page++ {
			var pulls []crossreview.GitHubPull
			if err = get("/pulls?state=all&head="+url.QueryEscape(owner+":"+branch)+"&sort=created&direction=desc&per_page=100&page="+strconv.Itoa(page), &pulls); err != nil {
				return err
			}
			for _, pull := range pulls {
				if _, err = readPull(pull, repo.FullName); err != nil {
					return err
				}
				if pull.Head.Ref != branch {
					return errRead
				}
				if latest == nil {
					copy := pull
					latest = &copy
				}
				if pull.State == "open" {
					if open != nil {
						return errRead
					}
					copy := pull
					open = &copy
				}
			}
			if len(pulls) < 100 {
				break
			}
			if page == 10 {
				return errRead
			}
		}
		selected := open
		if selected == nil {
			selected = latest
		}
		if selected != nil {
			var pull struct {
				crossreview.GitHubPull
				Draft      bool   `json:"draft"`
				Mergeable  *bool  `json:"mergeable"`
				MergeState string `json:"mergeable_state"`
			}
			if err = get("/pulls/"+strconv.FormatInt(selected.Number, 10), &pull); err != nil {
				return err
			}
			if _, err = readPull(pull.GitHubPull, repo.FullName); err != nil {
				return err
			}
			if pull.Number != selected.Number || pull.Head.Ref != branch || pull.Base.Ref != repo.DefaultBranch {
				return errRead
			}
			out.PR = &pull.Number
			out.PullHead = pull.Head.SHA
			out.Open = pull.State == "open"
			out.Merged = pull.Merged
			out.Draft = pull.Draft
			out.MergeableKnown = pull.Mergeable != nil && pull.MergeState != "unknown"
			out.Conflict = pull.MergeState == "dirty" || pull.Mergeable != nil && !*pull.Mergeable
			if out.Open {
				out.Checks, err = checks(get, out.PullHead)
				if err != nil {
					return err
				}
				out.Queued, out.QueueHead, err = queue(pull.Number, out.PullHead)
				if err != nil {
					return err
				}
			}
		}
		if out.PushedHead == head {
			var compare struct {
				Behind int    `json:"behind_by"`
				Ahead  int    `json:"ahead_by"`
				Status string `json:"status"`
			}
			if err = get("/compare/"+url.PathEscape(out.Base+"..."+head)+"?per_page=1", &compare); err != nil {
				return err
			}
			if compare.Behind < 0 || compare.Ahead < 0 || !strings.Contains("|ahead|behind|diverged|identical|", "|"+compare.Status+"|") {
				return errRead
			}
			out.Behind = compare.Behind > 0
			if out.Open && out.PullHead == head {
				out.Runs, err = shipRuns(get, head)
				if err != nil {
					return err
				}
				if out.Queued && reviewgate.ValidSHA(out.QueueHead) {
					groupRuns, err := shipRuns(get, out.QueueHead)
					if err != nil {
						return err
					}
					out.Runs = append(out.Runs, groupRuns...)
					if len(out.Runs) > 20 {
						return errRead
					}
				}
			}
		}
		// Reject mixed observations when the push or default branch moved during
		// the read. A later move remains a fresh claim; shadow never executes it.
		base, err := shipRef(get, repo.DefaultBranch)
		if err != nil || base != out.Base {
			return errRead
		}
		pushed, err := shipRef(get, branch)
		if errors.Is(err, crossreview.ErrNotFound) && out.PushedHead == "" {
			return nil
		}
		if err != nil || pushed != out.PushedHead {
			return errRead
		}
		return nil
	})
	if err != nil {
		return emptyShipFacts(), err
	}
	return out, nil
}

func shipRuns(get func(string, any) error, head string) ([]ShipRun, error) {
	type workflowRun struct {
		ID         int64  `json:"id"`
		WorkflowID int64  `json:"workflow_id"`
		Attempt    int    `json:"run_attempt"`
		Head       string `json:"head_sha"`
		Name       string `json:"name"`
		Event      string `json:"event"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	latest := map[string]workflowRun{}
	for page := 1; page <= 10; page++ {
		var response struct {
			Total int           `json:"total_count"`
			Runs  []workflowRun `json:"workflow_runs"`
		}
		if err := get(fmt.Sprintf("/actions/runs?head_sha=%s&per_page=100&page=%d", head, page), &response); err != nil {
			return nil, err
		}
		if response.Total > 1000 || response.Total < 0 {
			return nil, errRead
		}
		for _, run := range response.Runs {
			if run.ID <= 0 || run.WorkflowID <= 0 || run.Attempt < 1 || run.Head != head || run.Name == "" || len(run.Name) > 200 {
				return nil, errRead
			}
			if run.Event != "pull_request" && run.Event != "push" && run.Event != "merge_group" {
				continue
			}
			key := fmt.Sprintf("%d/%s", run.WorkflowID, run.Event)
			if old, ok := latest[key]; !ok || old.ID < run.ID {
				latest[key] = run
			}
		}
		if len(response.Runs) < 100 {
			break
		}
		if page == 10 {
			return nil, errRead
		}
	}
	if len(latest) > 20 {
		return nil, errRead
	}
	out := []ShipRun{}
	for _, run := range latest {
		item := ShipRun{ID: run.ID, Attempt: run.Attempt, Head: run.Head, Workflow: run.Name, Event: run.Event, Status: run.Status, Conclusion: run.Conclusion, Jobs: []Check{}}
		for page := 1; page <= 10; page++ {
			var response struct {
				Total int `json:"total_count"`
				Jobs  []struct {
					ID         int64  `json:"id"`
					Head       string `json:"head_sha"`
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
					Run        int64  `json:"run_id"`
					Attempt    int    `json:"run_attempt"`
				} `json:"jobs"`
			}
			if err := get(fmt.Sprintf("/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", run.ID, run.Attempt, page), &response); err != nil {
				return nil, err
			}
			if response.Total > 1000 || response.Total < 0 {
				return nil, errRead
			}
			for _, job := range response.Jobs {
				if job.ID <= 0 || job.Run != run.ID || job.Attempt != run.Attempt || job.Head != head || job.Name == "" || len(job.Name) > 200 {
					return nil, errRead
				}
				item.Jobs = append(item.Jobs, Check{Name: job.Name, Status: job.Status, Conclusion: job.Conclusion})
			}
			if len(response.Jobs) < 100 {
				break
			}
			if page == 10 {
				return nil, errRead
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}
