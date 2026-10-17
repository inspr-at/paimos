// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Publication is private build evidence, embedded alongside (never served as
// part of) the public history. Display changes omit merges and may be capped;
// automation must instead use the complete Git ancestry delta.
type Publication struct {
	ProjectKey  string            `json:"project_key"`
	Repository  string            `json:"repository"`
	Version     string            `json:"version"`
	Name        string            `json:"name"`
	Tag         string            `json:"tag"`
	PreviousTag string            `json:"previous_tag"`
	Commit      string            `json:"commit"`
	TaggedAt    time.Time         `json:"tagged_at"`
	Commits     []string          `json:"commits"`
	Tickets     map[string]string `json:"tickets"`
}

const MaxPublicationCommits = 20000

// BuildPublication only binds the tagged commit being built. Candidates and
// untagged/dev builds cannot cause automatic delivery. Historic releases remain
// the responsibility of the separately reviewed OPS backfill.
func BuildPublication(ctx context.Context, opts Options, h History) (*Publication, error) {
	if h.Product != "PAIMOS AEON" {
		return nil, nil
	}
	git := func(args ...string) (string, error) { return runGit(ctx, opts.Repo, args...) }
	raw, err := committedFile(git, "version.json", 64<<10)
	if err != nil {
		return nil, err
	}
	var v versionFile
	if err = json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	head, err := git("rev-parse", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	for i, r := range h.Releases {
		if r.Version != v.Version || r.State != StatePublished || r.Tag == "" || r.Evidence.SourceCommit != strings.TrimSpace(head) {
			continue
		}
		p := &Publication{ProjectKey: "AEON", Repository: h.Repository, Version: r.Version, Name: r.Codename, Tag: r.Tag, Commit: r.Evidence.SourceCommit, Tickets: map[string]string{}}
		if r.TaggedAt == nil {
			return nil, fmt.Errorf("publication tag has no timestamp")
		}
		p.TaggedAt = *r.TaggedAt
		for _, prior := range h.Releases[i+1:] {
			if prior.State == StatePublished && (opts.GitHub == nil || prior.PublishedAt != nil) {
				p.PreviousTag = prior.Tag
				break
			}
		}
		if opts.GitHub != nil && p.PreviousTag == "" && i+1 < len(h.Releases) {
			return nil, fmt.Errorf("previous published tag could not be verified")
		}
		if p.PreviousTag != "" {
			if _, err := git("merge-base", "--is-ancestor", p.PreviousTag, p.Tag); err != nil {
				return nil, fmt.Errorf("previous publication is not an ancestor: %w", err)
			}
		}
		rng := p.Tag
		if p.PreviousTag != "" {
			rng = p.PreviousTag + ".." + p.Tag
		}
		// max-count bounds output before allocation. Refuse incomplete evidence.
		out, err := git("rev-list", "--max-count=20001", rng)
		if err != nil {
			return nil, err
		}
		p.Commits = strings.Fields(out)
		if len(p.Commits) > MaxPublicationCommits {
			return nil, fmt.Errorf("publication ancestry exceeds %d commits", MaxPublicationCommits)
		}
		// First-parent merges/squashes identify work even before delivery audit was
		// installed. Only the merge title/branch and PR title establish key linkage;
		// arbitrary references in a commit's remaining body do not establish it.
		out, err = git("log", "--first-parent", "--max-count=20001", "--format=%H%x1f%s%x1f%b%x1e", rng)
		if err != nil {
			return nil, err
		}
		for _, record := range strings.Split(out, "\x1e") {
			f := strings.SplitN(strings.TrimSpace(record), "\x1f", 3)
			if len(f) < 3 {
				continue
			}
			title := strings.SplitN(strings.TrimSpace(f[2]), "\n", 2)[0]
			for _, key := range Tickets(strings.ToUpper(f[1]), title) {
				if p.Tickets[key] == "" {
					p.Tickets[key] = f[0]
				}
			}
		}
		return p, nil
	}
	return nil, nil
}

// EmbeddedPublication reads private evidence without adding API response fields.
func EmbeddedPublication() (*Publication, error) {
	raw, err := data.ReadFile("data/history.json")
	if err != nil {
		return nil, nil
	} // source/dev builds have no generated manifest
	var envelope struct {
		Publication *Publication `json:"delivery_publication"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	return envelope.Publication, nil
}
