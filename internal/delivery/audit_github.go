// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

// AuditGitHub adds read-only commit facts without widening the delivery reader
// interface used by the other Wave 1 slices.
type AuditGitHub interface {
	AuditCommits(context.Context) ([]string, error)
	AuditCommit(context.Context, string) (*MergeFact, error)
	AuditChecks(context.Context, string) ([]Check, error)
}

type MergeFact struct {
	SHA, Head, Title, Branch, MergedBy string
	PR                                 *int64
	At                                 time.Time
}

type auditPull struct {
	Number   int64      `json:"number"`
	Merged   bool       `json:"merged"`
	MergeSHA string     `json:"merge_commit_sha"`
	MergedAt *time.Time `json:"merged_at"`
	MergedBy struct {
		Login string `json:"login"`
	} `json:"merged_by"`
	Title string `json:"title"`
	Head  struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			Name string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

func auditPullFact(p auditPull, repo, branch string) (*MergeFact, error) {
	if !p.Merged || p.Base.Ref != branch || p.Base.Repo.Name != repo {
		return nil, nil
	}
	if p.Number < 1 || !reviewgate.ValidSHA(p.MergeSHA) || !reviewgate.ValidSHA(p.Head.SHA) || p.MergedAt == nil || p.MergedAt.IsZero() || p.MergedBy.Login == "" || len(p.MergedBy.Login) > 100 || len(p.Title) > 4096 || len(p.Head.Ref) > 255 {
		return nil, errRead
	}
	n := p.Number
	return &MergeFact{SHA: p.MergeSHA, Head: p.Head.SHA, Title: p.Title, Branch: p.Head.Ref, MergedBy: p.MergedBy.Login, PR: &n, At: *p.MergedAt}, nil
}

func auditDefault(get func(string, any) error, repo string) (string, error) {
	var r struct {
		Name    string `json:"full_name"`
		Default string `json:"default_branch"`
	}
	if err := get("", &r); err != nil {
		return "", err
	}
	if r.Name != repo || r.Default == "" || len(r.Default) > 255 {
		return "", errRead
	}
	return r.Default, nil
}

func (g AppReader) AuditCommits(ctx context.Context) ([]string, error) {
	out := []string{}
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		branch, err := auditDefault(get, g.App.Config.Repository)
		if err != nil {
			return err
		}
		var commits []struct {
			SHA     string `json:"sha"`
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
		}
		if err = get("/commits?sha="+url.QueryEscape(branch)+"&per_page=100", &commits); err != nil {
			return err
		}
		if len(commits) > 100 {
			return errRead
		}
		// Follow first parents only. Commits from a merged PR's side branch
		// are not additional direct pushes to the default branch.
		bySHA := map[string]int{}
		for i, c := range commits {
			if !reviewgate.ValidSHA(c.SHA) {
				return errRead
			}
			if _, ok := bySHA[c.SHA]; ok {
				return errRead
			}
			bySHA[c.SHA] = i
		}
		if len(commits) == 0 {
			return nil
		}
		sha := commits[0].SHA
		seen := map[string]bool{}
		for len(out) < 100 {
			i, ok := bySHA[sha]
			if !ok {
				break
			}
			if seen[sha] {
				return errRead
			}
			seen[sha] = true
			out = append(out, sha)
			if len(commits[i].Parents) == 0 {
				break
			}
			sha = commits[i].Parents[0].SHA
			if !reviewgate.ValidSHA(sha) {
				return errRead
			}
		}
		return nil
	})
	return out, err
}

func (g AppReader) AuditCommit(ctx context.Context, sha string) (*MergeFact, error) {
	if !reviewgate.ValidSHA(sha) {
		return nil, errRead
	}
	var out *MergeFact
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		branch, err := auditDefault(get, g.App.Config.Repository)
		if err != nil {
			return err
		}
		var c struct {
			SHA       string `json:"sha"`
			Committer *struct {
				Login string `json:"login"`
			} `json:"committer"`
			Commit struct {
				Committer struct {
					Date time.Time `json:"date"`
				} `json:"committer"`
			} `json:"commit"`
		}
		if err = get("/commits/"+sha, &c); err != nil {
			return err
		}
		if c.SHA != sha || c.Commit.Committer.Date.IsZero() {
			return errRead
		}
		var pulls []auditPull
		if err = get("/commits/"+sha+"/pulls?per_page=100", &pulls); err != nil {
			return err
		}
		if len(pulls) >= 100 {
			return errRead
		}
		constituent := false
		for _, p := range pulls {
			// The association endpoint's list document is not guaranteed to
			// include merged_by; read the canonical pull document.
			if p.Number <= 0 {
				return errRead
			}
			var full auditPull
			if err = get("/pulls/"+strconv.FormatInt(p.Number, 10), &full); err != nil {
				return err
			}
			if full.Number != p.Number {
				return errRead
			}
			f, err := auditPullFact(full, g.App.Config.Repository, branch)
			if err != nil {
				return err
			}
			if f == nil {
				continue
			}
			if f.SHA != sha {
				constituent = true
				continue
			}
			if out != nil {
				return errRead
			}
			out = f
		}
		if out != nil || constituent {
			return nil
		}
		login := ""
		if c.Committer != nil {
			login = c.Committer.Login
		}
		if len(login) > 100 {
			return errRead
		}
		// An unmapped committer is left unknown, never guessed from the
		// untrusted commit author name or message.
		out = &MergeFact{SHA: sha, Head: sha, MergedBy: login, At: c.Commit.Committer.Date}
		return nil
	})
	return out, err
}

func (g AppReader) AuditChecks(ctx context.Context, sha string) ([]Check, error) {
	if !reviewgate.ValidSHA(sha) {
		return nil, errRead
	}
	var out []Check
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		var err error
		out, err = checks(get, sha)
		return err
	})
	return out, err
}
