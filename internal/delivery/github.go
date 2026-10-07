// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

type Pull struct {
	QueueHead                 string
	Number                    int64
	Title, Branch, Head, Base string
	Open, Merged, Queued      bool
	Checks                    []Check
}

// GitHub is deliberately read-only. Partial inventories are errors, never green.
type GitHub interface {
	Pull(context.Context, int64) (Pull, error)
	OpenPulls(context.Context) ([]Pull, error)
	Group(context.Context, string, string, string) ([]Pull, error)
}
type AppReader struct{ App *crossreview.GitHubApp }

var errRead = errors.New("delivery GitHub observation unavailable")

func readPull(p crossreview.GitHubPull, repo string) (Pull, error) {
	if p.Number < 1 || !reviewgate.ValidSHA(p.Head.SHA) || !reviewgate.ValidSHA(p.Base.SHA) || p.Base.Repo.FullName != repo || len(p.Title) > 4096 || len(p.Head.Ref) > 255 {
		return Pull{}, errRead
	}
	return Pull{Number: p.Number, Title: p.Title, Branch: p.Head.Ref, Head: p.Head.SHA, Base: p.Base.SHA, Open: p.State == "open", Merged: p.Merged}, nil
}
func checks(get func(string, any) error, head string) ([]Check, error) {
	// Filter latest attempts. Every configured context must independently succeed.
	out := []Check{}
	seen := map[string]bool{}
	for page := 1; page <= 20; page++ {
		var resp struct {
			Total int                       `json:"total_count"`
			Runs  []crossreview.GitHubCheck `json:"check_runs"`
		}
		if err := get("/commits/"+head+"/check-runs?filter=latest&per_page=100&page="+strconv.Itoa(page), &resp); err != nil {
			return nil, err
		}
		if resp.Total > 2000 {
			return nil, errRead
		}
		for _, c := range resp.Runs {
			if len(c.Name) > 200 || c.Name == "" {
				return nil, errRead
			}
			// Duplicate providers/context names are ambiguous and cannot prove success.
			if seen[c.Name] {
				for i := range out {
					if out[i].Name == c.Name {
						out[i].Conclusion = "ambiguous"
					}
				}
				continue
			}
			seen[c.Name] = true
			out = append(out, Check{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion})
		}
		if len(resp.Runs) < 100 {
			break
		}
		if page == 20 {
			return nil, errRead
		}
	}
	// Status contexts are a distinct namespace; check runs take precedence.
	statusSeen := map[string]bool{}
	for page := 1; page <= 20; page++ {
		var resp []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		}
		if err := get("/commits/"+head+"/statuses?per_page=100&page="+strconv.Itoa(page), &resp); err != nil {
			return nil, err
		}
		for _, c := range resp {
			if len(c.Context) > 200 || c.Context == "" {
				return nil, errRead
			}
			if seen[c.Context] || statusSeen[c.Context] {
				continue
			}
			statusSeen[c.Context] = true
			out = append(out, Check{Name: c.Context, Status: "completed", Conclusion: c.State})
		}
		if len(resp) < 100 {
			return out, nil
		}
		if page == 20 {
			return nil, errRead
		}
	}
	return out, nil
}
func (g AppReader) Pull(ctx context.Context, n int64) (Pull, error) {
	var out Pull
	if n <= 0 {
		return out, errRead
	}
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, queue func(int64, string) (bool, string, error)) error {
		var p crossreview.GitHubPull
		if err := get("/pulls/"+strconv.FormatInt(n, 10), &p); err != nil {
			return err
		}
		var err error
		out, err = readPull(p, g.App.Config.Repository)
		if err != nil {
			return err
		}
		if out.Number != n {
			return errRead
		}
		if out.Open {
			out.Checks, err = checks(get, out.Head)
			if err == nil {
				out.Queued, out.QueueHead, err = queue(out.Number, out.Head)
			}
		}
		return err
	})
	return out, err
}
func openPulls(get func(string, any) error, repo string) ([]Pull, error) {
	out := []Pull{}
	for page := 1; page <= 20; page++ {
		var ps []crossreview.GitHubPull
		if err := get(fmt.Sprintf("/pulls?state=open&per_page=100&page=%d", page), &ps); err != nil {
			return nil, err
		}
		for _, p := range ps {
			v, err := readPull(p, repo)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if len(ps) < 100 {
			return out, nil
		}
	}
	return nil, errRead
}
func (g AppReader) OpenPulls(ctx context.Context) ([]Pull, error) {
	var out []Pull
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, queue func(int64, string) (bool, string, error)) error {
		var err error
		out, err = openPulls(get, g.App.Config.Repository)
		if err != nil {
			return err
		}
		for i := range out {
			out[i].Checks, err = checks(get, out[i].Head)
			if err == nil {
				out[i].Queued, out[i].QueueHead, err = queue(out[i].Number, out[i].Head)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
func (g AppReader) Group(ctx context.Context, head, base, ref string) ([]Pull, error) {
	if !reviewgate.ValidSHA(head) || !reviewgate.ValidSHA(base) {
		return nil, errRead
	}
	var out []Pull
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, queue func(int64, string) (bool, string, error)) error {
		var live struct {
			Ref    string `json:"ref"`
			Object struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			} `json:"object"`
		}
		if !queueRef.MatchString(ref) || strings.Contains(ref, "..") {
			return errRead
		}
		if err := get("/git/ref/"+url.PathEscape(strings.TrimPrefix(ref, "refs/")), &live); err != nil {
			return err
		}
		if live.Ref != ref || live.Object.SHA != head || live.Object.Type != "commit" {
			return errRead
		}
		ps, err := openPulls(get, g.App.Config.Repository)
		if err != nil {
			return err
		}
		shas := map[string]bool{}
		for page := 1; page <= 20; page++ {
			var diff struct {
				Total   int    `json:"total_commits"`
				Status  string `json:"status"`
				Commits []struct {
					SHA     string `json:"sha"`
					Parents []struct {
						SHA string `json:"sha"`
					} `json:"parents"`
				} `json:"commits"`
			}
			if err = get("/compare/"+url.PathEscape(base+"..."+head)+"?per_page=100&page="+strconv.Itoa(page), &diff); err != nil {
				return err
			}
			if diff.Total > 2000 || diff.Status != "ahead" {
				return errRead
			}
			for _, c := range diff.Commits {
				shas[c.SHA] = true
				for _, p := range c.Parents {
					shas[p.SHA] = true
				}
			}
			if len(diff.Commits) < 100 {
				break
			}
			if page == 20 {
				return errRead
			}
		}
		for _, p := range ps {
			if p.Base != base || !shas[p.Head] {
				continue
			}
			out = append(out, p)
		}
		if len(out) == 0 {
			return errRead
		}
		return nil
	})
	return out, err
}
func keyFromTitle(title string) string {
	prefix, _, ok := strings.Cut(title, ":")
	if !ok {
		return ""
	}
	return normalizeKey(prefix)
}
func keyFromBranch(branch string) string {
	branch = strings.TrimPrefix(branch, "work/")
	parts := strings.SplitN(branch, "-", 3)
	if len(parts) < 2 {
		return ""
	}
	return normalizeKey(parts[0] + "-" + parts[1])
}
func normalizeKey(key string) string {
	if len(key) > 64 {
		return ""
	}
	parts := strings.Split(key, "-")
	if len(parts) != 2 || len(parts[0]) < 1 {
		return ""
	}
	for _, c := range parts[0] {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return ""
		}
	}
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	return strings.ToUpper(parts[0]) + "-" + parts[1]
}
