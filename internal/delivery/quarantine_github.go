// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// QueueChecks resolves workflow identity from the suite's GitHub Actions run,
// never a check name, output title or webhook-supplied workflow field. The App
// reader needs Actions read access for private repositories (coordinator-owned
// token permissions). All requests still use the existing bounded read channel.
func (g AppReader) QueueChecks(ctx context.Context, suite queueSuite, run *queueRun) (string, []queueRun, error) {
	var workflow string
	var out []queueRun
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		var err error
		workflow, out, err = readQueueChecks(get, suite, run)
		return err
	})
	return workflow, out, err
}

func readQueueChecks(get func(string, any) error, suite queueSuite, run *queueRun) (string, []queueRun, error) {
	var workflow string
	var out []queueRun
	err := func() error {
		var response struct {
			Total int `json:"total_count"`
			Runs  []struct {
				Name   string `json:"name"`
				Suite  int64  `json:"check_suite_id"`
				Head   string `json:"head_sha"`
				Branch string `json:"head_branch"`
				Event  string `json:"event"`
			} `json:"workflow_runs"`
		}
		if err := get(fmt.Sprintf("/actions/runs?check_suite_id=%d&head_sha=%s&per_page=2", suite.ID, url.QueryEscape(suite.Head)), &response); err != nil {
			return err
		}
		if response.Total != 1 || len(response.Runs) != 1 {
			return errRead
		}
		w := response.Runs[0]
		if w.Suite != suite.ID || w.Head != suite.Head || w.Branch != suite.Branch || w.Event != "merge_group" || w.Name == "" || len(w.Name) > 200 {
			return errRead
		}
		workflow = w.Name
		if run != nil {
			if run.ID <= 0 || run.Head != suite.Head || run.Suite.ID != suite.ID || run.App.Slug != "github-actions" {
				return errRead
			}
			out = []queueRun{*run}
			return nil
		}
		seen := map[int64]bool{}
		for page := 1; page <= 20; page++ {
			var checks struct {
				Total int        `json:"total_count"`
				Runs  []queueRun `json:"check_runs"`
			}
			if err := get(fmt.Sprintf("/check-suites/%d/check-runs?filter=all&per_page=100&page=%s", suite.ID, strconv.Itoa(page)), &checks); err != nil {
				return err
			}
			if checks.Total < 0 || checks.Total > 2000 || len(checks.Runs) > 100 {
				return errRead
			}
			for _, c := range checks.Runs {
				if c.ID <= 0 || seen[c.ID] || c.Head != suite.Head || c.Suite.ID != suite.ID || c.App.Slug != "github-actions" || c.Name == "" || len(c.Name) > 200 {
					return errRead
				}
				seen[c.ID] = true
				out = append(out, c)
			}
			if len(checks.Runs) < 100 {
				if len(out) != checks.Total {
					return errRead
				}
				return nil
			}
		}
		return errRead
	}()
	return workflow, out, err
}
