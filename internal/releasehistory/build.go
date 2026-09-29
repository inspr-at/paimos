// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxChanges bounds one release's change list; the rest is counted, not listed.
const MaxChanges = 300

// Options say where to read the history from.
type Options struct {
	// Repo is the git working tree (with its tags fetched).
	Repo string
	// Repository is "owner/name" on GitHub, for links and evidence.
	Repository string
	// GitHub, when set, adds publication times, image digests and CI runs.
	GitHub *GitHub
	Now    func() time.Time
}

// versionFile is the part of version.json the history reads.
type versionFile struct {
	Product                 string   `json:"product"`
	VersionScheme           string   `json:"version_scheme"`
	Version                 string   `json:"version"`
	ReleaseChannel          string   `json:"release_channel"`
	ReleaseSequence         int      `json:"release_sequence"`
	ReservedAt              string   `json:"reserved_at"`
	Ticket                  string   `json:"ticket"`
	UnpublishedReservations []string `json:"unpublished_reservations"`
}

type tagRef struct {
	name, version, commit, message string
	tagged                         *time.Time
}

// Build reads the repository's tags, their version.json and the commits between
// them, and (with GitHub) their publication evidence, into one History.
func Build(ctx context.Context, opts Options) (History, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	git := func(args ...string) (string, error) { return runGit(ctx, opts.Repo, args...) }
	var head versionFile
	raw, err := os.ReadFile(filepath.Join(opts.Repo, "version.json"))
	if err != nil {
		return History{}, fmt.Errorf("read version.json: %w", err)
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return History{}, fmt.Errorf("parse version.json: %w", err)
	}
	h := History{
		Schema: Schema, Product: head.Product, Repository: opts.Repository, VersionScheme: head.VersionScheme,
		GeneratedAt: now().UTC().Truncate(time.Second), Source: "git", Releases: []Release{},
	}
	if h.VersionScheme == "" {
		h.VersionScheme = SchemeCalVer2
	}
	tags, err := listTags(git)
	if err != nil {
		return History{}, err
	}
	reserved := map[string]bool{}
	for _, v := range head.UnpublishedReservations {
		reserved[strings.TrimPrefix(v, "v")] = true
	}

	// Oldest first, so each release knows the one before it.
	byVersion := map[string]bool{}
	previousPublished, previousAny := "", ""
	for _, tag := range tags {
		byVersion[tag.version] = true
		r := Release{Version: tag.version, Tag: tag.name, State: StatePublished, TaggedAt: tag.tagged, Tickets: []string{}, Changes: []Change{}}
		if reserved[tag.version] {
			r.State = StateReserved
		}
		headline, channel, sequence := ParseTagMessage(tag.message)
		r.Headline, r.ReleaseChannel, r.ReleaseSequence = headline, channel, sequence
		var atTag versionFile
		if raw, err := git("show", tag.name+":version.json"); err == nil && json.Unmarshal([]byte(raw), &atTag) == nil {
			if atTag.ReleaseChannel != "" {
				r.ReleaseChannel = atTag.ReleaseChannel
			}
			if r.ReleaseSequence == 0 {
				r.ReleaseSequence = atTag.ReleaseSequence
			}
			if t, err := time.Parse(time.RFC3339, atTag.ReservedAt); err == nil && strings.TrimPrefix(atTag.Version, "v") == tag.version {
				r.ReservedAt = &t
			}
			if strings.TrimPrefix(atTag.Version, "v") == tag.version {
				r.Tickets = Tickets(atTag.Ticket, headline)
			}
		}
		if len(r.Tickets) == 0 {
			r.Tickets = Tickets(headline)
		}
		if r.ReservedAt == nil {
			r.ReservedAt = coordinateTime(tag.version)
		}
		if r.ReleaseChannel == "" {
			r.ReleaseChannel = "stable"
		}
		// Changes: a published release lists everything since the previous published
		// one; a reservation lists what was new when it was made.
		from := previousPublished
		if r.State == StateReserved {
			from = previousAny
		}
		r.Changes, r.ChangesOmitted, err = changes(git, from, tag.name)
		if err != nil {
			return History{}, err
		}
		r.Evidence = Evidence{SourceCommit: tag.commit, Unavailable: []string{}}
		// The snapshot must be in the immutable tag, never today's live fields.
		snapshotPath := "release-notes/" + tag.version + ".json"
		r.Notes = MissingNotes()
		present, listErr := git("ls-tree", "--name-only", tag.name, "--", snapshotPath)
		if listErr != nil {
			return History{}, listErr
		}
		if strings.TrimSpace(present) != "" {
			raw, readErr := git("show", tag.name+":"+snapshotPath)
			if readErr != nil {
				return History{}, readErr
			}
			r.Notes, err = NotesFromSnapshot([]byte(raw), tag.version, tag.name+":"+snapshotPath)
			if err != nil {
				return History{}, fmt.Errorf("%s: %w", snapshotPath, err)
			}
		}

		if opts.Repository != "" {
			r.Evidence.SourceURL = "https://github.com/" + opts.Repository + "/commit/" + tag.commit
		}
		h.Releases = append(h.Releases, r)
		previousAny = tag.name
		if r.State == StatePublished {
			previousPublished = tag.name
		}
	}
	// Reservations that never got a tag still belong to the history.
	for v := range reserved {
		if byVersion[v] || !ValidVersion(v) {
			continue
		}
		h.Releases = append(h.Releases, Release{
			Notes: MissingNotes(), Version: v, State: StateReserved, ReleaseChannel: "stable", ReservedAt: coordinateTime(v), Tickets: []string{}, Changes: []Change{},
			Evidence: Evidence{Unavailable: []string{"This version was reserved but never tagged or published."}},
		})
	}
	Sort(h.Releases)
	if opts.GitHub != nil {
		if err := opts.GitHub.enrich(ctx, opts.Repository, h.Releases); err != nil {
			for i := range h.Releases {
				h.Releases[i].Evidence.Unavailable = append(h.Releases[i].Evidence.Unavailable, "GitHub could not be read for this build ("+err.Error()+"), so publication, image and CI evidence are missing.")
			}
		} else {
			h.Source = "git+github"
		}
	} else {
		for i := range h.Releases {
			if h.Releases[i].Tag != "" {
				h.Releases[i].Evidence.Unavailable = append(h.Releases[i].Evidence.Unavailable, "GitHub was not consulted for this build, so the publication time, image digest and CI runs are not recorded.")
			}
		}
	}
	return h, nil
}

// coordinateTime reads the reservation instant a calendar coordinate encodes (UTC).
func coordinateTime(v string) *time.Time {
	t, err := time.Parse("060102150405", strings.TrimSuffix(v, ".0.0"))
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

func listTags(git func(...string) (string, error)) ([]tagRef, error) {
	out, err := git("for-each-ref", "--sort=refname", "--format=%(refname:short)%1f%(objecttype)%1f%(taggerdate:iso-strict)%1f%(*objectname)%1f%(contents)%1e", "refs/tags/v*")
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	tags := []tagRef{}
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		f := strings.SplitN(record, "\x1f", 5)
		if len(f) < 5 || f[1] != "tag" {
			continue // only annotated tags are releases
		}
		version := strings.TrimPrefix(f[0], "v")
		if f[0] != "v"+version || !ValidVersion(version) {
			continue
		}
		t := tagRef{name: f[0], version: version, commit: f[3], message: f[4]}
		if at, err := time.Parse(time.RFC3339, f[2]); err == nil {
			at = at.UTC()
			t.tagged = &at
		}
		tags = append(tags, t)
	}
	// Coordinates sort as time.
	for i := 1; i < len(tags); i++ {
		for j := i; j > 0 && tags[j].version < tags[j-1].version; j-- {
			tags[j], tags[j-1] = tags[j-1], tags[j]
		}
	}
	return tags, nil
}

func changes(git func(...string) (string, error), from, to string) ([]Change, int, error) {
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out, err := git("log", "--no-merges", "--format=%H%x1f%s%x1f%aI%x1e", rng)
	if err != nil {
		return nil, 0, fmt.Errorf("log %s: %w", rng, err)
	}
	list := []Change{}
	omitted := 0
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		f := strings.SplitN(record, "\x1f", 3)
		if len(f) < 3 {
			continue
		}
		if len(list) >= MaxChanges {
			omitted++
			continue
		}
		kind, scope := Classify(f[1])
		list = append(list, Change{Commit: f[0], Subject: f[1], Type: kind, Scope: scope, Tickets: Tickets(scope, f[1]), At: f[2]})
	}
	return list, omitted, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return stdout.String(), nil
}
