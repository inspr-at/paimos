// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"crypto/sha1"
	"fmt"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

// This capability proves immutable Git facts only. It cannot record a review
// or satisfy the review gate without VerifiedLatestTx in the final audit write.
type auditReviewGitHub interface {
	AuditReviewCarry(context.Context, string, string, string, string) (bool, error)
}

type auditGitCommit struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

type auditDiffFile struct {
	Name      string `json:"filename"`
	Status    string `json:"status"`
	Patch     string `json:"patch"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

type auditCompare struct {
	Status string `json:"status"`
	Base   struct {
		SHA string `json:"sha"`
	} `json:"base_commit"`
	MergeBase struct {
		SHA string `json:"sha"`
	} `json:"merge_base_commit"`
	Files []auditDiffFile `json:"files"`
}

func (g AppReader) AuditReviewCarry(ctx context.Context, base, reviewed, target, head string) (bool, error) {
	for _, sha := range []string{base, reviewed, target, head} {
		if !reviewgate.ValidSHA(sha) {
			return false, nil
		}
	}
	var clean bool
	err := g.readInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		var err error
		clean, err = readAuditReviewCarry(get, base, reviewed, target, head)
		return err
	})
	return clean, err
}

// Accept one merge of the target branch onto the reviewed first parent. The
// PR deltas must have equal git patch-id --stable values, and the base changes
// must not overlap their hunks. Trees also bind file modes, which the compare
// JSON omits. Unsupported/binary/renamed/truncated diffs fail closed: they need
// a review on the resulting head. No repository checkout or write is needed.
func readAuditReviewCarry(get func(string, any) error, base, reviewed, target, head string) (bool, error) {
	var merge auditGitCommit
	if err := get("/git/commits/"+head, &merge); err != nil {
		return false, err
	}
	if merge.SHA != head || len(merge.Parents) != 2 || merge.Parents[0].SHA != reviewed || !reviewgate.ValidSHA(merge.Parents[1].SHA) {
		return false, nil
	}
	updatedBase := merge.Parents[1].SHA
	compare := func(from, to string) (auditCompare, bool, error) {
		var c auditCompare
		err := get("/compare/"+from+"..."+to+"?per_page=1&page=1", &c)
		return c, c.Base.SHA == from && c.MergeBase.SHA == from && (c.Status == "ahead" || c.Status == "identical") && c.Files != nil && len(c.Files) < 300, err
	}
	// A second parent from another feature branch is not a base update.
	_, valid, err := compare(updatedBase, target)
	if err != nil || !valid {
		return false, err
	}
	before, valid, err := compare(base, reviewed)
	if err != nil || !valid || len(before.Files) == 0 {
		return false, err
	}
	after, valid, err := compare(updatedBase, head)
	if err != nil || !valid {
		return false, err
	}
	advance, valid, err := compare(base, updatedBase)
	if err != nil || !valid {
		return false, err
	}
	modes := map[string]map[string]string{}
	for _, sha := range []string{base, reviewed, updatedBase, head} {
		if modes[sha] != nil {
			continue
		}
		commit := merge
		if sha != head {
			if err := get("/git/commits/"+sha, &commit); err != nil {
				return false, err
			}
		}
		if commit.SHA != sha || !reviewgate.ValidSHA(commit.Tree.SHA) {
			return false, nil
		}
		var tree struct {
			SHA       string `json:"sha"`
			Truncated bool   `json:"truncated"`
			Tree      []struct {
				Path string `json:"path"`
				Mode string `json:"mode"`
			} `json:"tree"`
		}
		if err := get("/git/trees/"+commit.Tree.SHA+"?recursive=1", &tree); err != nil {
			return false, err
		}
		if tree.SHA != commit.Tree.SHA || tree.Truncated || tree.Tree == nil || len(tree.Tree) > 10000 {
			return false, nil
		}
		modes[sha] = map[string]string{}
		for _, entry := range tree.Tree {
			if _, duplicate := modes[sha][entry.Path]; duplicate {
				return false, nil
			}
			modes[sha][entry.Path] = entry.Mode
		}
	}
	oldID, oldHunks, ok := auditPatchID(before.Files, modes[base], modes[reviewed])
	if !ok {
		return false, nil
	}
	newID, _, ok := auditPatchID(after.Files, modes[updatedBase], modes[head])
	if !ok || oldID != newID {
		return false, nil
	}
	_, baseHunks, ok := auditPatchID(advance.Files, modes[base], modes[updatedBase])
	if !ok {
		return false, nil
	}
	for path, hunks := range oldHunks {
		other := baseHunks[path]
		for i, j := 0, 0; i < len(hunks) && j < len(other); {
			a, b := hunks[i], other[j]
			// Inclusive ends deliberately reject adjacent insertions and
			// uncertain overlap as well as actual conflict-resolution hunks.
			if a.start <= b.end && b.start <= a.end {
				return false, nil
			}
			if a.end < b.start {
				i++
			} else {
				j++
			}
		}
	}
	return true, nil
}

type auditHunk struct{ start, end int }

// Git's stable patch ID strips ASCII whitespace and hunk headers, hashes
// each file separately, then sums the SHA-1 digests with little-endian carry.
// Here a complete, bounded text patch and its tree modes are mandatory.
func auditPatchID(files []auditDiffFile, from, to map[string]string) ([sha1.Size]byte, map[string][]auditHunk, bool) {
	var id [sha1.Size]byte
	hunks := map[string][]auditHunk{}
	if len(files) >= 300 {
		return id, hunks, false
	}
	total := 0
	for _, f := range files {
		total += len(f.Patch)
		if !auditPatchPath(f.Name) || len(f.Patch) == 0 || len(f.Patch) > 1<<20 || total > 2<<20 || f.Additions < 0 || f.Deletions < 0 || f.Additions+f.Deletions > 100000 {
			return id, hunks, false
		}
		if _, duplicate := hunks[f.Name]; duplicate {
			return id, hunks, false
		}
		oldMode, newMode := from[f.Name], to[f.Name]
		header := "diff --git a/" + f.Name + " b/" + f.Name + "\n"
		oldPath, newPath := "a/"+f.Name, "b/"+f.Name
		switch f.Status {
		case "modified":
			if oldMode != newMode || !auditTextMode(oldMode) {
				return id, hunks, false
			}
		case "added":
			if oldMode != "" || !auditTextMode(newMode) {
				return id, hunks, false
			}
			header += "new file mode " + newMode + "\n"
			oldPath = "/dev/null"
		case "removed":
			if newMode != "" || !auditTextMode(oldMode) {
				return id, hunks, false
			}
			header += "deleted file mode " + oldMode + "\n"
			newPath = "/dev/null"
		default:
			return id, hunks, false
		}
		header += "--- " + oldPath + "\n+++ " + newPath + "\n"
		h := sha1.New()
		write := func(s string) {
			clean := make([]byte, 0, len(s))
			for i := 0; i < len(s); i++ {
				if c := s[i]; c != ' ' && (c < '\t' || c > '\r') {
					clean = append(clean, c)
				}
			}
			_, _ = h.Write(clean)
		}
		write(header)
		before, after, adds, deletes := 0, 0, 0, 0
		lastAfter := -1
		for _, line := range strings.Split(strings.TrimSuffix(f.Patch, "\n"), "\n") {
			if strings.HasPrefix(line, "@@ ") {
				if before != 0 || after != 0 {
					return id, hunks, false
				}
				parts := strings.SplitN(line, " ", 5)
				if len(parts) < 4 || parts[0] != "@@" || parts[3] != "@@" {
					return id, hunks, false
				}
				start, count, ok := auditHunkRange(parts[1], '-')
				startAfter, countAfter, okAfter := auditHunkRange(parts[2], '+')
				if !ok || !okAfter || count == 0 && countAfter == 0 {
					return id, hunks, false
				}
				before, after = count, countAfter
				prior := hunks[f.Name]
				if startAfter < lastAfter || len(prior) > 0 && start < prior[len(prior)-1].end {
					return id, hunks, false
				}
				lastAfter = startAfter + countAfter
				hunks[f.Name] = append(hunks[f.Name], auditHunk{start: start, end: start + count})
				continue
			}
			if line == "\\ No newline at end of file" {
				continue
			}
			if len(line) == 0 || len(hunks[f.Name]) == 0 {
				return id, hunks, false
			}
			switch line[0] {
			case ' ':
				before--
				after--
			case '-':
				before--
				deletes++
			case '+':
				after--
				adds++
			default:
				return id, hunks, false
			}
			if before < 0 || after < 0 {
				return id, hunks, false
			}
			write(line)
		}
		if before != 0 || after != 0 || adds != f.Additions || deletes != f.Deletions || len(hunks[f.Name]) == 0 {
			return id, hunks, false
		}
		carry := 0
		for i, b := range h.Sum(nil) {
			carry += int(id[i]) + int(b)
			id[i], carry = byte(carry), carry>>8
		}
	}
	return id, hunks, true
}

func auditTextMode(s string) bool { return s == "100644" || s == "100755" }

func auditPatchPath(s string) bool {
	if !validAdmissionPath(s) {
		return false
	}
	for _, c := range s {
		if c <= ' ' || c >= 127 || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

func auditHunkRange(s string, sign byte) (int, int, bool) {
	if len(s) < 2 || s[0] != sign {
		return 0, 0, false
	}
	start, count, hasCount := strings.Cut(s[1:], ",")
	a, err := strconv.Atoi(start)
	b := 1
	if hasCount {
		b, err = strconv.Atoi(count)
	}
	// Reject noncanonical/overflowing coordinates before any range arithmetic.
	canonical := fmt.Sprint(a)
	if hasCount {
		canonical += "," + fmt.Sprint(b)
	}
	return a, b, err == nil && a >= 0 && b >= 0 && a <= 10000000 && b <= 100000 && canonical == s[1:]
}
