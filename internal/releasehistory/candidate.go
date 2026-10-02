// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// committedFile bounds a blob before reading or decoding it. Candidate inputs
// come from HEAD, so uncommitted edits cannot change the embedded release.
func committedFile(git func(...string) (string, error), path string, limit int64) ([]byte, error) {
	ref := "HEAD:" + path
	size, err := git("cat-file", "-s", ref)
	if err != nil {
		return nil, fmt.Errorf("read committed %s: %w", path, err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil || n < 0 || n > limit {
		return nil, fmt.Errorf("committed %s exceeds size limit", path)
	}
	raw, err := git("show", ref)
	return []byte(raw), err
}

func withCandidate(h History, head versionFile, opts Options, git func(...string) (string, error)) (History, error) {
	v := opts.Candidate
	if !ValidVersion(v) || head.Version != v || !CalendarScheme(head.VersionScheme) || head.Product == "" || head.ReleaseChannel == "" || head.ReleaseSequence < 1 {
		return History{}, fmt.Errorf("candidate must match committed version.json and its release metadata")
	}
	at, err := time.Parse(time.RFC3339, head.ReservedAt)
	if err != nil || !at.Equal(*coordinateTime(v)) {
		return History{}, fmt.Errorf("candidate reservation time must match its coordinate")
	}
	for _, reservation := range head.UnpublishedReservations {
		if strings.TrimPrefix(reservation, "v") == v {
			return History{}, fmt.Errorf("candidate is listed as an unpublished reservation")
		}
	}
	// Once the tag exists the normal builder wins, byte for byte, including
	// when newer tags have since been fetched. A later build fills evidence
	// without rewriting the original image manifest.
	for _, rel := range h.Releases {
		if rel.Version == v {
			return h, nil
		}
	}
	previous := ""
	for _, rel := range h.Releases {
		if rel.State == StatePublished {
			if rel.Version >= v || rel.ReleaseSequence >= head.ReleaseSequence {
				return History{}, fmt.Errorf("candidate must follow published release history")
			}
			if previous == "" {
				previous = rel.Tag
			}
		}
	}
	commit, err := git("rev-parse", "HEAD^{commit}")
	if err != nil {
		return History{}, err
	}
	commit = strings.TrimSpace(commit)
	changes, omitted, err := changes(git, previous, commit)
	if err != nil {
		return History{}, err
	}
	notes, err := candidateNotes(h, head, git)
	if err != nil {
		return History{}, err
	}
	r := Release{
		Version: v, State: StateCandidate, ReleaseChannel: head.ReleaseChannel,
		ReleaseSequence: head.ReleaseSequence, ReservedAt: &at, Notes: notes,
		Tickets: Tickets(head.Ticket), Changes: changes, ChangesOmitted: omitted,
		Evidence: Evidence{SourceCommit: commit, Unavailable: []string{},
			Pending: []string{"tag_message", "tagged_at", "published_at", "image", "release_run", "release_url"}},
	}
	if opts.Repository != "" {
		r.Evidence.SourceURL = "https://github.com/" + opts.Repository + "/commit/" + commit
	}
	if opts.CandidateCI != nil {
		run := *opts.CandidateCI
		r.Evidence.CI = &run
	} else {
		r.Evidence.Pending = append(r.Evidence.Pending, "ci")
	}
	h.Releases = append(h.Releases, r)
	Sort(h.Releases)
	return h, nil
}

func candidateNotes(h History, head versionFile, git func(...string) (string, error)) (*Notes, error) {
	path := "release-notes/" + head.Version + ".json"
	present, err := git("ls-tree", "--name-only", "HEAD", "--", path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(present) != "" {
		raw, err := committedFile(git, path, 16<<20)
		if err != nil {
			return nil, err
		}
		return NotesFromSnapshot(raw, head.Version, "HEAD:"+path)
	}
	raw, err := committedFile(git, ProductNotesPath, 16<<20)
	if err != nil {
		return nil, fmt.Errorf("candidate requires committed captured notes: %w", err)
	}
	bundle, err := ReadProductNotes(raw)
	if err != nil {
		return nil, err
	}
	notes, ok := bundle.Releases[head.Version]
	if !sameProductNotes(h, bundle) || !ok || (notes.ReleaseChannel != "" && notes.ReleaseChannel != head.ReleaseChannel) || (notes.ReleaseSequence != 0 && notes.ReleaseSequence != head.ReleaseSequence) {
		return nil, fmt.Errorf("candidate requires matching committed product notes")
	}
	withNotes := withProductNotes(History{Product: h.Product, Repository: h.Repository, Releases: []Release{{Version: head.Version}}}, bundle)
	return withNotes.Releases[0].Notes, nil
}
