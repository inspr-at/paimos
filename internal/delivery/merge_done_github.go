// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"strconv"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

// MergeReader uses the existing pull_requests:read installation authority.
// A green queue run, destroyed group or closed PR is never itself merge proof.
type MergeReader interface {
	MergedPull(context.Context, int64) (*MergeFact, error)
	MergedGroup(context.Context, string) ([]MergeFact, error)
}

func mergedPull(get func(string, any) error, repo string, number int64) (*MergeFact, error) {
	var p auditPull
	if err := get("/pulls/"+strconv.FormatInt(number, 10), &p); err != nil {
		return nil, err
	}
	if p.Number != number {
		return nil, errRead
	}
	return auditPullFact(p, repo, "main")
}

func (g AppReader) MergedPull(ctx context.Context, number int64) (*MergeFact, error) {
	if number < 1 {
		return nil, errRead
	}
	var fact *MergeFact
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		var err error
		fact, err = mergedPull(get, g.App.Config.Repository, number)
		return err
	})
	return fact, err
}

func (g AppReader) MergedGroup(ctx context.Context, sha string) ([]MergeFact, error) {
	if !reviewgate.ValidSHA(sha) {
		return nil, errRead
	}
	out := []MergeFact{}
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		var pulls []struct {
			Number int64 `json:"number"`
		}
		if err := get("/commits/"+sha+"/pulls?per_page=100", &pulls); err != nil {
			return err
		}
		if len(pulls) >= 100 {
			return errRead
		}
		seen := map[int64]bool{}
		for _, p := range pulls {
			if p.Number < 1 || seen[p.Number] {
				return errRead
			}
			seen[p.Number] = true
			f, err := mergedPull(get, g.App.Config.Repository, p.Number)
			if err != nil {
				return err
			}
			// Canonical main-branch merge proof is required for every constituent;
			// a multi-PR queue group can have distinct per-PR merge commits.
			if f != nil {
				out = append(out, *f)
			}
		}
		return nil
	})
	return out, err
}
