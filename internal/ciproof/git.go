// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const maxGitOutput = 128 << 20

// Repository must be a dedicated controller-owned bare mirror, never a
// candidate checkout. Its config/tool path remain the operator's trust boundary.
type Repository struct{ dir, git string }

func OpenRepository(ctx context.Context, dir, git string) (*Repository, error) {
	if !filepath.IsAbs(dir) || !filepath.IsAbs(git) {
		return nil, fmt.Errorf("absolute mirror and Git executable paths required")
	}
	r := &Repository{dir: dir, git: git}
	b, err := r.read(ctx, nil, "rev-parse", "--is-bare-repository")
	if err != nil || string(b) != "true\n" {
		return nil, fmt.Errorf("dedicated bare mirror required")
	}
	return r, nil
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxGitOutput {
		return 0, fmt.Errorf("Git output limit exceeded")
	}
	return b.Buffer.Write(p)
}

func (r *Repository) read(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, r.git, append([]string{"--no-optional-locks", "--git-dir=" + r.dir}, args...)...)
	c.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_ATTR_NOSYSTEM=1"}
	c.Stdin = bytes.NewReader(input)
	var out boundedBuffer
	c.Stdout = &out
	c.Stderr = io.Discard
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("Git object read %s failed: %w", args[0], err)
	}
	return out.Bytes(), nil
}

func (r *Repository) commit(ctx context.Context, id string) (string, error) {
	if !objectID.MatchString(id) {
		return "", fmt.Errorf("immutable commit object ID required")
	}
	typ, err := r.read(ctx, nil, "cat-file", "-t", id)
	if err != nil || string(typ) != "commit\n" {
		return "", fmt.Errorf("commit object missing or wrong type")
	}
	b, err := r.read(ctx, nil, "rev-parse", "--verify", id+"^{tree}")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(b))
	if !objectID.MatchString(tree) {
		return "", fmt.Errorf("invalid tree object")
	}
	return tree, nil
}

// Snapshot enumerates NUL-delimited paths and independently hashes actual blob
// bytes. Symlinks are data; submodules and malformed/empty inventories fail closed.
func (r *Repository) Snapshot(ctx context.Context, commit string) (Snapshot, error) {
	tree, err := r.commit(ctx, commit)
	if err != nil {
		return Snapshot{}, err
	}
	raw, err := r.read(ctx, nil, "ls-tree", "-rz", "--full-tree", tree)
	if err != nil {
		return Snapshot{}, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return Snapshot{}, fmt.Errorf("empty or incomplete Git inventory")
	}
	entries := []Entry{}
	for _, row := range bytes.Split(raw[:len(raw)-1], []byte{0}) {
		header, path, ok := bytes.Cut(row, []byte{'\t'})
		fields := strings.Fields(string(header))
		if !ok || len(fields) != 3 || fields[1] != "blob" || !objectID.MatchString(fields[2]) || !utf8.Valid(path) || len(path) == 0 {
			return Snapshot{}, fmt.Errorf("unsupported Git inventory entry")
		}
		if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" {
			return Snapshot{}, fmt.Errorf("unsupported Git path mode")
		}
		entries = append(entries, Entry{Path: string(path), Mode: fields[0], Blob: fields[2]})
	}
	if len(entries) > 100000 {
		return Snapshot{}, fmt.Errorf("Git inventory size limit")
	}
	// A single cat-file batch avoids one process per file. No candidate commands,
	// filters, hooks, config modules, package metadata or test collectors execute.
	var request strings.Builder
	for _, e := range entries {
		request.WriteString(e.Blob + "\n")
	}
	blobs, err := r.read(ctx, []byte(request.String()), "cat-file", "--batch")
	if err != nil {
		return Snapshot{}, err
	}
	reader := bytes.NewReader(blobs)
	for i := range entries {
		var header []byte
		for {
			c, err := reader.ReadByte()
			if err != nil {
				return Snapshot{}, fmt.Errorf("truncated blob header")
			}
			if c == '\n' {
				break
			}
			header = append(header, c)
			if len(header) > 256 {
				return Snapshot{}, fmt.Errorf("invalid blob header")
			}
		}
		var id, kind string
		var size int64
		if _, err := fmt.Sscanf(string(header), "%s %s %d", &id, &kind, &size); err != nil || id != entries[i].Blob || kind != "blob" || size < 0 || size > maxGitOutput {
			return Snapshot{}, fmt.Errorf("invalid blob response")
		}
		content := make([]byte, int(size))
		if _, err := io.ReadFull(reader, content); err != nil {
			return Snapshot{}, fmt.Errorf("truncated blob")
		}
		end, err := reader.ReadByte()
		if err != nil || end != '\n' {
			return Snapshot{}, fmt.Errorf("truncated blob delimiter")
		}
		entries[i].ContentDigest = Hash("blob", content)
	}
	if reader.Len() != 0 {
		return Snapshot{}, fmt.Errorf("unexpected blob response")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for i, e := range entries {
		if i > 0 && e.Path == entries[i-1].Path {
			return Snapshot{}, fmt.Errorf("duplicate Git path")
		}
	}
	return Snapshot{Commit: commit, Tree: tree, Entries: entries, ManifestDigest: digest("tree-manifest", entries)}, nil
}

func (r *Repository) blob(ctx context.Context, s Snapshot, path string) ([]byte, error) {
	for _, e := range s.Entries {
		if e.Path == path {
			if e.Mode != "100644" && e.Mode != "100755" {
				return nil, fmt.Errorf("policy blob must be a regular file")
			}
			return r.read(ctx, nil, "cat-file", "blob", e.Blob)
		}
	}
	return nil, fmt.Errorf("required trusted inventory file missing: %s", path)
}

func (r *Repository) ancestor(ctx context.Context, base, head string) error {
	_, err := r.read(ctx, nil, "merge-base", "--is-ancestor", base, head)
	if err != nil {
		return fmt.Errorf("base/source is not an ancestor of candidate")
	}
	return nil
}
