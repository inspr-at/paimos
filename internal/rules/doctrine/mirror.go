// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const mirrorMarker = ".aeon-commit"

// mirrorReader is a bounded snapshot of one immutable, host-published tree.
// It has no network client or credential. The marker is host attestation of
// the archive's commit; blob identities are computed from the archived bytes.
type mirrorReader struct {
	repository string
	commit     string
	entries    []Entry
	blobs      map[string][]byte
}

func openMirror(dir, repository string, checks ...func(*os.File) bool) (*os.Root, string, error) {
	if !filepath.IsAbs(dir) || !repositoryPattern.MatchString(repository) {
		return nil, "", ErrCredential
	}
	base, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", gitFail("the host mirror is unavailable")
	}
	defer base.Close()
	root, err := base.OpenRoot(repository)
	if err != nil {
		return nil, "", gitFail("the host mirror repository is unavailable")
	}
	directory, err := openMirrorDirectory(root, ".", checks...)
	if err != nil {
		root.Close()
		return nil, "", err
	}
	directory.Close()
	commit, err := mirrorCommit(root, checks...)
	if err != nil {
		root.Close()
		return nil, "", err
	}
	return root, commit, nil
}

func mirrorCommit(root *os.Root, checks ...func(*os.File) bool) (string, error) {
	f, err := openMirrorFile(root, mirrorMarker, checks...)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 42))
	commit := strings.TrimSuffix(string(raw), "\n")
	if err != nil || !shaPattern.MatchString(commit) {
		return "", gitFail("the host mirror needs a valid full commit marker")
	}
	return commit, nil
}

func readMirror(ctx context.Context, dir, repository string, checks ...func(*os.File) bool) (*mirrorReader, error) {
	root, commit, err := openMirror(dir, repository, checks...)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	r := &mirrorReader{repository: repository, commit: commit, blobs: map[string][]byte{}}
	total, rows := 0, 0
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 32 {
			return gitFail("the host mirror tree is too deep")
		}
		f, err := openMirrorDirectory(root, path, checks...)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			children, err := f.ReadDir(128)
			if err != nil && err != io.EOF {
				return gitFail("the host mirror tree could not be read")
			}
			for _, child := range children {
				if err := ctx.Err(); err != nil {
					return err
				}
				rows++
				if rows > maxTreeRows {
					return gitFail("the host mirror has too many files")
				}
				name := child.Name()
				if path != "." {
					name = path + "/" + name
				}
				if len(name) > 300 {
					return gitFail("the host mirror path is too long")
				}
				if child.Type()&os.ModeSymlink != 0 {
					return gitFail("host mirror symlinks are refused")
				}
				if name == mirrorMarker {
					continue
				}
				if name == ".git" {
					return gitFail("the host mirror must be an exported tree without git metadata")
				}
				if child.IsDir() {
					if err := walk(name, depth+1); err != nil {
						return err
					}
					continue
				}
				blob, err := openMirrorFile(root, name, checks...)
				if err != nil {
					return err
				}
				info, err := blob.Stat()
				if err != nil || info.Size() > maxGuardFile || info.Size() > int64(maxGuardTotal-total) {
					blob.Close()
					return gitFail("the host mirror exceeds the private tree size bounds")
				}
				raw, err := io.ReadAll(io.LimitReader(blob, int64(min(maxGuardFile, maxGuardTotal-total))+1))
				blob.Close()
				if err != nil || len(raw) > maxGuardFile || len(raw) > maxGuardTotal-total {
					return gitFail("the host mirror exceeds the private tree size bounds")
				}
				total += len(raw)
				sha := BlobSHA(raw)
				r.entries = append(r.entries, Entry{Path: name, SHA: sha, Size: len(raw)})
				r.blobs[sha] = raw
			}
			if err == io.EOF {
				return nil
			}
		}
	}
	if err := walk(".", 0); err != nil {
		return nil, err
	}
	final, err := mirrorCommit(root, checks...)
	if err != nil || final != commit {
		return nil, gitFail("the host mirror changed during the read")
	}
	return r, nil
}

func (r *mirrorReader) Commit(ctx context.Context, repository, ref string) (Commit, error) {
	if err := ctx.Err(); err != nil {
		return Commit{}, err
	}
	if repository != r.repository || (ref != "main" && ref != r.commit) {
		return Commit{}, gitFail("the requested commit is not the host mirror commit")
	}
	return Commit{SHA: r.commit}, nil
}

func (r *mirrorReader) Tree(ctx context.Context, repository, commit string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if repository != r.repository || commit != r.commit {
		return nil, gitFail("the pinned commit is not the host mirror commit; reindex the source")
	}
	return slices.Clone(r.entries), nil
}

func (r *mirrorReader) Blob(ctx context.Context, repository, sha string, size int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, ok := r.blobs[sha]
	if !ok || repository != r.repository || size != len(raw) {
		return nil, gitFail("the host mirror blob is unavailable")
	}
	return slices.Clone(raw), nil
}
