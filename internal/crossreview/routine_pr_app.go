// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/routineactions/evidence"
)

type AppRoutinePullRequests struct{ App *GitHubApp }

func (a AppRoutinePullRequests) Configured(tid, repo string) bool {
	return a.App != nil && a.App.Configured(tid, repo)
}

type appPull struct {
	GitHubPull
	Body string `json:"body"`
	Head struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

func operationMarker(ref string) string { return "<!-- aeon-routine-operation:" + ref + " -->" }
func appResult(p appPull, ref string) *evidence.Pull {
	if !strings.HasSuffix(p.Body, operationMarker(ref)) || p.Head.Repo.FullName != p.Base.Repo.FullName {
		return nil
	}
	return &evidence.Pull{Number: p.Number, Repository: p.Base.Repo.FullName, Branch: p.Head.Ref, BaseBranch: p.Base.Ref, BaseSHA: p.Base.SHA, HeadSHA: p.Head.SHA, OperationRef: ref, Open: p.State == "open"}
}
func findAppPull(get func(string, any) error, in evidence.PROpen, ref string) (*evidence.Pull, error) {
	owner, _, _ := strings.Cut(in.Repository, "/")
	var found *evidence.Pull
	for page := 1; page <= 20; page++ {
		var pulls []appPull
		path := "/pulls?state=all&head=" + url.QueryEscape(owner+":"+in.Branch) + "&base=" + url.QueryEscape(in.BaseBranch) + "&per_page=100&page=" + strconv.Itoa(page)
		if err := get(path, &pulls); err != nil {
			return nil, err
		}
		if len(pulls) > 100 {
			return nil, ErrUnavailable
		}
		for _, p := range pulls {
			if v := appResult(p, ref); v != nil {
				if found != nil || !v.Matches(in, ref) {
					return nil, ErrUnavailable
				}
				found = v
			} else if p.State == "open" {
				return nil, ErrUnavailable
			}
		}
		if len(pulls) < 100 {
			return found, nil
		}
	}
	return nil, ErrUnavailable
}
func (a AppRoutinePullRequests) Find(ctx context.Context, in evidence.PROpen, ref string) (*evidence.Pull, error) {
	if a.App == nil {
		return nil, ErrUnavailable
	}
	var out *evidence.Pull
	err := a.App.RoutinePullInstallation(ctx, in.Repository, false, func(get func(string, any) error, _ func(any, any) error) error {
		var err error
		out, err = findAppPull(get, in, ref)
		return err
	})
	return out, err
}
func (a AppRoutinePullRequests) Open(ctx context.Context, in evidence.PROpen, ref string) (*evidence.Pull, error) {
	if a.App == nil || !in.Valid() {
		return nil, ErrUnavailable
	}
	var out *evidence.Pull
	err := a.App.RoutinePullInstallation(ctx, in.Repository, true, func(get func(string, any) error, post func(any, any) error) error {
		var err error
		if out, err = findAppPull(get, in, ref); err != nil || out != nil {
			return err
		}
		for branch, sha := range map[string]string{in.Branch: in.HeadSHA, in.BaseBranch: in.BaseSHA} {
			var v struct {
				Ref    string `json:"ref"`
				Object struct {
					SHA  string `json:"sha"`
					Type string `json:"type"`
				} `json:"object"`
			}
			if err := get("/git/ref/heads/"+url.PathEscape(branch), &v); err != nil {
				return err
			}
			if v.Ref != "refs/heads/"+branch || v.Object.Type != "commit" || v.Object.SHA != sha {
				return ErrUnavailable
			}
		}
		var p appPull
		if err := post(map[string]any{"title": in.Title, "body": in.Body + "\n\n" + operationMarker(ref), "head": in.Branch, "base": in.BaseBranch, "draft": false}, &p); err != nil {
			return err
		}
		out = appResult(p, ref)
		if out == nil || !out.Matches(in, ref) {
			return ErrUnavailable
		}
		return nil
	})
	return out, err
}
