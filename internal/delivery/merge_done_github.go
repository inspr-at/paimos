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

// Completion needs the canonical state behind an empty fact. Keep the public
// fact-only reader compatible with backfill and routine consumers.
type mergeStateReader interface {
	mergedPullState(context.Context, int64) (*MergeFact, bool, error)
	mergedGroupState(context.Context, string, bool) ([]MergeFact, bool, error)
}

func readMergedPull(ctx context.Context, reader MergeReader, number int64) (*MergeFact, bool, error) {
	if reader, ok := reader.(mergeStateReader); ok {
		return reader.mergedPullState(ctx, number)
	}
	fact, err := reader.MergedPull(ctx, number)
	// A fact-only reader cannot prove that an empty pull is terminal.
	return fact, fact == nil, err
}

func readMergedGroup(ctx context.Context, reader MergeReader, sha string, waitForProof bool) ([]MergeFact, bool, error) {
	if reader, ok := reader.(mergeStateReader); ok {
		return reader.mergedGroupState(ctx, sha, waitForProof)
	}
	facts, err := reader.MergedGroup(ctx, sha)
	return facts, waitForProof && len(facts) == 0, err
}

func mergedPull(get func(string, any) error, repo string, number int64) (*MergeFact, error) {
	fact, _, err := mergedPullState(get, repo, number)
	return fact, err
}

func mergedPullState(get func(string, any) error, repo string, number int64) (*MergeFact, bool, error) {
	var p auditPull
	if err := get("/pulls/"+strconv.FormatInt(number, 10), &p); err != nil {
		return nil, false, err
	}
	if p.Number != number {
		return nil, false, errRead
	}
	fact, err := auditPullFact(p, repo, "main")
	// A closed, unmerged pull or a different merge target will never produce a
	// main-branch fact. Open pulls can still merge after queue destruction.
	terminal := !p.Merged && p.State == "closed" || p.Base.Ref != "" && p.Base.Ref != "main" || p.Base.Repo.Name != "" && p.Base.Repo.Name != repo
	pending := fact == nil && !terminal
	return fact, pending, err
}

func (g AppReader) MergedPull(ctx context.Context, number int64) (*MergeFact, error) {
	fact, _, err := g.mergedPullState(ctx, number)
	return fact, err
}

func (g AppReader) mergedPullState(ctx context.Context, number int64) (*MergeFact, bool, error) {
	if number < 1 {
		return nil, false, errRead
	}
	var fact *MergeFact
	var pending bool
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		var err error
		fact, pending, err = mergedPullState(get, g.App.Config.Repository, number)
		return err
	})
	return fact, pending, err
}

func (g AppReader) MergedGroup(ctx context.Context, sha string) ([]MergeFact, error) {
	facts, _, err := g.mergedGroupState(ctx, sha, false)
	return facts, err
}

func (g AppReader) mergedGroupState(ctx context.Context, sha string, waitForProof bool) ([]MergeFact, bool, error) {
	if !reviewgate.ValidSHA(sha) {
		return nil, false, errRead
	}
	out := []MergeFact{}
	pending := false
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
		// With no retained queue ledger, an empty association can still be
		// delayed proof. A push without any associated pull is a direct push.
		pending = waitForProof && len(pulls) == 0
		seen := map[int64]bool{}
		for _, p := range pulls {
			if p.Number < 1 || seen[p.Number] {
				return errRead
			}
			seen[p.Number] = true
			f, waiting, err := mergedPullState(get, g.App.Config.Repository, p.Number)
			if err != nil {
				return err
			}
			pending = pending || waiting
			// Canonical main-branch merge proof is required for every constituent;
			// a multi-PR queue group can have distinct per-PR merge commits.
			if f != nil {
				out = append(out, *f)
			}
		}
		return nil
	})
	return out, pending, err
}
