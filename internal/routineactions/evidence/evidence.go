// SPDX-License-Identifier: AGPL-3.0-only
// Package evidence carries bounded catalogue adapter observations without
// importing the recurrence, run, review or delivery controllers.
package evidence

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

func bounded(s string, n int, required bool) bool {
	return len(s) <= n && utf8.ValidString(s) && !strings.ContainsRune(s, 0) && (!required || strings.TrimSpace(s) != "")
}

type PROpen struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
	BaseSHA    string `json:"base_sha"`
	HeadSHA    string `json:"head_sha"`
	Title      string `json:"title"`
	Body       string `json:"body"`
}

func (v PROpen) Valid() bool {
	return reviewgate.ValidRepository(v.Repository) && validBranch(v.Branch) && v.Branch != "main" && v.BaseBranch == "main" && reviewgate.ValidSHA(v.BaseSHA) && reviewgate.ValidSHA(v.HeadSHA) && v.BaseSHA != v.HeadSHA && bounded(v.Title, 500, true) && !strings.ContainsAny(v.Title, "\r\n") && bounded(v.Body, 32768, false) && !strings.Contains(v.Body, "aeon-routine-operation:")
}

func validBranch(s string) bool {
	if !bounded(s, 255, true) || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.Contains(s, "//") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return strings.IndexFunc(s, func(c rune) bool { return c <= 32 || c >= 127 || strings.ContainsRune("~^:?*[\\", c) }) < 0
}

type Pull struct {
	Number                                                         int64
	Repository, Branch, BaseBranch, BaseSHA, HeadSHA, OperationRef string
	Open                                                           bool
}

func (v Pull) Matches(in PROpen, ref string) bool {
	return v.Number > 0 && v.Number <= 2147483647 && v.Repository == in.Repository && v.Branch == in.Branch && v.BaseBranch == in.BaseBranch && v.BaseSHA == in.BaseSHA && v.HeadSHA == in.HeadSHA && v.OperationRef == ref && v.Open
}

type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type Run struct {
	ID         int64   `json:"id"`
	Attempt    int     `json:"attempt"`
	Head       string  `json:"head_sha"`
	Workflow   string  `json:"workflow"`
	Event      string  `json:"event"`
	Status     string  `json:"status"`
	Conclusion string  `json:"conclusion"`
	Jobs       []Check `json:"jobs"`
}

// Facts are bounded, normalized read-only observations. They are never
// accepted in a request, or replayed as GitHub writes.
type Facts struct {
	PushedHead     string  `json:"pushed_head"`
	Base           string  `json:"base_sha"`
	Behind         bool    `json:"behind"`
	PR             *int64  `json:"pull_request"`
	PullHead       string  `json:"pull_head"`
	Open           bool    `json:"open"`
	Merged         bool    `json:"merged"`
	Draft          bool    `json:"draft"`
	Conflict       bool    `json:"conflict"`
	MergeableKnown bool    `json:"mergeable_known"`
	Queued         bool    `json:"queued"`
	QueueHead      string  `json:"queue_head"`
	Checks         []Check `json:"checks"`
	Runs           []Run   `json:"runs"`
}

type PipelineFacts struct {
	Facts       Facts
	ArtifactSHA string
	ObservedAt  time.Time
	MergeSHA    string
}
type Review struct {
	OrderID, Repository, BaseSHA, HeadSHA, PolicyDigest string
	GateOpen                                            bool
	Verdict                                             string
}
