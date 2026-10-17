// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type auditGitFixture struct {
	t                                  *testing.T
	dir, base, reviewed, updated, head string
}

func (f *auditGitFixture) git(args ...string) string {
	f.t.Helper()
	out, err := f.command(args...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func (f *auditGitFixture) command(args ...string) *exec.Cmd {
	c := exec.CommandContext(f.t.Context(), "git", append([]string{"-c", "user.name=Audit fixture", "-c", "user.email=audit@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=disabled-test-hooks"}, args...)...)
	c.Dir = f.dir
	return c
}

func newAuditGitFixture(t *testing.T, variant string) *auditGitFixture {
	t.Helper()
	f := &auditGitFixture{t: t, dir: t.TempDir()}
	f.git("init", "--initial-branch=main", "--object-format=sha1")
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i+1)
	}
	write := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.dir, "shared.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if err := os.WriteFile(filepath.Join(f.dir, "removed.txt"), []byte("obsolete\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.git("add", ".")
	f.git("commit", "-m", "base")
	f.base = f.git("rev-parse", "HEAD")
	f.git("checkout", "-b", "ticket")
	lines[1] = "reviewed"
	write()
	if err := os.Remove(filepath.Join(f.dir, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "added.sh"), []byte("#!/bin/sh\necho reviewed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	f.git("add", ".")
	f.git("commit", "-m", "reviewed delta")
	f.reviewed = f.git("rev-parse", "HEAD")
	f.git("checkout", "main")
	lines[1] = "line-02"
	if variant == "conflict" {
		lines[1] = " line-02 " // same stable patch-id after resolving to the PR side
	} else {
		lines[34] = "base advanced far from the PR hunk"
	}
	write()
	f.git("add", ".")
	f.git("commit", "-m", "advance base")
	f.updated = f.git("rev-parse", "HEAD")
	f.git("checkout", "ticket")
	if variant == "conflict" {
		out, err := f.command("merge", "--no-commit", "--no-ff", "main").CombinedOutput()
		if err == nil || !strings.Contains(string(out), "CONFLICT (content)") {
			t.Fatalf("fixture did not establish a content conflict: %v %s", err, out)
		}
	} else {
		f.git("merge", "--no-commit", "--no-ff", "main")
	}
	lines[1] = "reviewed"
	if variant == "edited" {
		lines[1] = "unreviewed edit during merge"
	}
	write()
	f.git("add", ".")
	f.git("commit", "-m", "merge base")
	f.head = f.git("rev-parse", "HEAD")
	return f
}

// Serve GitHub's JSON shapes from real immutable Git objects. This makes the
// test's clean/conflicted DAG and patch-id assertions independent of the code
// being tested, without network access or a checkout in the production reader.
func (f *auditGitFixture) get(path string, into any) error {
	f.t.Helper()
	var body any
	path, _, _ = strings.Cut(path, "?")
	switch {
	case strings.HasPrefix(path, "/git/commits/"):
		sha := strings.TrimPrefix(path, "/git/commits/")
		fields := strings.Split(f.git("show", "-s", "--format=%H%n%T%n%P", sha), "\n")
		parents := []map[string]string{}
		for _, parent := range strings.Fields(fields[2]) {
			parents = append(parents, map[string]string{"sha": parent})
		}
		body = map[string]any{"sha": fields[0], "tree": map[string]string{"sha": fields[1]}, "parents": parents}
	case strings.HasPrefix(path, "/git/trees/"):
		sha := strings.TrimPrefix(path, "/git/trees/")
		tree := []map[string]string{}
		for _, line := range strings.Split(f.git("ls-tree", "-r", sha), "\n") {
			parts := strings.Fields(line)
			tree = append(tree, map[string]string{"path": parts[3], "mode": parts[0]})
		}
		body = map[string]any{"sha": sha, "truncated": false, "tree": tree}
	case strings.HasPrefix(path, "/compare/"):
		from, to, _ := strings.Cut(strings.TrimPrefix(path, "/compare/"), "...")
		mb := f.git("merge-base", from, to)
		files := []auditDiffFile{}
		for _, line := range strings.Split(f.git("diff", "--name-status", "--no-renames", mb, to), "\n") {
			if line == "" {
				continue
			}
			kind, name, _ := strings.Cut(line, "\t")
			nums := strings.Fields(f.git("diff", "--numstat", "--no-renames", mb, to, "--", name))
			adds, _ := strconv.Atoi(nums[0])
			dels, _ := strconv.Atoi(nums[1])
			diff := f.git("diff", "--no-ext-diff", "--no-renames", mb, to, "--", name)
			_, patch, ok := strings.Cut(diff, "\n@@ ")
			if !ok {
				f.t.Fatalf("fixture needs a text patch: %s", diff)
			}
			files = append(files, auditDiffFile{Name: name, Status: map[string]string{"M": "modified", "A": "added", "D": "removed"}[kind], Patch: "@@ " + patch, Additions: adds, Deletions: dels})
		}
		status := "ahead"
		if from == to {
			status = "identical"
		}
		body = map[string]any{"status": status, "base_commit": map[string]string{"sha": from}, "merge_base_commit": map[string]string{"sha": mb}, "files": files}
	default:
		f.t.Fatalf("unbound GitHub read: %s", path)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func (f *auditGitFixture) patchID(base, head string) string {
	f.t.Helper()
	diff := f.git("diff", "--no-ext-diff", base, head)
	cmd := f.command("patch-id", "--stable")
	cmd.Stdin = strings.NewReader(diff + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git patch-id: %v: %s", err, out)
	}
	return strings.Fields(string(out))[0]
}

func TestMergeAuditReviewCarryGitProof(t *testing.T) {
	// Risk: treating a merge message/ancestry as an independent review, or
	// missing conflict resolutions which whitespace-insensitive patch-id hides.
	for _, variant := range []string{"clean", "edited", "conflict"} {
		t.Run(variant, func(t *testing.T) {
			f := newAuditGitFixture(t, variant)
			before, after := f.patchID(f.base, f.reviewed), f.patchID(f.updated, f.head)
			if (before == after) != (variant != "edited") {
				t.Fatalf("fixture has wrong patch identity: %s %s", before, after)
			}
			for _, binding := range [][2]string{{f.base, f.reviewed}, {f.updated, f.head}} {
				var c auditCompare
				if err := f.get("/compare/"+binding[0]+"..."+binding[1], &c); err != nil {
					t.Fatal(err)
				}
				id, _, ok := auditPatchID(c.Files, map[string]string{"shared.txt": "100644", "removed.txt": "100644"}, map[string]string{"shared.txt": "100644", "added.sh": "100755"})
				if !ok || hex.EncodeToString(id[:]) != f.patchID(binding[0], binding[1]) {
					t.Fatalf("stable patch ID disagrees with Git: %x, valid=%v", id, ok)
				}
			}
			clean, err := readAuditReviewCarry(f.get, f.base, f.reviewed, f.updated, f.head)
			if err != nil || clean != (variant == "clean") {
				t.Fatalf("carry %s: %v %v", variant, clean, err)
			}
			for _, shape := range []string{"truncated-tree", "missing-patch", "file-limit", "wrong-parent", "wrong-target", "mode-change"} {
				t.Run(shape, func(t *testing.T) {
					get := func(path string, into any) error {
						if err := f.get(path, into); err != nil {
							return err
						}
						raw, _ := json.Marshal(into)
						var body map[string]any
						if err := json.Unmarshal(raw, &body); err != nil {
							return err
						}
						switch {
						case shape == "truncated-tree" && strings.HasPrefix(path, "/git/trees/"):
							body["truncated"] = true
						case shape == "mode-change" && strings.HasPrefix(path, "/git/trees/"):
							body["tree"].([]any)[0].(map[string]any)["mode"] = "120000"
						case shape == "wrong-parent" && path == "/git/commits/"+f.head:
							body["parents"].([]any)[0].(map[string]any)["sha"] = f.base
						case shape == "wrong-target" && strings.HasPrefix(path, "/compare/"+f.updated+"..."+f.updated):
							body["merge_base_commit"] = map[string]string{"sha": f.base}
						case shape == "missing-patch" && strings.HasPrefix(path, "/compare/"+f.base+"..."+f.reviewed):
							body["files"].([]any)[0].(map[string]any)["patch"] = ""
						case shape == "file-limit" && strings.HasPrefix(path, "/compare/"+f.base+"..."+f.reviewed):
							body["files"] = make([]auditDiffFile, 300)
						}
						raw, _ = json.Marshal(body)
						return json.Unmarshal(raw, into)
					}
					if clean, err := readAuditReviewCarry(get, f.base, f.reviewed, f.updated, f.head); err != nil || clean {
						t.Fatalf("unsupported proof accepted: %v %v", clean, err)
					}
				})
			}
		})
	}
}

func TestMergeAuditReviewCarryRechecksVerifiedGate(t *testing.T) {
	// Risk: equivalent patches bypass model/family/evidence verification, or
	// a proof prepared before the final fence authorizes an invalidated review.
	for _, variant := range []string{"clean", "edited", "conflict", "invalidated", "wrong-pr"} {
		t.Run(variant, func(t *testing.T) {
			f := newFixture(t)
			f.build(t)
			repo := newAuditGitFixture(t, variant)
			boundPR := int64(7)
			if variant == "wrong-pr" {
				boundPR = 8
			}
			f.reviewRange(t, repo.base, repo.reviewed, &boundPR)
			p := f.pull()
			p.Open, p.Merged, p.Head, p.Base = false, true, repo.head, repo.updated
			f.gh.pulls[7] = p
			pr := int64(7)
			fact := MergeFact{SHA: strings.Repeat("c", 40), Head: p.Head, Base: p.Base, Title: p.Title, Branch: p.Branch, MergedBy: "github-merge-queue[bot]", PR: &pr, At: f.at}
			g := &auditFake{fakeGitHub: f.gh, facts: map[string]*MergeFact{}, checks: map[string][]Check{fact.SHA: auditGreen()}}
			proofs := 0
			g.carry = func(_ context.Context, base, reviewed, target, head string) (bool, error) {
				proofs++
				if base != repo.base || reviewed != repo.reviewed || target != repo.updated || head != repo.head {
					t.Fatal("proof did not bind the review and merged source head")
				}
				clean, err := readAuditReviewCarry(repo.get, base, reviewed, target, head)
				if variant == "invalidated" {
					f.tx(t, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET github_status='stale'`)
						return err
					})
				}
				return clean, err
			}
			f.m.github = g
			auditSend(t, f, "pull_request", "carry-review-"+variant, auditPRBody(f, fact), 204)
			a := auditRead(t, f, fact.SHA)
			if a.ReviewOK != (variant == "clean") || !a.ViaQueue || !a.ChecksPassed {
				t.Fatalf("review carry verification: %+v", a)
			}
			if (proofs == 0) != (variant == "wrong-pr") {
				t.Fatalf("unexpected proof count: %d", proofs)
			}
			if variant != "clean" && !reflect.DeepEqual(a.Flags, []string{"no_review"}) {
				t.Fatalf("missing review did not stay closed: %+v", a)
			}
		})
	}
}
