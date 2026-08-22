// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- Git SHA-1 object identity, not a security signature.
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxAgentRunWorktreePaths  = 10_000
	maxAgentRunWorktreeBytes  = int64(64 << 20)
	maxAgentRunWorktreePath   = 4 << 10
	maxAgentRunWorktreeDepth  = 8
	agentRunWorktreeDeadline  = 10 * time.Second
	agentRunCommitProofPaths  = 1_000_000
	agentRunCommitProofLimit  = 30 * time.Second
	maxAgentRunGitBinaryBytes = int64(64 << 20)
)

var (
	errAgentRunWorktreeEvidence       = errors.New("implementation result binding unavailable")
	errAgentRunWorktreeLimit          = fmt.Errorf("%w: resource limit exceeded", errAgentRunWorktreeEvidence)
	errAgentRunWorktreeChangedInTests = errors.New("worktree changed during tests")
)

// agentRunWorktreeSnapshot is intentionally opaque. Paths, file contents, and
// symlink targets are consumed only by SHA-256 and never returned or logged.
type agentRunWorktreeSnapshot struct {
	commitSHA string
	digest    [sha256.Size]byte
}

// agentRunWorktreeCapture freezes the trusted Git executable and physical
// repository top before provider code runs. Later snapshots revalidate both;
// PATH and repository-root changes can therefore only fail closed.
type agentRunWorktreeCapture struct {
	gitPath         string
	gitInfo         os.FileInfo
	gitDigest       [sha256.Size]byte
	top             string
	topInfo         os.FileInfo
	executionRoot   string
	executionInfo   os.FileInfo
	gitMarkerInfo   os.FileInfo
	gitMarkerDigest [sha256.Size]byte
}

func (s agentRunWorktreeSnapshot) equal(other agentRunWorktreeSnapshot) bool {
	return s.digest == other.digest
}

type agentRunHashBudget struct {
	remaining int64
	paths     int
	exhausted bool
}

func newAgentRunHashBudget() *agentRunHashBudget {
	return &agentRunHashBudget{remaining: maxAgentRunWorktreeBytes}
}

func (b *agentRunHashBudget) consume(n int) error {
	if n < 0 || int64(n) > b.remaining {
		b.exhausted = true
		return errAgentRunWorktreeLimit
	}
	b.remaining -= int64(n)
	return nil
}

func (b *agentRunHashBudget) consumePath(path []byte) error {
	if b.paths >= maxAgentRunWorktreePaths || len(path) == 0 || len(path) > maxAgentRunWorktreePath {
		b.exhausted = true
		return errAgentRunWorktreeLimit
	}
	if err := b.consume(len(path)); err != nil {
		return err
	}
	b.paths++
	return nil
}

func newAgentRunWorktreeCapture(parent context.Context, requestedRoot string) (*agentRunWorktreeCapture, error) {
	ctx, cancel := context.WithTimeout(parent, agentRunWorktreeDeadline)
	defer cancel()

	gitPath, gitDigest, gitInfo, err := resolveAgentRunGitExecutable(ctx)
	if err != nil {
		return nil, errAgentRunWorktreeEvidence
	}
	requested, candidateTop, requestedInfo, topInfo, markerInfo, markerDigest, err := findAgentRunRepositoryTop(requestedRoot)
	if err != nil {
		return nil, errAgentRunWorktreeEvidence
	}
	capture := &agentRunWorktreeCapture{
		gitPath: gitPath, gitInfo: gitInfo, gitDigest: gitDigest,
		top: candidateTop, topInfo: topInfo, executionRoot: requested, executionInfo: requestedInfo,
		gitMarkerInfo: markerInfo, gitMarkerDigest: markerDigest,
	}
	top, err := agentRunGitOneLine(ctx, capture, requested, candidateTop, "rev-parse", "--show-toplevel")
	if err != nil || !sameAgentRunDirectory(top, topInfo) || !sameAgentRunRequestedDirectory(candidateTop, requested, requestedInfo) {
		return nil, errAgentRunWorktreeEvidence
	}
	return capture, nil
}

func resolveAgentRunGitExecutable(ctx context.Context) (string, [sha256.Size]byte, os.FileInfo, error) {
	var zero [sha256.Size]byte
	lookedUp, err := exec.LookPath("git")
	if err != nil {
		return "", zero, nil, errAgentRunWorktreeEvidence
	}
	initial, initialDigest, initialInfo, err := inspectAgentRunGitExecutableCandidate(ctx, lookedUp)
	if err != nil {
		// Never silently skip a provider-writable executable that PATH selected.
		return "", zero, nil, errAgentRunWorktreeEvidence
	}
	if agentRunGitExecutableRunsPinned(ctx, initial) {
		return initial, initialDigest, initialInfo, nil
	}

	// Some platform launchers (notably /usr/bin/git on macOS) dispatch another
	// Git through PATH. Find and freeze the first later executable that resolves
	// to a non-mutable store and runs with PATH restricted to that store.
	seen := map[string]bool{initial: true}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory == "" {
			directory = "."
		}
		candidate, digest, info, candidateErr := inspectAgentRunGitExecutableCandidate(ctx, filepath.Join(directory, "git"))
		if candidateErr != nil || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if agentRunGitExecutableRunsPinned(ctx, candidate) {
			return candidate, digest, info, nil
		}
	}
	return "", zero, nil, errAgentRunWorktreeEvidence
}

func inspectAgentRunGitExecutableCandidate(ctx context.Context, candidate string) (string, [sha256.Size]byte, os.FileInfo, error) {
	var zero [sha256.Size]byte
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", zero, nil, errAgentRunWorktreeEvidence
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !filepath.IsAbs(resolved) {
		return "", zero, nil, errAgentRunWorktreeEvidence
	}
	digest, info, err := hashAgentRunTrustedExecutable(ctx, resolved)
	if err != nil {
		return "", zero, nil, errAgentRunWorktreeEvidence
	}
	return resolved, digest, info, nil
}

func agentRunGitExecutableRunsPinned(ctx context.Context, path string) bool {
	// #nosec G204 G702 -- path is an absolute, no-symlink candidate whose file
	// and every ancestor were proved non-mutable by the runner before execution;
	// the only argument is this fixed validation constant.
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = agentRunScrubbedGitEnvironment(filepath.Dir(path))
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	return err == nil && len(out) <= 256 && bytes.HasPrefix(bytes.TrimSpace(out), []byte("git version "))
}

// snapshotAgentRunWorktree is a direct-call convenience. The production runner
// creates one capture before provider execution and reuses it for all snapshots.
func snapshotAgentRunWorktree(parent context.Context, repoRoot string) (agentRunWorktreeSnapshot, error) {
	capture, err := newAgentRunWorktreeCapture(parent, repoRoot)
	if err != nil {
		return agentRunWorktreeSnapshot{}, errAgentRunWorktreeEvidence
	}
	return capture.snapshot(parent)
}

// snapshot constructs a canonical digest from actual filesystem nodes. Git is
// used only to discover HEAD, stage-0 index paths, and repository-.gitignore-
// only untracked paths; attributes, filters, diff drivers, and presentation
// never participate in evidence.
func (c *agentRunWorktreeCapture) snapshot(parent context.Context) (agentRunWorktreeSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, agentRunWorktreeDeadline)
	defer cancel()
	if c == nil || c.revalidate(ctx) != nil {
		return agentRunWorktreeSnapshot{}, errAgentRunWorktreeEvidence
	}
	budget := newAgentRunHashBudget()
	visited := []os.FileInfo{c.topInfo}
	digest, head, err := hashAgentRunRawRepository(ctx, c, c.top, budget, 0, &visited)
	if err != nil {
		return agentRunWorktreeSnapshot{}, classifyAgentRunWorktreeSnapshotError(err, budget)
	}
	return agentRunWorktreeSnapshot{commitSHA: head, digest: digest}, nil
}

func classifyAgentRunWorktreeSnapshotError(err error, budget *agentRunHashBudget) error {
	if (budget != nil && budget.exhausted) || errors.Is(err, errAgentRunWorktreeLimit) {
		return errAgentRunWorktreeLimit
	}
	return errAgentRunWorktreeEvidence
}

func (c *agentRunWorktreeCapture) revalidate(ctx context.Context) error {
	gitDigest, gitInfo, err := hashAgentRunTrustedExecutable(ctx, c.gitPath)
	if err != nil || !os.SameFile(c.gitInfo, gitInfo) || c.gitInfo.Mode() != gitInfo.Mode() ||
		c.gitInfo.Size() != gitInfo.Size() || c.gitInfo.ModTime() != gitInfo.ModTime() || gitDigest != c.gitDigest {
		return errAgentRunWorktreeEvidence
	}
	topInfo, err := os.Stat(c.top)
	if err != nil || !topInfo.IsDir() || !os.SameFile(c.topInfo, topInfo) {
		return errAgentRunWorktreeEvidence
	}
	executionInfo, err := os.Stat(c.executionRoot)
	if err != nil || !executionInfo.IsDir() || !os.SameFile(c.executionInfo, executionInfo) ||
		!sameAgentRunRequestedDirectory(c.top, c.executionRoot, c.executionInfo) {
		return errAgentRunWorktreeEvidence
	}
	markerInfo, markerDigest, err := inspectAgentRunGitMarker(filepath.Join(c.top, ".git"))
	if err != nil || !os.SameFile(c.gitMarkerInfo, markerInfo) || c.gitMarkerInfo.Mode() != markerInfo.Mode() ||
		(c.gitMarkerInfo.Mode().IsRegular() && markerDigest != c.gitMarkerDigest) {
		return errAgentRunWorktreeEvidence
	}
	top, err := agentRunGitOneLine(ctx, c, c.top, c.top, "rev-parse", "--show-toplevel")
	if err != nil || !sameAgentRunDirectory(top, c.topInfo) {
		return errAgentRunWorktreeEvidence
	}
	return nil
}

func hashAgentRunTrustedExecutable(ctx context.Context, path string) ([sha256.Size]byte, os.FileInfo, error) {
	var zero [sha256.Size]byte
	if ctx.Err() != nil || !filepath.IsAbs(path) {
		return zero, nil, errAgentRunWorktreeEvidence
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || agentRunPathMutableByRunner(current, info) {
			return zero, nil, errAgentRunWorktreeEvidence
		}
		if current == path {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return zero, nil, errAgentRunWorktreeEvidence
			}
		} else if !info.IsDir() {
			return zero, nil, errAgentRunWorktreeEvidence
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	file, err := os.Open(path) // #nosec G304 -- absolute executable selected before provider execution.
	if err != nil {
		return zero, nil, errAgentRunWorktreeEvidence
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o111 == 0 {
		return zero, nil, errAgentRunWorktreeEvidence
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(file, maxAgentRunGitBinaryBytes+1))
	if err != nil || n > maxAgentRunGitBinaryBytes || ctx.Err() != nil {
		return zero, nil, errAgentRunWorktreeEvidence
	}
	finalInfo, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(openedInfo, finalInfo) || !os.SameFile(openedInfo, pathInfo) ||
		openedInfo.Mode() != finalInfo.Mode() || openedInfo.Mode() != pathInfo.Mode() ||
		openedInfo.Size() != finalInfo.Size() || openedInfo.Size() != pathInfo.Size() ||
		openedInfo.ModTime() != finalInfo.ModTime() || openedInfo.ModTime() != pathInfo.ModTime() {
		return zero, nil, errAgentRunWorktreeEvidence
	}
	copy(zero[:], digest.Sum(nil))
	return zero, openedInfo, nil
}

func findAgentRunRepositoryTop(requestedRoot string) (string, string, os.FileInfo, os.FileInfo, os.FileInfo, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	requested, err := filepath.Abs(requestedRoot)
	if err != nil {
		return "", "", nil, nil, nil, zero, errAgentRunWorktreeEvidence
	}
	requested, err = filepath.EvalSymlinks(requested)
	if err != nil {
		return "", "", nil, nil, nil, zero, errAgentRunWorktreeEvidence
	}
	requestedInfo, err := os.Stat(requested)
	if err != nil || !requestedInfo.IsDir() {
		return "", "", nil, nil, nil, zero, errAgentRunWorktreeEvidence
	}
	for candidate := requested; ; candidate = filepath.Dir(candidate) {
		markerInfo, markerDigest, markerErr := inspectAgentRunGitMarker(filepath.Join(candidate, ".git"))
		if markerErr == nil {
			topInfo, statErr := os.Stat(candidate)
			if statErr != nil || !topInfo.IsDir() || !sameAgentRunRequestedDirectory(candidate, requested, requestedInfo) {
				return "", "", nil, nil, nil, zero, errAgentRunWorktreeEvidence
			}
			return requested, candidate, requestedInfo, topInfo, markerInfo, markerDigest, nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", "", nil, nil, nil, zero, errAgentRunWorktreeEvidence
		}
	}
}

func inspectAgentRunGitMarker(path string) (os.FileInfo, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return nil, zero, errAgentRunWorktreeEvidence
	}
	if info.IsDir() {
		return info, zero, nil
	}
	if info.Size() < 0 || info.Size() > maxAgentRunWorktreePath {
		return nil, zero, errAgentRunWorktreeEvidence
	}
	file, err := os.Open(path) // #nosec G304 -- fixed .git marker below the selected repository top.
	if err != nil {
		return nil, zero, errAgentRunWorktreeEvidence
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, zero, errAgentRunWorktreeEvidence
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxAgentRunWorktreePath+1))
	finalInfo, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || len(contents) > maxAgentRunWorktreePath ||
		!os.SameFile(openedInfo, finalInfo) || !os.SameFile(openedInfo, pathInfo) ||
		openedInfo.Mode() != finalInfo.Mode() || openedInfo.Mode() != pathInfo.Mode() ||
		openedInfo.Size() != finalInfo.Size() || openedInfo.ModTime() != finalInfo.ModTime() {
		return nil, zero, errAgentRunWorktreeEvidence
	}
	return openedInfo, sha256.Sum256(contents), nil
}

func sameAgentRunDirectory(path string, expected os.FileInfo) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	info, err := os.Stat(resolved)
	return err == nil && info.IsDir() && os.SameFile(expected, info)
}

func sameAgentRunRequestedDirectory(top, requested string, expected os.FileInfo) bool {
	rel, err := filepath.Rel(top, requested)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if rel == "." {
		info, statErr := os.Stat(top)
		return statErr == nil && info.IsDir() && os.SameFile(expected, info)
	}
	if !validAgentRunRelativePath([]byte(filepath.ToSlash(rel))) {
		return false
	}
	directory, err := openAgentRunDirectory(top, rel)
	if err != nil {
		return false
	}
	defer directory.Close()
	info, err := directory.Stat()
	return err == nil && info.IsDir() && os.SameFile(expected, info)
}

type agentRunTrackedEntry struct {
	path         []byte
	indexMode    uint32
	indexOID     string
	indexPresent bool
	headMode     uint32
	headOID      string
	headPresent  bool
}

type agentRunNodeCandidate struct {
	agentRunTrackedEntry
	untracked bool
	policy    bool
}

func hashAgentRunRawRepository(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot string, budget *agentRunHashBudget, depth int, visited *[]os.FileInfo) ([sha256.Size]byte, string, error) {
	var zero [sha256.Size]byte
	if depth > maxAgentRunWorktreeDepth {
		budget.exhausted = true
		return zero, "", errAgentRunWorktreeLimit
	}
	if ctx.Err() != nil {
		return zero, "", errAgentRunWorktreeEvidence
	}
	head, err := agentRunGitOneLine(ctx, capture, repoRoot, repoRoot, "rev-parse", "--verify", "HEAD")
	if err != nil || !agentRunHexOID(head) {
		return zero, "", errAgentRunWorktreeEvidence
	}
	indexEntries, err := listAgentRunIndexEntries(ctx, capture, repoRoot)
	if err != nil {
		return zero, "", err
	}
	headEntries, err := listAgentRunHeadEntries(ctx, capture, repoRoot, head)
	if err != nil {
		return zero, "", err
	}
	trackedEntries, err := mergeAgentRunTrackedEntries(indexEntries, headEntries)
	if err != nil {
		return zero, "", err
	}
	untrackedPaths, err := listAgentRunUntrackedPaths(ctx, capture, repoRoot, maxAgentRunWorktreePaths)
	if err != nil {
		return zero, "", err
	}
	policyPaths, err := listAgentRunUntrackedIgnorePolicyPaths(ctx, capture, repoRoot, maxAgentRunWorktreePaths)
	if err != nil {
		return zero, "", err
	}
	candidates := mergeAgentRunNodeCandidates(trackedEntries, untrackedPaths, policyPaths)

	digest := sha256.New()
	writeAgentRunHashRecord(digest, []byte("paimos.agent-run.raw-repository.v3"))
	writeAgentRunHashRecord(digest, []byte(head))
	emitted := uint64(0)
	coveredPrefixes := make([][]byte, 0, 4)
	for _, candidate := range candidates {
		if agentRunPathCovered(candidate.path, coveredPrefixes) {
			continue
		}
		nodeDigest, include, coversDescendants, hashErr := hashAgentRunCandidate(ctx, capture, budget, repoRoot, candidate, depth, visited)
		if hashErr != nil {
			return zero, "", hashErr
		}
		if !include {
			continue
		}
		if err := budget.consumePath(candidate.path); err != nil {
			return zero, "", err
		}
		writeAgentRunHashRecord(digest, []byte("node"))
		writeAgentRunHashRecord(digest, candidate.path)
		writeAgentRunHashRecord(digest, nodeDigest[:])
		emitted++
		if coversDescendants {
			coveredPrefixes = append(coveredPrefixes, bytes.Clone(candidate.path))
		}
	}
	writeAgentRunHashRecord(digest, []byte("node-count"))
	writeAgentRunHashUint64(digest, emitted)

	verifiedIndex, err := listAgentRunIndexEntries(ctx, capture, repoRoot)
	if err != nil {
		return zero, "", err
	}
	if !sameAgentRunTrackedEntries(indexEntries, verifiedIndex) {
		return zero, "", errAgentRunWorktreeEvidence
	}
	verifiedUntracked, err := listAgentRunUntrackedPaths(ctx, capture, repoRoot, maxAgentRunWorktreePaths)
	if err != nil {
		return zero, "", err
	}
	if !sameAgentRunPaths(untrackedPaths, verifiedUntracked) {
		return zero, "", errAgentRunWorktreeEvidence
	}
	verifiedPolicyPaths, err := listAgentRunUntrackedIgnorePolicyPaths(ctx, capture, repoRoot, maxAgentRunWorktreePaths)
	if err != nil {
		return zero, "", err
	}
	if !sameAgentRunPaths(policyPaths, verifiedPolicyPaths) {
		return zero, "", errAgentRunWorktreeEvidence
	}
	verifiedHead, err := agentRunGitOneLine(ctx, capture, repoRoot, repoRoot, "rev-parse", "--verify", "HEAD")
	if err != nil || verifiedHead != head {
		return zero, "", errAgentRunWorktreeEvidence
	}
	var sum [sha256.Size]byte
	copy(sum[:], digest.Sum(nil))
	return sum, head, nil
}

func listAgentRunIndexEntries(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot string) ([]agentRunTrackedEntry, error) {
	return listAgentRunIndexEntriesLimit(ctx, capture, repoRoot, maxAgentRunWorktreePaths)
}

func listAgentRunIndexEntriesLimit(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot string, limit int) ([]agentRunTrackedEntry, error) {
	entries := make([]agentRunTrackedEntry, 0, 256)
	err := readAgentRunGitNUL(ctx, capture, repoRoot, repoRoot, maxAgentRunWorktreePath+128, func(record []byte) error {
		tab := bytes.IndexByte(record, '\t')
		if tab <= 0 {
			return errAgentRunWorktreeEvidence
		}
		header := strings.Fields(string(record[:tab]))
		if len(header) != 4 || header[0] != "H" || header[3] != "0" || !agentRunHexOID(header[2]) {
			return errAgentRunWorktreeEvidence
		}
		mode, parseErr := strconv.ParseUint(header[1], 8, 32)
		pathBytes := bytes.Clone(record[tab+1:])
		if len(pathBytes) > maxAgentRunWorktreePath {
			return errAgentRunWorktreeLimit
		}
		if parseErr != nil || !validAgentRunIndexMode(uint32(mode)) || !validAgentRunRelativePath(pathBytes) {
			return errAgentRunWorktreeEvidence
		}
		if len(entries) > 0 && bytes.Compare(entries[len(entries)-1].path, pathBytes) >= 0 {
			return errAgentRunWorktreeEvidence
		}
		entries = append(entries, agentRunTrackedEntry{
			path:         pathBytes,
			indexMode:    uint32(mode),
			indexOID:     header[2],
			indexPresent: true,
		})
		if len(entries) > limit {
			return errAgentRunWorktreeLimit
		}
		return nil
	}, "ls-files", "-v", "--stage", "-z", "--cached", "--")
	if err != nil {
		if errors.Is(err, errAgentRunWorktreeLimit) {
			return nil, errAgentRunWorktreeLimit
		}
		return nil, errAgentRunWorktreeEvidence
	}
	return entries, nil
}

func listAgentRunHeadEntries(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot, head string) ([]agentRunTrackedEntry, error) {
	return listAgentRunHeadEntriesLimit(ctx, capture, repoRoot, head, maxAgentRunWorktreePaths)
}

func listAgentRunHeadEntriesLimit(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot, head string, limit int) ([]agentRunTrackedEntry, error) {
	entries := make([]agentRunTrackedEntry, 0, 256)
	err := readAgentRunGitNUL(ctx, capture, repoRoot, repoRoot, maxAgentRunWorktreePath+128, func(record []byte) error {
		tab := bytes.IndexByte(record, '\t')
		if tab <= 0 {
			return errAgentRunWorktreeEvidence
		}
		header := strings.Fields(string(record[:tab]))
		if len(header) != 3 || !agentRunHexOID(header[2]) {
			return errAgentRunWorktreeEvidence
		}
		mode, parseErr := strconv.ParseUint(header[0], 8, 32)
		parsedMode := uint32(mode)
		if parseErr != nil || !validAgentRunIndexMode(parsedMode) ||
			(parsedMode == 0o160000 && header[1] != "commit") || (parsedMode != 0o160000 && header[1] != "blob") {
			return errAgentRunWorktreeEvidence
		}
		pathBytes := bytes.Clone(record[tab+1:])
		if len(pathBytes) > maxAgentRunWorktreePath {
			return errAgentRunWorktreeLimit
		}
		if !validAgentRunRelativePath(pathBytes) || (len(entries) > 0 && bytes.Compare(entries[len(entries)-1].path, pathBytes) >= 0) {
			return errAgentRunWorktreeEvidence
		}
		entries = append(entries, agentRunTrackedEntry{
			path:        pathBytes,
			headMode:    parsedMode,
			headOID:     header[2],
			headPresent: true,
		})
		if len(entries) > limit {
			return errAgentRunWorktreeLimit
		}
		return nil
	}, "ls-tree", "-r", "-z", "--full-tree", head, "--")
	if err != nil {
		if errors.Is(err, errAgentRunWorktreeLimit) {
			return nil, errAgentRunWorktreeLimit
		}
		return nil, errAgentRunWorktreeEvidence
	}
	return entries, nil
}

func mergeAgentRunTrackedEntries(indexEntries, headEntries []agentRunTrackedEntry) ([]agentRunTrackedEntry, error) {
	merged := make([]agentRunTrackedEntry, 0, len(indexEntries)+len(headEntries))
	i, j := 0, 0
	for i < len(indexEntries) || j < len(headEntries) {
		switch {
		case j >= len(headEntries):
			merged = append(merged, indexEntries[i])
			i++
		case i >= len(indexEntries):
			merged = append(merged, headEntries[j])
			j++
		default:
			comparison := bytes.Compare(indexEntries[i].path, headEntries[j].path)
			if comparison < 0 {
				merged = append(merged, indexEntries[i])
				i++
			} else if comparison > 0 {
				merged = append(merged, headEntries[j])
				j++
			} else {
				entry := indexEntries[i]
				entry.headMode = headEntries[j].headMode
				entry.headOID = headEntries[j].headOID
				entry.headPresent = true
				merged = append(merged, entry)
				i++
				j++
			}
		}
		if len(merged) > maxAgentRunWorktreePaths {
			return nil, errAgentRunWorktreeLimit
		}
	}
	return merged, nil
}

func mergeAgentRunNodeCandidates(tracked []agentRunTrackedEntry, untracked, policy [][]byte) []agentRunNodeCandidate {
	byPath := make(map[string]agentRunNodeCandidate, len(tracked)+len(untracked)+len(policy))
	for _, entry := range tracked {
		byPath[string(entry.path)] = agentRunNodeCandidate{agentRunTrackedEntry: entry}
	}
	for _, pathBytes := range untracked {
		key := string(pathBytes)
		candidate := byPath[key]
		if candidate.path == nil {
			candidate.path = pathBytes
		}
		candidate.untracked = true
		byPath[key] = candidate
	}
	for _, pathBytes := range policy {
		key := string(pathBytes)
		candidate := byPath[key]
		if candidate.path == nil {
			candidate.path = pathBytes
		}
		candidate.policy = true
		byPath[key] = candidate
	}
	candidates := make([]agentRunNodeCandidate, 0, len(byPath))
	for _, candidate := range byPath {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return bytes.Compare(candidates[i].path, candidates[j].path) < 0
	})
	return candidates
}

func agentRunPathBelow(pathBytes, directory []byte) bool {
	return len(pathBytes) > len(directory) && bytes.HasPrefix(pathBytes, directory) && pathBytes[len(directory)] == '/'
}

func agentRunPathCovered(pathBytes []byte, directories [][]byte) bool {
	for _, directory := range directories {
		if agentRunPathBelow(pathBytes, directory) {
			return true
		}
	}
	return false
}

func listAgentRunUntrackedPaths(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot string, limit int) ([][]byte, error) {
	if limit < 0 {
		return nil, errAgentRunWorktreeEvidence
	}
	paths := make([][]byte, 0, 64)
	var previousRecord []byte
	err := readAgentRunGitNUL(ctx, capture, repoRoot, repoRoot, maxAgentRunWorktreePath+1, func(record []byte) error {
		if previousRecord != nil && bytes.Compare(previousRecord, record) >= 0 {
			return errAgentRunWorktreeEvidence
		}
		previousRecord = bytes.Clone(record)
		pathBytes, directoryMarker, valid := normalizeAgentRunUntrackedPath(record)
		if !valid {
			return errAgentRunWorktreeEvidence
		}
		if directoryMarker {
			// git ls-files --others emits an embedded repository as "name/".
			// Accept only that single directory suffix, and prove the normalized
			// path is currently a real no-follow directory before recursive hashing.
			directory, openErr := openAgentRunDirectory(repoRoot, filepath.FromSlash(string(pathBytes)))
			if openErr != nil {
				return errAgentRunWorktreeEvidence
			}
			info, statErr := directory.Stat()
			closeErr := directory.Close()
			if statErr != nil || closeErr != nil || !info.IsDir() {
				return errAgentRunWorktreeEvidence
			}
		}
		paths = append(paths, pathBytes)
		if len(paths) > limit {
			return errAgentRunWorktreeLimit
		}
		return nil
	}, "ls-files", "--others", "--exclude-per-directory=.gitignore", "-z", "--")
	if err != nil {
		if errors.Is(err, errAgentRunWorktreeLimit) {
			return nil, errAgentRunWorktreeLimit
		}
		return nil, errAgentRunWorktreeEvidence
	}
	sort.Slice(paths, func(i, j int) bool { return bytes.Compare(paths[i], paths[j]) < 0 })
	for i := 1; i < len(paths); i++ {
		if bytes.Equal(paths[i-1], paths[i]) {
			return nil, errAgentRunWorktreeEvidence
		}
	}
	return paths, nil
}

func normalizeAgentRunUntrackedPath(record []byte) ([]byte, bool, bool) {
	if validAgentRunRelativePath(record) {
		return bytes.Clone(record), false, true
	}
	if len(record) < 2 || record[len(record)-1] != '/' || record[len(record)-2] == '/' {
		return nil, false, false
	}
	pathBytes := record[:len(record)-1]
	if !validAgentRunRelativePath(pathBytes) {
		return nil, false, false
	}
	return bytes.Clone(pathBytes), true, true
}

// listAgentRunUntrackedIgnorePolicyPaths binds every repository .gitignore
// policy file, including policies hidden by their own or a parent policy. No
// exclude option is supplied, so repository, info, and global excludes cannot
// suppress these discovery records.
func listAgentRunUntrackedIgnorePolicyPaths(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot string, limit int) ([][]byte, error) {
	if limit < 0 {
		return nil, errAgentRunWorktreeEvidence
	}
	paths := make([][]byte, 0, 16)
	err := readAgentRunGitNUL(ctx, capture, repoRoot, repoRoot, maxAgentRunWorktreePath+1, func(record []byte) error {
		pathBytes := bytes.Clone(record)
		if !validAgentRunRelativePath(pathBytes) || (len(paths) > 0 && bytes.Compare(paths[len(paths)-1], pathBytes) >= 0) {
			return errAgentRunWorktreeEvidence
		}
		paths = append(paths, pathBytes)
		if len(paths) > limit {
			return errAgentRunWorktreeLimit
		}
		return nil
	}, "ls-files", "--others", "-z", "--", ".gitignore", ":(glob)**/.gitignore")
	if err != nil {
		if errors.Is(err, errAgentRunWorktreeLimit) {
			return nil, errAgentRunWorktreeLimit
		}
		return nil, errAgentRunWorktreeEvidence
	}
	return paths, nil
}

func readAgentRunGitNUL(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot, safeRoot string, maxRecord int, consume func([]byte) error, args ...string) error {
	cmd := agentRunGitCommand(ctx, capture, repoRoot, safeRoot, args...)
	cmd.Stderr = io.Discard
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return errAgentRunWorktreeEvidence
	}
	if err = cmd.Start(); err != nil {
		return errAgentRunWorktreeEvidence
	}
	reader := bufio.NewReaderSize(pipe, 32<<10)
	for {
		record, readErr := reader.ReadSlice(0)
		if errors.Is(readErr, bufio.ErrBufferFull) || len(record) > maxRecord {
			err = errAgentRunWorktreeLimit
			break
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			err = errAgentRunWorktreeEvidence
			break
		}
		if len(record) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if record[len(record)-1] != 0 {
			err = errAgentRunWorktreeEvidence
			break
		}
		if consumeErr := consume(record[:len(record)-1]); consumeErr != nil {
			err = consumeErr
			break
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err != nil || waitErr != nil || ctx.Err() != nil {
		if errors.Is(err, errAgentRunWorktreeLimit) {
			return errAgentRunWorktreeLimit
		}
		return errAgentRunWorktreeEvidence
	}
	return nil
}

func agentRunGitOneLine(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot, safeRoot string, args ...string) (string, error) {
	value, err := agentRunGitOptionalOneLine(ctx, capture, repoRoot, safeRoot, args...)
	if err != nil || value == "" {
		return "", errAgentRunWorktreeEvidence
	}
	return value, nil
}

func agentRunGitOptionalOneLine(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot, safeRoot string, args ...string) (string, error) {
	cmd := agentRunGitCommand(ctx, capture, repoRoot, safeRoot, args...)
	cmd.Stderr = io.Discard
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return "", errAgentRunWorktreeEvidence
	}
	out, readErr := io.ReadAll(io.LimitReader(pipe, int64(maxAgentRunWorktreePath+1)))
	if readErr != nil || len(out) > maxAgentRunWorktreePath {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil || len(out) > maxAgentRunWorktreePath || waitErr != nil || ctx.Err() != nil {
		return "", errAgentRunWorktreeEvidence
	}
	return strings.TrimSpace(string(out)), nil
}

func agentRunGitCommand(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot, safeRoot string, args ...string) *exec.Cmd {
	base := []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.ignoreCase=false",
		"-c", "core.precomposeUnicode=false",
		"-c", "core.useReplaceRefs=false",
		"-c", "safe.directory=" + safeRoot,
		"-C", repoRoot,
	}
	// #nosec G204 -- capture.gitPath is the absolute executable frozen and
	// revalidated by agentRunWorktreeCapture; base is constant/config-neutral,
	// and every caller supplies fixed Git subcommands plus validated OIDs/paths.
	cmd := exec.CommandContext(ctx, capture.gitPath, append(base, args...)...)
	cmd.Env = agentRunScrubbedGitEnvironment(filepath.Dir(capture.gitPath))
	return cmd
}

func agentRunScrubbedGitEnvironment(pinnedPath string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, value := range os.Environ() {
		key := value
		if separator := strings.IndexByte(value, '='); separator >= 0 {
			key = value[:separator]
		}
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") || strings.EqualFold(key, "PATH") {
			continue
		}
		env = append(env, value)
	}
	return append(env,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
		"PATH="+pinnedPath,
	)
}

func agentRunHexOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func validAgentRunIndexMode(mode uint32) bool {
	return mode == 0o100644 || mode == 0o100755 || mode == 0o120000 || mode == 0o160000
}

func validAgentRunRelativePath(pathBytes []byte) bool {
	if len(pathBytes) == 0 || len(pathBytes) > maxAgentRunWorktreePath || bytes.IndexByte(pathBytes, 0) >= 0 {
		return false
	}
	rel := filepath.FromSlash(string(pathBytes))
	return !filepath.IsAbs(rel) && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && filepath.ToSlash(filepath.Clean(rel)) == string(pathBytes)
}

func sameAgentRunTrackedEntries(left, right []agentRunTrackedEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].indexMode != right[i].indexMode || left[i].indexOID != right[i].indexOID ||
			left[i].indexPresent != right[i].indexPresent || left[i].headMode != right[i].headMode ||
			left[i].headOID != right[i].headOID || left[i].headPresent != right[i].headPresent ||
			!bytes.Equal(left[i].path, right[i].path) {
			return false
		}
	}
	return true
}

func sameAgentRunPaths(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !bytes.Equal(left[i], right[i]) {
			return false
		}
	}
	return true
}

func hashAgentRunCandidate(ctx context.Context, capture *agentRunWorktreeCapture, budget *agentRunHashBudget, repoRoot string, candidate agentRunNodeCandidate, depth int, visited *[]os.FileInfo) ([sha256.Size]byte, bool, bool, error) {
	var zero [sha256.Size]byte
	if ctx.Err() != nil || !validAgentRunRelativePath(candidate.path) {
		return zero, false, false, errAgentRunWorktreeEvidence
	}
	rel := filepath.FromSlash(string(candidate.path))
	fullPath := filepath.Join(repoRoot, rel)
	info, err := os.Lstat(fullPath)
	if err != nil {
		absent, absentErr := agentRunNodeAbsent(repoRoot, rel)
		if absentErr != nil || !absent {
			return zero, false, false, errAgentRunWorktreeEvidence
		}
		if candidate.headPresent {
			node := sha256.New()
			writeAgentRunHashRecord(node, []byte("deleted"))
			copy(zero[:], node.Sum(nil))
			return zero, true, false, nil
		}
		if candidate.untracked || candidate.policy {
			return zero, false, false, errAgentRunWorktreeEvidence
		}
		// An absent index-only cacheinfo entry has no worktree source.
		return zero, false, false, nil
	}

	node := sha256.New()
	if info.IsDir() {
		include, coversDescendants, hashErr := hashAgentRunRawDirectory(ctx, capture, node, budget, repoRoot, candidate, info, depth, visited)
		if hashErr != nil || !include {
			return zero, include, coversDescendants, hashErr
		}
		copy(zero[:], node.Sum(nil))
		return zero, true, coversDescendants, nil
	}
	if err := hashAgentRunRawNode(ctx, node, budget, repoRoot, candidate.path); err != nil {
		return zero, false, false, errAgentRunWorktreeEvidence
	}
	copy(zero[:], node.Sum(nil))
	return zero, true, false, nil
}

func hashAgentRunRawNode(ctx context.Context, digest hash.Hash, budget *agentRunHashBudget, repoRoot string, pathBytes []byte) error {
	if ctx.Err() != nil {
		return errAgentRunWorktreeEvidence
	}
	if !validAgentRunRelativePath(pathBytes) {
		return errAgentRunWorktreeEvidence
	}
	rel := filepath.FromSlash(string(pathBytes))
	fullPath := filepath.Join(repoRoot, rel)
	info, err := os.Lstat(fullPath)
	if err != nil {
		return errAgentRunWorktreeEvidence
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := readAgentRunSymlink(repoRoot, rel)
		if readErr != nil || budget.consume(len(target)) != nil {
			return errAgentRunWorktreeEvidence
		}
		finalInfo, statErr := os.Lstat(fullPath)
		if statErr != nil || !os.SameFile(info, finalInfo) || finalInfo.Mode() != info.Mode() ||
			finalInfo.Size() != info.Size() || finalInfo.ModTime() != info.ModTime() {
			return errAgentRunWorktreeEvidence
		}
		writeAgentRunHashRecord(digest, []byte("symlink"))
		writeAgentRunHashUint64(digest, agentRunExecutableBit(info.Mode()))
		writeAgentRunHashRecord(digest, []byte(target))
		return nil
	}
	if info.IsDir() {
		return errAgentRunWorktreeEvidence
	}
	if !info.Mode().IsRegular() {
		return errAgentRunWorktreeEvidence
	}

	file, err := openAgentRunRegularFile(repoRoot, rel)
	if err != nil {
		return errAgentRunWorktreeEvidence
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Mode() != info.Mode() {
		return errAgentRunWorktreeEvidence
	}
	contentDigest := sha256.New()
	n, err := hashAgentRunRegularContent(ctx, contentDigest, budget, file)
	if err != nil {
		return errAgentRunWorktreeEvidence
	}
	finalInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, finalInfo) || finalInfo.Mode() != openedInfo.Mode() ||
		finalInfo.Size() != n || finalInfo.ModTime() != openedInfo.ModTime() {
		return errAgentRunWorktreeEvidence
	}
	pathInfo, err := os.Lstat(fullPath)
	if err != nil || !os.SameFile(openedInfo, pathInfo) || pathInfo.Mode() != openedInfo.Mode() ||
		pathInfo.Size() != openedInfo.Size() || pathInfo.ModTime() != openedInfo.ModTime() {
		return errAgentRunWorktreeEvidence
	}
	writeAgentRunHashRecord(digest, []byte("regular"))
	writeAgentRunHashUint64(digest, agentRunExecutableBit(openedInfo.Mode()))
	if n < 0 {
		return errAgentRunWorktreeEvidence
	}
	writeAgentRunHashUint64(digest, uint64(n))
	writeAgentRunHashRecord(digest, contentDigest.Sum(nil))
	return nil
}

func hashAgentRunRawDirectory(ctx context.Context, capture *agentRunWorktreeCapture, digest hash.Hash, budget *agentRunHashBudget, repoRoot string, candidate agentRunNodeCandidate, info os.FileInfo, depth int, visited *[]os.FileInfo) (bool, bool, error) {
	rel := filepath.FromSlash(string(candidate.path))
	fullPath := filepath.Join(repoRoot, rel)
	directory, err := openAgentRunDirectory(repoRoot, rel)
	if err != nil {
		return false, false, errAgentRunWorktreeEvidence
	}
	defer directory.Close()
	openedInfo, err := directory.Stat()
	if err != nil || !openedInfo.IsDir() || !os.SameFile(info, openedInfo) || openedInfo.Mode() != info.Mode() {
		return false, false, errAgentRunWorktreeEvidence
	}
	subTop, topErr := agentRunGitOneLine(ctx, capture, fullPath, fullPath, "rev-parse", "--show-toplevel")
	var subTopInfo os.FileInfo
	if topErr == nil {
		subTopInfo, err = os.Stat(subTop)
		if err != nil || !subTopInfo.IsDir() || !os.SameFile(openedInfo, subTopInfo) {
			topErr = errAgentRunWorktreeEvidence
		}
	}
	if topErr == nil {
		if depth >= maxAgentRunWorktreeDepth {
			budget.exhausted = true
			return false, false, errAgentRunWorktreeLimit
		}
		for _, seen := range *visited {
			if os.SameFile(seen, openedInfo) {
				return false, false, errAgentRunWorktreeEvidence
			}
		}
		*visited = append(*visited, openedInfo)
		nestedDigest, nestedHead, nestedErr := hashAgentRunRawRepository(ctx, capture, subTop, budget, depth+1, visited)
		*visited = (*visited)[:len(*visited)-1]
		if nestedErr != nil {
			return false, false, nestedErr
		}
		if !sameAgentRunDirectoryAtPath(directory, openedInfo, fullPath) {
			return false, false, errAgentRunWorktreeEvidence
		}
		writeAgentRunHashRecord(digest, []byte("repository"))
		writeAgentRunHashUint64(digest, agentRunExecutableBit(openedInfo.Mode()))
		writeAgentRunHashRecord(digest, []byte(nestedHead))
		writeAgentRunHashRecord(digest, nestedDigest[:])
		return true, true, nil
	}

	expectsGitlink := (candidate.headPresent && candidate.headMode == 0o160000) ||
		(candidate.indexPresent && candidate.indexMode == 0o160000)
	if expectsGitlink {
		names, readErr := directory.Readdirnames(1)
		if readErr == nil || len(names) != 0 || !errors.Is(readErr, io.EOF) {
			return false, false, errAgentRunWorktreeEvidence
		}
		if !sameAgentRunDirectoryAtPath(directory, openedInfo, fullPath) {
			return false, false, errAgentRunWorktreeEvidence
		}
		if !candidate.headPresent {
			// Empty/uninitialized index-only gitlinks contain no source.
			return false, false, nil
		}
		writeAgentRunHashRecord(digest, []byte("repository-uninitialized"))
		writeAgentRunHashUint64(digest, agentRunExecutableBit(openedInfo.Mode()))
		return true, false, nil
	}
	if !candidate.headPresent && !candidate.untracked && !candidate.policy {
		// An index-only directory is not source merely because cache metadata
		// made its otherwise-untracked empty directory discoverable.
		return false, false, nil
	}
	if !sameAgentRunDirectoryAtPath(directory, openedInfo, fullPath) {
		return false, false, errAgentRunWorktreeEvidence
	}
	writeAgentRunHashRecord(digest, []byte("directory"))
	writeAgentRunHashUint64(digest, agentRunExecutableBit(openedInfo.Mode()))
	return true, false, nil
}

func sameAgentRunDirectoryAtPath(directory *os.File, openedInfo os.FileInfo, fullPath string) bool {
	finalInfo, err := directory.Stat()
	if err != nil || !finalInfo.IsDir() || !os.SameFile(openedInfo, finalInfo) ||
		finalInfo.Mode() != openedInfo.Mode() || finalInfo.ModTime() != openedInfo.ModTime() {
		return false
	}
	pathInfo, err := os.Lstat(fullPath)
	return err == nil && pathInfo.IsDir() && os.SameFile(openedInfo, pathInfo) &&
		pathInfo.Mode() == openedInfo.Mode() && pathInfo.ModTime() == openedInfo.ModTime()
}

func agentRunExecutableBit(mode os.FileMode) uint64 {
	if mode.Perm()&0o111 != 0 {
		return 1
	}
	return 0
}

func hashAgentRunRegularContent(ctx context.Context, digest hash.Hash, budget *agentRunHashBudget, file *os.File) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if ctx.Err() != nil {
			return 0, errAgentRunWorktreeEvidence
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if err := budget.consume(n); err != nil {
				return 0, errAgentRunWorktreeEvidence
			}
			if _, err := digest.Write(buffer[:n]); err != nil {
				return 0, errAgentRunWorktreeEvidence
			}
			total += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil || n == 0 {
			return 0, errAgentRunWorktreeEvidence
		}
	}
}

// proveChangedCommitMatchesWorktree is the typed fallback when the richer raw
// repository snapshot exceeds its byte or path budget. It proves two narrower
// facts without consulting Git filters, attributes, status, or diff drivers:
// the selected commit has a different tree from the captured base commit, and
// every source node represented by the worktree/index is the raw source stored
// by that selected commit. The proof is streaming and deadline/path bounded, so
// a legitimately large committed implementation remains reportable.
func (c *agentRunWorktreeCapture) proveChangedCommitMatchesWorktree(parent context.Context, baseCommit, testedCommit string) error {
	if c == nil || !agentRunHexOID(baseCommit) || !agentRunHexOID(testedCommit) || baseCommit == testedCommit {
		return errAgentRunWorktreeEvidence
	}
	ctx, cancel := context.WithTimeout(parent, agentRunCommitProofLimit)
	defer cancel()
	if c.revalidate(ctx) != nil {
		return errAgentRunWorktreeEvidence
	}
	current, err := agentRunGitOneLine(ctx, c, c.top, c.top, "rev-parse", "--verify", "HEAD")
	if err != nil || current != testedCommit {
		return errAgentRunWorktreeEvidence
	}
	baseTree, err := agentRunGitOneLine(ctx, c, c.top, c.top, "rev-parse", "--verify", baseCommit+"^{tree}")
	if err != nil || !agentRunHexOID(baseTree) {
		return errAgentRunWorktreeEvidence
	}
	testedTree, err := agentRunGitOneLine(ctx, c, c.top, c.top, "rev-parse", "--verify", testedCommit+"^{tree}")
	if err != nil || !agentRunHexOID(testedTree) || testedTree == baseTree {
		return errAgentRunWorktreeEvidence
	}
	visited := []os.FileInfo{c.topInfo}
	if err := proveAgentRunRawWorktreeAtCommit(ctx, c, c.top, testedCommit, 0, &visited); err != nil {
		return errAgentRunWorktreeEvidence
	}
	if c.revalidate(ctx) != nil {
		return errAgentRunWorktreeEvidence
	}
	return nil
}

func proveAgentRunRawWorktreeAtCommit(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot, commit string, depth int, visited *[]os.FileInfo) error {
	if ctx.Err() != nil || depth > maxAgentRunWorktreeDepth || !agentRunHexOID(commit) {
		return errAgentRunWorktreeEvidence
	}
	current, err := agentRunGitOneLine(ctx, capture, repoRoot, repoRoot, "rev-parse", "--verify", "HEAD")
	if err != nil || current != commit {
		return errAgentRunWorktreeEvidence
	}
	indexEntries, err := listAgentRunIndexEntriesLimit(ctx, capture, repoRoot, agentRunCommitProofPaths)
	if err != nil {
		return errAgentRunWorktreeEvidence
	}
	headEntries, err := listAgentRunHeadEntriesLimit(ctx, capture, repoRoot, commit, agentRunCommitProofPaths)
	if err != nil || !sameAgentRunIndexAndCommit(indexEntries, headEntries) {
		return errAgentRunWorktreeEvidence
	}
	untracked, err := listAgentRunUntrackedPaths(ctx, capture, repoRoot, agentRunCommitProofPaths)
	if err != nil || len(untracked) != 0 {
		return errAgentRunWorktreeEvidence
	}
	policy, err := listAgentRunUntrackedIgnorePolicyPaths(ctx, capture, repoRoot, agentRunCommitProofPaths)
	if err != nil || len(policy) != 0 {
		return errAgentRunWorktreeEvidence
	}
	objectFormat, err := agentRunGitOneLine(ctx, capture, repoRoot, repoRoot, "rev-parse", "--show-object-format")
	if err != nil || (objectFormat != "sha1" && objectFormat != "sha256") {
		return errAgentRunWorktreeEvidence
	}
	for _, entry := range headEntries {
		if err := proveAgentRunRawNodeAtCommit(ctx, capture, repoRoot, objectFormat, entry, depth, visited); err != nil {
			return errAgentRunWorktreeEvidence
		}
	}
	verifiedIndex, err := listAgentRunIndexEntriesLimit(ctx, capture, repoRoot, agentRunCommitProofPaths)
	if err != nil || !sameAgentRunTrackedEntries(indexEntries, verifiedIndex) {
		return errAgentRunWorktreeEvidence
	}
	verifiedUntracked, err := listAgentRunUntrackedPaths(ctx, capture, repoRoot, agentRunCommitProofPaths)
	if err != nil || len(verifiedUntracked) != 0 {
		return errAgentRunWorktreeEvidence
	}
	verifiedPolicy, err := listAgentRunUntrackedIgnorePolicyPaths(ctx, capture, repoRoot, agentRunCommitProofPaths)
	if err != nil || len(verifiedPolicy) != 0 {
		return errAgentRunWorktreeEvidence
	}
	verifiedHead, err := agentRunGitOneLine(ctx, capture, repoRoot, repoRoot, "rev-parse", "--verify", "HEAD")
	if err != nil || verifiedHead != commit {
		return errAgentRunWorktreeEvidence
	}
	return nil
}

func sameAgentRunIndexAndCommit(indexEntries, headEntries []agentRunTrackedEntry) bool {
	if len(indexEntries) != len(headEntries) {
		return false
	}
	for i := range indexEntries {
		if !bytes.Equal(indexEntries[i].path, headEntries[i].path) ||
			indexEntries[i].indexMode != headEntries[i].headMode || indexEntries[i].indexOID != headEntries[i].headOID {
			return false
		}
	}
	return true
}

func proveAgentRunRawNodeAtCommit(ctx context.Context, capture *agentRunWorktreeCapture, repoRoot, objectFormat string,
	entry agentRunTrackedEntry, depth int, visited *[]os.FileInfo) error {
	if ctx.Err() != nil || !validAgentRunRelativePath(entry.path) || !entry.headPresent {
		return errAgentRunWorktreeEvidence
	}
	rel := filepath.FromSlash(string(entry.path))
	fullPath := filepath.Join(repoRoot, rel)
	info, err := os.Lstat(fullPath)
	if err != nil {
		if entry.headMode == 0o160000 {
			absent, absentErr := agentRunNodeAbsent(repoRoot, rel)
			if absentErr == nil && absent {
				return nil
			}
		}
		return errAgentRunWorktreeEvidence
	}
	switch entry.headMode {
	case 0o100644, 0o100755:
		if !info.Mode().IsRegular() || (entry.headMode == 0o100755) != (agentRunExecutableBit(info.Mode()) == 1) {
			return errAgentRunWorktreeEvidence
		}
		oid, hashErr := hashAgentRunRawGitBlob(ctx, repoRoot, rel, objectFormat, info)
		if hashErr != nil || oid != entry.headOID {
			return errAgentRunWorktreeEvidence
		}
		return nil
	case 0o120000:
		if info.Mode()&os.ModeSymlink == 0 {
			return errAgentRunWorktreeEvidence
		}
		target, readErr := readAgentRunSymlink(repoRoot, rel)
		if readErr != nil || agentRunGitBlobOID(objectFormat, []byte(target)) != entry.headOID {
			return errAgentRunWorktreeEvidence
		}
		finalInfo, statErr := os.Lstat(fullPath)
		if statErr != nil || !os.SameFile(info, finalInfo) || info.Mode() != finalInfo.Mode() ||
			info.Size() != finalInfo.Size() || info.ModTime() != finalInfo.ModTime() {
			return errAgentRunWorktreeEvidence
		}
		return nil
	case 0o160000:
		if !info.IsDir() || depth >= maxAgentRunWorktreeDepth {
			return errAgentRunWorktreeEvidence
		}
		directory, openErr := openAgentRunDirectory(repoRoot, rel)
		if openErr != nil {
			return errAgentRunWorktreeEvidence
		}
		defer directory.Close()
		openedInfo, statErr := directory.Stat()
		if statErr != nil || !openedInfo.IsDir() || !os.SameFile(info, openedInfo) {
			return errAgentRunWorktreeEvidence
		}
		subTop, topErr := agentRunGitOneLine(ctx, capture, fullPath, fullPath, "rev-parse", "--show-toplevel")
		if topErr != nil {
			names, readErr := directory.Readdirnames(1)
			if len(names) == 0 && errors.Is(readErr, io.EOF) && sameAgentRunDirectoryAtPath(directory, openedInfo, fullPath) {
				return nil
			}
			return errAgentRunWorktreeEvidence
		}
		if !sameAgentRunDirectory(subTop, openedInfo) {
			return errAgentRunWorktreeEvidence
		}
		for _, seen := range *visited {
			if os.SameFile(seen, openedInfo) {
				return errAgentRunWorktreeEvidence
			}
		}
		*visited = append(*visited, openedInfo)
		proofErr := proveAgentRunRawWorktreeAtCommit(ctx, capture, subTop, entry.headOID, depth+1, visited)
		*visited = (*visited)[:len(*visited)-1]
		if proofErr != nil || !sameAgentRunDirectoryAtPath(directory, openedInfo, fullPath) {
			return errAgentRunWorktreeEvidence
		}
		return nil
	default:
		return errAgentRunWorktreeEvidence
	}
}

func hashAgentRunRawGitBlob(ctx context.Context, repoRoot, rel, objectFormat string, expected os.FileInfo) (string, error) {
	file, err := openAgentRunRegularFile(repoRoot, rel)
	if err != nil {
		return "", errAgentRunWorktreeEvidence
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(expected, openedInfo) ||
		expected.Mode() != openedInfo.Mode() || openedInfo.Size() < 0 {
		return "", errAgentRunWorktreeEvidence
	}
	digest, err := newAgentRunGitObjectHash(objectFormat)
	if err != nil {
		return "", errAgentRunWorktreeEvidence
	}
	_, _ = fmt.Fprintf(digest, "blob %d%c", openedInfo.Size(), byte(0))
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if ctx.Err() != nil {
			return "", errAgentRunWorktreeEvidence
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			_, _ = digest.Write(buffer[:n])
			total += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || n == 0 {
			return "", errAgentRunWorktreeEvidence
		}
	}
	finalInfo, err := file.Stat()
	pathInfo, pathErr := os.Lstat(filepath.Join(repoRoot, rel))
	if err != nil || pathErr != nil || total != openedInfo.Size() || !os.SameFile(openedInfo, finalInfo) ||
		!os.SameFile(openedInfo, pathInfo) || openedInfo.Mode() != finalInfo.Mode() || openedInfo.Mode() != pathInfo.Mode() ||
		openedInfo.Size() != finalInfo.Size() || openedInfo.Size() != pathInfo.Size() ||
		openedInfo.ModTime() != finalInfo.ModTime() || openedInfo.ModTime() != pathInfo.ModTime() {
		return "", errAgentRunWorktreeEvidence
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func agentRunGitBlobOID(objectFormat string, contents []byte) string {
	digest, err := newAgentRunGitObjectHash(objectFormat)
	if err != nil {
		return ""
	}
	_, _ = fmt.Fprintf(digest, "blob %d%c", len(contents), byte(0))
	_, _ = digest.Write(contents)
	return hex.EncodeToString(digest.Sum(nil))
}

func newAgentRunGitObjectHash(objectFormat string) (hash.Hash, error) {
	switch objectFormat {
	case "sha1":
		return sha1.New(), nil // #nosec G401 -- reproduces Git's configured object identity.
	case "sha256":
		return sha256.New(), nil
	default:
		return nil, errAgentRunWorktreeEvidence
	}
}

func implementationResultDigest(runID int64, baseCommit, testedCommit, testCommand, testsSummary string, before, tested agentRunWorktreeSnapshot) string {
	digest := sha256.New()
	writeAgentRunHashRecord(digest, []byte("paimos.agent-run.implementation-result.v2"))
	writeAgentRunHashRecord(digest, []byte(strconv.FormatInt(runID, 10)))
	writeAgentRunHashRecord(digest, []byte(baseCommit))
	writeAgentRunHashRecord(digest, []byte(testedCommit))
	writeAgentRunHashRecord(digest, before.digest[:])
	writeAgentRunHashRecord(digest, tested.digest[:])
	commandDigest := sha256.Sum256([]byte(testCommand))
	writeAgentRunHashRecord(digest, commandDigest[:])
	testsDigest := sha256.Sum256([]byte(testsSummary))
	writeAgentRunHashRecord(digest, testsDigest[:])
	return hex.EncodeToString(digest.Sum(nil))
}

func writeAgentRunHashRecord(dst hash.Hash, value []byte) {
	writeAgentRunHashUint64(dst, uint64(len(value)))
	_, _ = dst.Write(value)
}

func writeAgentRunHashUint64(dst hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = dst.Write(encoded[:])
}
