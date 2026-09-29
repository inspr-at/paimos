// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/rulesimport"
)

const (
	// MaxFiles bounds the doctrine files one source indexes.
	MaxFiles = 64
	// MaxSourceBytes bounds the bytes one source caches at its commit.
	MaxSourceBytes = 4 << 20
	// MaxSidecarBytes bounds one TL;DR sidecar file.
	MaxSidecarBytes = 64 << 10
	// MaxPatterns bounds the path patterns of one source.
	MaxPatterns = 20
	maxSkipped  = 50
)

// DefaultPaths selects the doctrine files of an INSPR doctrine repository.
var DefaultPaths = []string{"docs/AGENTS-*.md"}

var patternPattern = regexp.MustCompile(`^[A-Za-z0-9._*?-][A-Za-z0-9._*?/-]{0,199}$`)

// File is one cached blob at the pinned commit: a doctrine file or the TL;DR
// sidecar next to one. Content is the exact bytes of the git object.
type File struct {
	Path    string `json:"path"`
	BlobSHA string `json:"blob_sha"`
	Content []byte `json:"-"`
}

// Skip is a file a pattern selected that is not indexed, with the reason.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// SidecarPath is where the TL;DRs of a doctrine file live in git: the same
// directory and name, with .tldr.yaml in place of .md.
func SidecarPath(doc string) string {
	return strings.TrimSuffix(doc, ".md") + ".tldr.yaml"
}

// ValidPatterns checks the path patterns of one source.
func ValidPatterns(patterns []string) bool {
	if len(patterns) == 0 || len(patterns) > MaxPatterns {
		return false
	}
	seen := map[string]bool{}
	for _, p := range patterns {
		if !patternPattern.MatchString(p) || strings.Contains(p, "..") || strings.Contains(p, "//") || strings.HasSuffix(p, "/") || seen[p] {
			return false
		}
		if _, err := path.Match(p, ""); err != nil {
			return false
		}
		seen[p] = true
	}
	return true
}

// selects reports whether one of patterns names file. A wildcard never
// selects a personal profile (AGENTS-PROFILE*.md): a person's doctrine is
// indexed only when its exact path is configured.
func selects(patterns []string, file string) bool {
	for _, p := range patterns {
		if p == file {
			return true
		}
		if ok, _ := path.Match(p, file); ok && !strings.HasPrefix(path.Base(file), "AGENTS-PROFILE") {
			return true
		}
	}
	return false
}

// Fetch reads the doctrine files patterns select at commit, and the TL;DR
// sidecar next to each. It reads blobs only by their object id, so the bytes
// are exactly those at the commit. Selected files the importer does not
// recognise, or that are too large, are skipped with a reason.
func Fetch(ctx context.Context, r Reader, repository, commit string, patterns []string) ([]File, []Skip, error) {
	entries, err := r.Tree(ctx, repository, commit)
	if err != nil {
		return nil, nil, err
	}
	byPath := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byPath[e.Path] = e
	}
	var wanted []Entry
	var skipped []Skip
	docs := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Path, ".md") || !selects(patterns, e.Path) {
			continue
		}
		switch {
		case rulesimport.Prohibited(e.Path) != nil:
			skipped = append(skipped, Skip{Path: e.Path, Reason: "path not allowed"})
		case !rulesimport.Classifies(e.Path):
			skipped = append(skipped, Skip{Path: e.Path, Reason: "not a doctrine rule file"})
		case e.Size > rulesimport.MaxFileBytes:
			skipped = append(skipped, Skip{Path: e.Path, Reason: "larger than 256 KiB"})
		default:
			docs++
			wanted = append(wanted, e)
			if side, ok := byPath[SidecarPath(e.Path)]; ok {
				if side.Size > MaxSidecarBytes {
					skipped = append(skipped, Skip{Path: side.Path, Reason: "larger than 64 KiB"})
				} else {
					wanted = append(wanted, side)
				}
			}
		}
	}
	if docs > MaxFiles {
		return nil, nil, gitFail("more than %d doctrine files match; narrow the paths", MaxFiles)
	}
	total := 0
	for _, e := range wanted {
		total += e.Size
	}
	if total > MaxSourceBytes {
		return nil, nil, gitFail("the selected doctrine is larger than 4 MiB; narrow the paths")
	}
	files := make([]File, 0, len(wanted))
	for _, e := range wanted {
		raw, err := r.Blob(ctx, repository, e.SHA, e.Size)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, File{Path: e.Path, BlobSHA: e.SHA, Content: raw})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Path < skipped[j].Path })
	if len(skipped) > maxSkipped {
		skipped = skipped[:maxSkipped]
	}
	if skipped == nil {
		skipped = []Skip{}
	}
	return files, skipped, nil
}
