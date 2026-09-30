// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

type pullRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}
type pull struct {
	Number         int     `json:"number"`
	State          string  `json:"state"`
	Draft          bool    `json:"draft"`
	Merged         bool    `json:"merged"`
	MergeCommit    string  `json:"merge_commit_sha"`
	MergeableState string  `json:"mergeable_state"`
	Head           pullRef `json:"head"`
	Base           pullRef `json:"base"`
}

func repoPath(repo string) string     { return "/repos/" + repo }
func proposalBranch(id string) string { return "aeon/proposals/" + id }
func (g *GitHub) pr(ctx context.Context, p Proposal) (pull, error) {
	var v pull
	err := g.request(ctx, "GET", fmt.Sprintf("%s/pulls/%d", repoPath(p.Repository), p.PRNumber), nil, &v)
	return v, err
}

func validPull(p Proposal, pr pull) bool {
	return pr.Number == p.PRNumber && pr.Base.Repo.FullName == p.Repository && pr.Head.Repo.FullName == p.Repository && pr.Base.Ref == "main" && pr.Head.Ref == proposalBranch(p.ID) && pr.Head.SHA == p.HeadSHA
}

func (g *GitHub) main(ctx context.Context, repo string) (string, error) {
	var branch struct {
		Protected bool `json:"protected"`
		Commit    struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.request(ctx, "GET", repoPath(repo)+"/branches/main", nil, &branch); err != nil {
		return "", err
	}
	if !branch.Protected || !shaPattern.MatchString(branch.Commit.SHA) {
		return "", fail(409, "unprotected_base", "Doctrine proposals require a protected main branch.")
	}
	return branch.Commit.SHA, nil
}

// preparePR is retryable: a deterministic branch and a commit timestamp from
// the persisted reservation recover a dropped response without another PR.
func (g *GitHub) preparePR(ctx context.Context, p *Proposal, files map[string]string, explanation string) error {
	if g.botName == "" || g.botEmail == "" {
		return gitFail("the App contribution identity is missing")
	}
	base := repoPath(p.Repository)
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.request(ctx, "GET", base+"/git/commits/"+p.BaseCommit, nil, &commit); err != nil {
		return err
	}
	if !shaPattern.MatchString(commit.Tree.SHA) {
		return gitFail("the base tree is invalid")
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	entries := []map[string]string{}
	for _, path := range paths {
		entries = append(entries, map[string]string{"path": path, "mode": "100644", "type": "blob", "content": files[path]})
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	if err := g.request(ctx, "POST", base+"/git/trees", map[string]any{"base_tree": commit.Tree.SHA, "tree": entries}, &tree); err != nil {
		return err
	}
	if !shaPattern.MatchString(tree.SHA) {
		return gitFail("the proposal tree is invalid")
	}
	var head struct {
		SHA string `json:"sha"`
	}
	// No tenant/person identifiers or credentials in public commit metadata.
	identity := map[string]string{"name": g.botName, "email": g.botEmail, "date": p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")}
	message := "Propose doctrine rule change\n\nSigned-off-by: " + g.botName + " <" + g.botEmail + ">"
	if err := g.request(ctx, "POST", base+"/git/commits", map[string]any{"message": message, "tree": tree.SHA, "parents": []string{p.BaseCommit}, "author": identity, "committer": identity}, &head); err != nil {
		return err
	}
	if !shaPattern.MatchString(head.SHA) {
		return gitFail("the proposal commit is invalid")
	}
	p.HeadSHA = head.SHA
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	branch := proposalBranch(p.ID)
	if err := g.authorizeWrite(ctx); err != nil {
		return err
	}
	err := g.request(ctx, "POST", base+"/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": head.SHA}, &ref)
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) || ae.status != 422 {
			return err
		}
		if err = g.request(ctx, "GET", base+"/git/ref/heads/"+branch, nil, &ref); err != nil {
			return err
		}
	}
	if ref.Object.SHA != head.SHA {
		return fail(409, "branch_changed", "The proposal branch changed outside Aeon; inspect it in git.")
	}
	p.Branch = branch
	var prs []pull
	if err := g.request(ctx, "GET", base+"/pulls?state=all&head="+url.QueryEscape("inspr-at:"+branch)+"&per_page=100", nil, &prs); err != nil {
		return err
	}
	var pr pull
	if len(prs) > 1 {
		return fail(409, "multiple_prs", "More than one PR uses this proposal branch.")
	}
	if len(prs) == 1 {
		pr = prs[0]
	} else {
		if err := g.authorizeWrite(ctx); err != nil {
			p.Orphaned = true
			p.GateReason = "This proposal's branch exists on GitHub without a pull request because authority was revoked during the ref write. An admin should delete the branch."
			return err
		}
		body := "## Proposed change\n\n" + explanation + "\n\nReview the rule and TL;DR diff. Merge requires a person in Aeon and the repository gates."
		if err := g.request(ctx, "POST", base+"/pulls", map[string]any{"title": "Propose doctrine rule change", "head": branch, "base": "main", "body": body}, &pr); err != nil {
			return err
		}
	}
	p.PRNumber = pr.Number
	if pr.Number < 1 || !validPull(*p, pr) {
		return gitFail("the PR does not match this proposal")
	}
	p.Orphaned = false
	p.GateReason = ""
	p.PRURL = fmt.Sprintf("https://github.com/%s/pull/%d", p.Repository, p.PRNumber)
	return nil
}

type gateReview struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	Body     string `json:"body"`
	CommitID string `json:"commit_id"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
}

// Gate evidence is authored by an independently configured repository gate
// account, never accepted from the approval request or a PR author assertion.
type gateEvidence struct {
	Verdict        string `json:"verdict"`
	Head           string `json:"head_sha"`
	Checks         bool   `json:"checks_green"`
	AuthorFamily   string `json:"author_family"`
	ReviewerFamily string `json:"reviewer_family"`
}

const gateMarker = "aeon-doctrine-gate: "

func (g *GitHub) gate(ctx context.Context, p Proposal, pr pull, login string) (int64, string, error) {
	if !validPull(p, pr) {
		return 0, "The PR head or target changed; this approval is stale.", nil
	}
	if pr.Merged || pr.State != "open" || pr.Draft {
		return 0, "The PR must be open and ready for review.", nil
	}
	if _, err := g.main(ctx, p.Repository); err != nil {
		return 0, "", err
	}
	if pr.MergeableState != "clean" {
		return 0, "Waiting for required checks and protected-branch mergeability.", nil
	}
	reviews := []gateReview{}
	for page := 1; page <= 10; page++ {
		var chunk []gateReview
		if err := g.request(ctx, "GET", fmt.Sprintf("%s/pulls/%d/reviews?per_page=100&page=%d", repoPath(p.Repository), p.PRNumber, page), nil, &chunk); err != nil {
			return 0, "", err
		}
		reviews = append(reviews, chunk...)
		if len(chunk) < 100 {
			break
		}
		if page == 10 {
			return 0, "Too many reviews to verify safely.", nil
		}
	}
	// A later comment by the gate does not replace an approval, but a dismissal
	// or requested changes does. Any reviewer's outstanding changes close it.
	latest := map[string]gateReview{}
	for _, r := range reviews {
		if r.State != "COMMENTED" && r.State != "PENDING" {
			if prev, ok := latest[r.User.Login]; !ok || r.ID > prev.ID {
				latest[r.User.Login] = r
			}
		}
	}
	for _, r := range latest {
		if r.State == "CHANGES_REQUESTED" {
			return 0, "A reviewer requested changes.", nil
		}
	}
	r, ok := latest[login]
	if !ok || r.State != "APPROVED" || r.CommitID != p.HeadSHA || !strings.HasPrefix(r.Body, gateMarker) {
		return 0, "Waiting for the independent cross-family review on this commit.", nil
	}
	var e gateEvidence
	if json.Unmarshal([]byte(strings.TrimPrefix(r.Body, gateMarker)), &e) != nil || e.Verdict != "ok" || e.Head != p.HeadSHA || !e.Checks || !reviewFamily(e.AuthorFamily) || !reviewFamily(e.ReviewerFamily) || e.AuthorFamily == e.ReviewerFamily {
		return 0, "The independent gate has not attested green checks and an explicit cross-family ok.", nil
	}
	return r.ID, "", nil
}
func reviewFamily(s string) bool {
	return s == "openai" || s == "anthropic" || s == "xai" || s == "google"
}

func (g *GitHub) authorizeWrite(ctx context.Context) error {
	if g == nil || g.beforeWrite == nil {
		return fail(503, "app_unavailable", "Doctrine writes require a fresh authorization check.")
	}
	return g.beforeWrite(ctx)
}

func (g *GitHub) merge(ctx context.Context, p *Proposal) error {
	if err := g.authorizeWrite(ctx); err != nil {
		return err
	}
	var out struct {
		Merged bool   `json:"merged"`
		SHA    string `json:"sha"`
	}
	if err := g.request(ctx, "PUT", fmt.Sprintf("%s/pulls/%d/merge", repoPath(p.Repository), p.PRNumber), map[string]any{"sha": p.HeadSHA, "merge_method": "squash", "commit_title": "Propose doctrine rule change", "commit_message": "Signed-off-by: " + g.botName + " <" + g.botEmail + ">"}, &out); err != nil {
		return err
	}
	if !out.Merged || !shaPattern.MatchString(out.SHA) {
		return gitFail("GitHub did not confirm a merge")
	}
	p.MergeCommit = out.SHA
	p.State = "merged"
	return nil
}
func (g *GitHub) releaseRequest(ctx context.Context, p Proposal) error {
	if err := g.authorizeWrite(ctx); err != nil {
		return err
	}
	return g.request(ctx, "POST", repoPath(p.Repository)+"/dispatches", map[string]any{"event_type": "doctrine-release", "client_payload": map[string]string{"proposal_id": p.ID, "merge_commit": p.MergeCommit, "requested_scheme": "CalVer3"}}, nil)
}

const releaseMarker = "aeon-doctrine-release: "

// The release workflow publishes this provenance as its complete release body
// or on a separate line. Its tag is independently resolved; target_commitish
// (often just "main") is never treated as commit evidence.
func (g *GitHub) observeRelease(ctx context.Context, p *Proposal) error {
	var releases []struct {
		Tag   string `json:"tag_name"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
		Pre   bool   `json:"prerelease"`
	}
	if err := g.request(ctx, "GET", repoPath(p.Repository)+"/releases?per_page=100", nil, &releases); err != nil {
		return err
	}
	for _, r := range releases {
		if r.Draft || r.Pre || !validRef(r.Tag) {
			continue
		}
		for _, line := range strings.Split(r.Body, "\n") {
			if !strings.HasPrefix(line, releaseMarker) {
				continue
			}
			var evidence struct {
				ProposalID  string `json:"proposal_id"`
				MergeCommit string `json:"merge_commit"`
				Scheme      string `json:"version_scheme"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, releaseMarker)), &evidence) != nil || evidence.ProposalID != p.ID || evidence.MergeCommit != p.MergeCommit || evidence.Scheme != "CalVer3" {
				continue
			}
			commit, err := g.Commit(ctx, p.Repository, "refs/tags/"+r.Tag)
			if err != nil {
				return err
			}
			if commit.SHA != p.MergeCommit {
				var cmp struct {
					Status string `json:"status"`
				}
				if err := g.request(ctx, "GET", repoPath(p.Repository)+"/compare/"+p.MergeCommit+"..."+commit.SHA, nil, &cmp); err != nil {
					return err
				}
				if cmp.Status != "ahead" {
					continue
				}
			}
			p.Release = r.Tag
			p.ReleaseCommit = commit.SHA
			p.ReleaseURL = "https://github.com/" + p.Repository + "/releases/tag/" + url.PathEscape(r.Tag)
			p.State = "released"
			return nil
		}
	}
	return nil
}
