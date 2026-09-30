// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

type boundedReviewBuffer struct {
	// Embedding would promote ReadFrom and let io.Copy bypass capped Write.
	buffer bytes.Buffer
	limit  int
}

func (b *boundedReviewBuffer) Len() int       { return b.buffer.Len() }
func (b *boundedReviewBuffer) String() string { return b.buffer.String() }

func (b *boundedReviewBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errReviewContext
	}
	return b.buffer.Write(p)
}

// reviewGit runs only in a helper-owned bare repository. In particular, neither
// Git discovery nor inherited configuration can redirect its repository.
func reviewGit(ctx context.Context, repo string, args ...string) (string, error) {
	flags := []string{"--no-replace-objects", "--no-pager", "--git-dir=" + repo,
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.attributesFile=" + os.DevNull}
	cmd := exec.CommandContext(ctx, "git", append(flags, args...)...)
	cmd.Dir = repo
	cmd.Env = []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "LC_ALL=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=file",
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C")
	out := &boundedReviewBuffer{limit: 192 << 10}
	cmd.Stdout = out
	if cmd.Run() != nil {
		return "", errReviewContext
	}
	return out.String(), nil
}

type sealedReviewRepo struct {
	dir, repository, base, head string
}

func (r *sealedReviewRepo) close() { _ = os.RemoveAll(r.dir) }

// newSealedReviewRepo imports only the sealed objects, never workspace refs,
// config, attributes, grafts, shallow boundaries, hooks or an index. An empty
// head captures the workspace's raw HEAD for the completed-change binding.
func newSealedReviewRepo(ctx context.Context, workspace, base, head string) (r *sealedReviewRepo, err error) {
	if strings.TrimSpace(workspace) == "" || !reviewgate.ValidSHA(base) || (head != "" && !reviewgate.ValidSHA(head)) {
		return nil, errReviewContext
	}
	dir, err := os.MkdirTemp("", "aeon-review-") // MkdirTemp creates mode 0700.
	if err != nil {
		return nil, errReviewContext
	}
	r = &sealedReviewRepo{dir: dir, base: base, head: head}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if _, err = reviewGit(ctx, dir, "init", "--bare", "--template=", "--object-format=sha1", "--initial-branch=review"); err != nil {
		return nil, errReviewContext
	}
	gitDir, commonDir, err := reviewWorkspaceDirs(workspace)
	if err != nil {
		return nil, errReviewContext
	}
	// Parse only the literal origin URL. Includes and URL rewrite rules are not
	// loaded, and none of the file's other settings configure this command.
	remote, err := reviewGit(ctx, dir, "config", "--file", filepath.Join(commonDir, "config"), "--no-includes", "--get", "remote.origin.url")
	if err != nil {
		return nil, errReviewContext
	}
	r.repository = reviewRepository(remote)
	if !reviewgate.ValidRepository(r.repository) {
		return nil, errReviewContext
	}
	if head == "" {
		r.head, err = reviewWorkspaceHEAD(gitDir, commonDir)
		if err != nil || !reviewgate.ValidSHA(r.head) {
			return nil, errReviewContext
		}
	}
	if r.base == r.head {
		return nil, errReviewContext
	}
	// Even upload-pack must not open the workspace's configuration. Expose its
	// object store through a private bare transport view with only our two refs.
	// Fetch copies the objects into dir; it does not borrow the object store.
	source := filepath.Join(dir, "source.git")
	if err = os.MkdirAll(filepath.Join(source, "refs", "heads"), 0700); err != nil {
		return nil, errReviewContext
	}
	if err = os.Symlink(filepath.Join(commonDir, "objects"), filepath.Join(source, "objects")); err != nil {
		return nil, errReviewContext
	}
	for name, contents := range map[string]string{
		"config":                 "[core]\nrepositoryformatversion = 0\nbare = true\n",
		"HEAD":                   "ref: refs/heads/sealed-head\n",
		"refs/heads/sealed-base": r.base + "\n", "refs/heads/sealed-head": r.head + "\n",
	} {
		if err = os.WriteFile(filepath.Join(source, name), []byte(contents), 0600); err != nil {
			return nil, errReviewContext
		}
	}
	if _, err = reviewGit(ctx, dir, "-c", "fetch.fsckObjects=true", "fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-maintenance", "--", source, r.base, r.head); err != nil {
		return nil, errReviewContext
	}
	for _, sha := range []string{r.base, r.head} {
		resolved, e := reviewGit(ctx, dir, "rev-parse", "--verify", sha+"^{commit}")
		if e != nil || strings.TrimSpace(resolved) != sha {
			return nil, errReviewContext
		}
	}
	if err = r.verifyAncestry(ctx); err != nil {
		return nil, errReviewContext
	}
	return r, nil
}

// verifyAncestry follows raw parent headers, never revision walkers or a
// merge-base that can consume grafts, replacement refs or commit-graph data.
func (r *sealedReviewRepo) verifyAncestry(ctx context.Context) error {
	pending := []string{r.head}
	seen := map[string]bool{}
	for len(pending) > 0 {
		sha := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if sha == r.base {
			return nil
		}
		if seen[sha] {
			continue
		}
		if len(seen) >= 4096 || ctx.Err() != nil {
			return errReviewContext
		}
		seen[sha] = true
		commit, err := reviewGit(ctx, r.dir, "cat-file", "commit", sha)
		if err != nil {
			return errReviewContext
		}
		headers, _, ok := strings.Cut(commit, "\n\n")
		if !ok {
			return errReviewContext
		}
		for _, line := range strings.Split(headers, "\n") {
			if parent, ok := strings.CutPrefix(line, "parent "); ok {
				if !reviewgate.ValidSHA(parent) {
					return errReviewContext
				}
				pending = append(pending, parent)
			}
		}
	}
	return errReviewContext
}

func reviewMetadataFile(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (192<<10)+1))
	if err != nil || len(b) > 192<<10 {
		return "", errReviewContext
	}
	return string(b), nil
}

func reviewWorkspaceDirs(workspace string) (string, string, error) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", errReviewContext
	}
	gitDir := filepath.Join(workspace, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return "", "", errReviewContext
	}
	if !info.IsDir() {
		gitfile, err := reviewMetadataFile(gitDir)
		name, ok := strings.CutPrefix(strings.TrimSpace(gitfile), "gitdir: ")
		if err != nil || !ok || name == "" {
			return "", "", errReviewContext
		}
		gitDir = name
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(workspace, gitDir)
		}
	}
	commonDir := gitDir
	common, err := reviewMetadataFile(filepath.Join(gitDir, "commondir"))
	if err == nil {
		commonDir = strings.TrimSpace(common)
		if commonDir == "" {
			return "", "", errReviewContext
		}
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	} else if !os.IsNotExist(err) {
		return "", "", errReviewContext
	}
	return gitDir, commonDir, nil
}

func reviewWorkspaceHEAD(gitDir, commonDir string) (string, error) {
	value, err := reviewMetadataFile(filepath.Join(gitDir, "HEAD"))
	for depth := 0; err == nil && depth < 8; depth++ {
		value = strings.TrimSpace(value)
		if reviewgate.ValidSHA(value) {
			return value, nil
		}
		ref, ok := strings.CutPrefix(value, "ref: ")
		if !ok || !strings.HasPrefix(ref, "refs/") || !branchRefName(ref) || filepath.ToSlash(filepath.Clean(ref)) != ref {
			break
		}
		value, err = reviewMetadataFile(filepath.Join(commonDir, filepath.FromSlash(ref)))
		if os.IsNotExist(err) {
			packed, e := reviewMetadataFile(filepath.Join(commonDir, "packed-refs"))
			if e != nil {
				break
			}
			var resolved string
			for _, line := range strings.Split(packed, "\n") {
				sha, name, ok := strings.Cut(line, " ")
				if ok && name == ref {
					// Duplicate records have no unambiguous binding. Never
					// attest one based on our own first/last-record choice.
					if resolved != "" || !reviewgate.ValidSHA(sha) {
						return "", errReviewContext
					}
					resolved = sha
				}
			}
			if resolved != "" {
				return resolved, nil
			}
		}
	}
	return "", errReviewContext
}
