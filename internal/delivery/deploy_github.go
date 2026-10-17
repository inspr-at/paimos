// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

type deploymentReader interface {
	DeploymentRead(context.Context, string, string, func(func(string, any) error) error) error
}

// Borrow only the existing tenant/repository-scoped installation authority.
// Other project repositories use public reads, never that installation's token.
func (g AppReader) DeploymentRead(ctx context.Context, tenantID, repository string, read func(func(string, any) error) error) error {
	if g.App != nil && g.App.Configured(tenantID, repository) {
		return g.readInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
			return read(get)
		})
	}
	return publicDeploymentRead(ctx, repository, read)
}

func publicDeploymentRead(ctx context.Context, repository string, read func(func(string, any) error) error) error {
	if !reviewgate.ValidRepository(repository) {
		return errRead
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return read(func(path string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+path, nil)
		if err != nil {
			return errRead
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		res, err := client.Do(req)
		if err != nil {
			return errRead
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return crossreview.ErrNotFound
		}
		if res.StatusCode != http.StatusOK {
			return errRead
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
		if err != nil || len(raw) > 4<<20 || json.Unmarshal(raw, out) != nil {
			return errRead
		}
		return nil
	})
}

// Compare immutable object identities rather than commit dates or a truncated
// commit list. The comparison's merge base must equal the candidate merge.
func deploymentAncestors(get func(string, any) error, repository, sha string, merges []string) (map[string]bool, error) {
	var repo struct {
		Name string `json:"full_name"`
	}
	if err := get("", &repo); err != nil {
		return nil, err
	}
	if !strings.EqualFold(repo.Name, repository) {
		return nil, errRead
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := get("/git/commits/"+sha, &commit); err != nil {
		if errors.Is(err, crossreview.ErrNotFound) {
			return nil, fail(422, "deployed SHA is unknown to the project repository")
		}
		return nil, err
	}
	if commit.SHA != sha {
		return nil, errRead
	}
	out := make(map[string]bool, len(merges))
	for _, merge := range merges {
		if !reviewgate.ValidSHA(merge) {
			return nil, errRead
		}
		if merge == sha {
			out[merge] = true
			continue
		}
		var comparison struct {
			Status string `json:"status"`
			Base   struct {
				SHA string `json:"sha"`
			} `json:"base_commit"`
			MergeBase struct {
				SHA string `json:"sha"`
			} `json:"merge_base_commit"`
		}
		if err := get("/compare/"+merge+"..."+sha+"?per_page=1", &comparison); err != nil {
			return nil, err
		}
		if comparison.Base.SHA != merge || !reviewgate.ValidSHA(comparison.MergeBase.SHA) {
			return nil, errRead
		}
		switch comparison.Status {
		case "ahead", "identical":
			if comparison.MergeBase.SHA != merge {
				return nil, errRead
			}
			out[merge] = true
		case "behind", "diverged":
			out[merge] = false
		default:
			return nil, errRead
		}
	}
	return out, nil
}
