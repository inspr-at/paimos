// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/inspr-at/paimos/internal/rulesimport"
)

// projectDocMaxBytes is Codex's documented default project_doc_max_bytes.
// The comparison does not read config.toml, so fallback filenames stay unused.
const projectDocMaxBytes = 32768

// Chain is the instruction files one harness would load for one launch
// directory. Paths are logical names. An empty Home skips user-level files.
type Chain struct {
	Harness    string
	Repo       string
	RepoName   string
	RepoSHA256 string
	Files      []ChainFile
	Gaps       []string
}

// ChainFile is one loaded instruction file. Text stays in memory for the
// diff and is not part of a stored or uploaded comparison.
type ChainFile struct {
	Logical string
	SHA256  string
	Bytes   int
	Text    string
}

// LoadChain reads the allowlisted instruction chain for harness (claude-code
// or codex). It does not list directories or read above the git root. Claude
// @path imports are expanded inside the given repo or home root, with a depth,
// count and byte bound. CLAUDE.local.md is read at each project directory and
// counts against the AGENTS.md fallback. Home and repo are explicit; this
// function does not look up the operator's home directory.
func LoadChain(harness, home, repo string) (Chain, error) {
	if harness != "claude-code" && harness != "codex" {
		return Chain{}, fmt.Errorf("harness must be claude-code or codex")
	}
	abs, err := cleanLaunch(repo)
	if err != nil {
		return Chain{}, err
	}
	if home != "" {
		home, err = cleanLaunch(home)
		if err != nil {
			return Chain{}, err
		}
	}
	root, err := findGitRoot(abs)
	if err != nil {
		return Chain{}, err
	}
	dirs, err := chainDirs(root, abs)
	if err != nil {
		return Chain{}, err
	}
	logicalRoot := root
	if logicalRoot == "" {
		logicalRoot = abs
	}
	b := &builder{harness: harness, repo: logicalRoot, home: home, seen: map[string]bool{}}
	if harness == "claude-code" {
		err = b.claude(home, dirs)
	} else {
		err = b.codex(home, dirs)
	}
	if err != nil {
		return Chain{}, err
	}
	name := filepath.Base(abs)
	if !repoNameOK(name) {
		name = ""
	}
	return Chain{
		Harness: harness, Repo: abs, RepoName: name, RepoSHA256: sha256Hex(abs),
		Files: b.files, Gaps: b.gaps(),
	}, nil
}

type builder struct {
	harness       string
	repo          string
	home          string
	files         []ChainFile
	used          int
	capped        bool
	omittedImport bool
	seen          map[string]bool
	importCount   int
	importBytes   int
}

func (b *builder) gaps() []string {
	gaps := []string{"managed_policy_not_read"}
	if b.omittedImport {
		gaps = append(gaps, "imports_not_followed")
	}
	gaps = append(gaps, "parents_above_repository_not_read")
	if b.harness == "codex" {
		gaps = append(gaps, "codex_fallback_filenames_not_applied")
		if b.capped {
			gaps = append(gaps, "codex_byte_cap")
		}
	}
	return gaps
}

func (b *builder) add(logical, text, sum string, size int) bool {
	if b.harness == "codex" && b.used+size > projectDocMaxBytes {
		b.capped = true
		return false
	}
	b.used += size
	b.files = append(b.files, ChainFile{Logical: logical, SHA256: sum, Bytes: size, Text: text})
	return true
}

func (b *builder) claude(home string, dirs []string) error {
	if home != "" {
		if _, err := b.take(filepath.Join(home, ".claude", "CLAUDE.md"), "user/CLAUDE.md", false); err != nil {
			return err
		}
	}
	// Claude walks root-down. At each directory it loads CLAUDE.md, or
	// .claude/CLAUDE.md when that is absent, then CLAUDE.local.md. Any of
	// those project files suppresses the AGENTS.md fallback. The user file
	// does not count.
	found := false
	for _, dir := range dirs {
		ok, err := b.take(filepath.Join(dir, "CLAUDE.md"), logicalName(b.repo, dir, "CLAUDE.md"), false)
		if err != nil {
			return err
		}
		if !ok {
			ok, err = b.take(filepath.Join(dir, ".claude", "CLAUDE.md"), logicalName(b.repo, dir, ".claude/CLAUDE.md"), false)
			if err != nil {
				return err
			}
		}
		local, err := b.take(filepath.Join(dir, "CLAUDE.local.md"), logicalName(b.repo, dir, "CLAUDE.local.md"), false)
		if err != nil {
			return err
		}
		found = found || ok || local
	}
	if !found {
		for _, dir := range dirs {
			if _, err := b.take(filepath.Join(dir, "AGENTS.md"), logicalName(b.repo, dir, "AGENTS.md"), false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *builder) codex(home string, dirs []string) error {
	if home != "" {
		dir := filepath.Join(home, ".codex")
		if _, err := b.take(filepath.Join(dir, "AGENTS.override.md"), "user/AGENTS.override.md", true); err != nil {
			return err
		}
		if !b.capped && !b.hasLogical("user/AGENTS.override.md") {
			if _, err := b.take(filepath.Join(dir, "AGENTS.md"), "user/AGENTS.md", true); err != nil {
				return err
			}
		}
		if b.capped {
			return nil
		}
	}
	for _, dir := range dirs {
		override := logicalName(b.repo, dir, "AGENTS.override.md")
		if _, err := b.take(filepath.Join(dir, "AGENTS.override.md"), override, true); err != nil {
			return err
		}
		if b.capped {
			return nil
		}
		if b.hasLogical(override) {
			continue
		}
		if _, err := b.take(filepath.Join(dir, "AGENTS.md"), logicalName(b.repo, dir, "AGENTS.md"), true); err != nil {
			return err
		}
		if b.capped {
			return nil
		}
	}
	return nil
}

func (b *builder) hasLogical(name string) bool {
	for _, file := range b.files {
		if file.Logical == name {
			return true
		}
	}
	return false
}

// take reads one candidate. skipEmpty drops a 0-byte Codex file so the other
// name at that level can be used. A symlink or unreadable file is an error.
func (b *builder) take(path, logical string, skipEmpty bool) (bool, error) {
	text, sum, size, ok, err := readCandidate(path)
	if err != nil || !ok {
		return false, err
	}
	if skipEmpty && size == 0 {
		return false, nil
	}
	if !b.add(logical, text, sum, size) {
		return false, nil
	}
	if abs, err := filepath.Abs(path); err == nil {
		b.seen[filepath.Clean(abs)] = true
	}
	if b.harness == "claude-code" {
		b.expandImports(path, text, 1)
	}
	return true, nil
}

func readCandidate(path string) (text, sum string, size int, ok bool, err error) {
	text, sum, size, err = rulesimport.ReadInstruction(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", 0, false, nil
	}
	if err != nil {
		return "", "", 0, false, fmt.Errorf("instruction file %s: %w", filepath.Base(path), err)
	}
	return text, sum, size, true, nil
}

func logicalName(root, dir, base string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || rel == "" {
		return "repo/" + base
	}
	return "repo/" + filepath.ToSlash(rel) + "/" + base
}

func cleanLaunch(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", rulesimport.ErrProhibitedPath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", rulesimport.ErrProhibitedPath
	}
	abs = filepath.Clean(abs)
	if err = rulesimport.Prohibited(abs); err != nil {
		return "", err
	}
	if err = rejectUnexpectedSymlinks(abs); err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("path must be a directory")
	}
	return abs, nil
}

func rejectUnexpectedSymlinks(abs string) error {
	if abs == string(filepath.Separator) {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(abs), "/"), "/")
	cur := string(filepath.Separator)
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return rulesimport.ErrProhibitedPath
		}
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if i == 0 && darwinRootAlias(part) {
				cur = next
				continue
			}
			return rulesimport.ErrSymlink
		}
		cur = next
	}
	return nil
}

func darwinRootAlias(part string) bool {
	if runtime.GOOS != "darwin" || (part != "var" && part != "tmp") {
		return false
	}
	target, err := os.Readlink(string(filepath.Separator) + part)
	if err != nil {
		return false
	}
	return target == "private/"+part || target == "/private/"+part
}

func findGitRoot(launch string) (string, error) {
	dir := launch
	for {
		info, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil && info.Mode()&os.ModeSymlink == 0 {
			return dir, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		if err = rulesimport.Prohibited(parent); err != nil {
			return "", nil
		}
		dir = parent
	}
}

func chainDirs(root, launch string) ([]string, error) {
	if root == "" {
		return []string{launch}, nil
	}
	var up []string
	for dir := launch; ; {
		up = append(up, dir)
		if dir == root {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("launch directory is outside the repository")
		}
		dir = parent
	}
	for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
		up[i], up[j] = up[j], up[i]
	}
	return up, nil
}

func repoNameOK(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}
