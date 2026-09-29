// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func cleanPath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", ErrProhibitedPath
	}
	// Check spelling before cleaning so private-store/../file is also refused.
	if err := pathProhibited(path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrProhibitedPath
	}
	if err := pathProhibited(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func pathProhibited(path string) error {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if componentProhibited(part, true) {
			return ErrProhibitedPath
		}
	}
	return nil
}

// Prohibited reports a private or secret path component. Harness instruction
// directories are prohibited here. Instruction reads use a separate allowlist.
func Prohibited(path string) error {
	if path == "" || strings.ContainsRune(path, 0) {
		return ErrProhibitedPath
	}
	return pathProhibited(path)
}

func componentProhibited(part string, blockHarness bool) bool {
	low := strings.ToLower(part)
	switch low {
	case ".inspr", ".ssh", ".aws", ".gnupg", ".paimos", ".aeon", ".config", ".cursor", ".local", ".cache", ".azure", ".kube", ".docker", ".grok", ".pi", "secrets", "secret", "credentials", "credentials.json", "auth", "auth.json", "config", "config.json", "transcripts", "transcript", "sessions", "keychains":
		return true
	case ".codex", ".claude":
		return blockHarness
	}
	if low == ".env" || strings.HasPrefix(low, ".env.") || strings.HasPrefix(low, "id_") {
		return true
	}
	for _, ext := range []string{".key", ".pem", ".age", ".gpg", ".p12", ".pfx", ".env", "_rsa", "_ed25519"} {
		if strings.HasSuffix(low, ext) {
			return true
		}
	}
	return false
}

// validateDoctrinePath runs before ANY open, including opens of directories.
func validateDoctrinePath(path string) (string, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return "", err
	}
	if _, err = classify(clean, SectionAll, 0); err != nil {
		return "", err
	}
	return clean, nil
}

// instructionBases are the only names a harness comparison may open.
func instructionBase(base string) bool {
	switch base {
	case "CLAUDE.md", "CLAUDE.local.md", "AGENTS.md", "AGENTS.override.md":
		return true
	default:
		return false
	}
}

// validateInstructionPath allows one immediate .claude or .codex parent for
// the matching instruction basename. Every other private component is refused.
// A missing file is not an error here; the opener reports that separately.
func validateInstructionPath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", ErrProhibitedPath
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", ErrProhibitedPath
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrProhibitedPath
	}
	abs = filepath.Clean(abs)
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(abs), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", ErrProhibitedPath
	}
	base := parts[len(parts)-1]
	if !instructionBase(base) {
		return "", ErrProhibitedPath
	}
	harnessDirs := 0
	for i, part := range parts {
		low := strings.ToLower(part)
		if low == ".claude" || low == ".codex" {
			harnessDirs++
			if harnessDirs > 1 || i != len(parts)-2 {
				return "", ErrProhibitedPath
			}
			if low == ".claude" && base != "CLAUDE.md" {
				return "", ErrProhibitedPath
			}
			if low == ".codex" && base != "AGENTS.md" && base != "AGENTS.override.md" {
				return "", ErrProhibitedPath
			}
			continue
		}
		if part == ".." || componentProhibited(part, false) {
			return "", ErrProhibitedPath
		}
	}
	return abs, nil
}

// ReadInstruction reads one allowlisted harness instruction file. Missing
// files return os.ErrNotExist. Symlinks are refused and are not followed.
func ReadInstruction(path string) (string, string, int, error) {
	f, err := openInstructionNoFollow(path)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	return readOpenedDoctrine(f)
}

// ReadContained reads one regular text file that stays inside one of roots.
// The instruction basename allowlist does not apply. Secret-looking
// components, symlinks and paths outside every root are refused before the
// file is opened. maxBytes includes the file; a larger file is not returned.
func ReadContained(path string, roots []string, maxBytes int) (string, string, int, error) {
	if maxBytes <= 0 || maxBytes > MaxFileBytes {
		return "", "", 0, ErrByteBound
	}
	f, err := openContainedNoFollow(path, roots)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", "", 0, ErrNotRegular
	}
	if info.Size() > int64(maxBytes) {
		return "", "", 0, ErrByteBound
	}
	return readOpenedDoctrine(f)
}

func validateContainedPath(path string, roots []string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) || len(roots) == 0 {
		return "", ErrProhibitedPath
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", ErrProhibitedPath
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrProhibitedPath
	}
	abs = filepath.Clean(abs)
	if !pathInsideRoots(abs, roots) {
		return "", ErrProhibitedPath
	}
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(abs), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", ErrProhibitedPath
	}
	for _, part := range parts {
		if part == "." || part == ".." || secretImportComponent(part) {
			return "", ErrProhibitedPath
		}
	}
	return abs, nil
}

func secretImportComponent(part string) bool {
	if componentProhibited(part, false) {
		return true
	}
	return strings.Contains(strings.ToLower(part), "credential")
}

func pathInsideRoots(path string, roots []string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		rel, err := filepath.Rel(abs, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

// readDoctrine uses a pinned descriptor walk, then checks that same descriptor
// before reading. No pathname stat/open race or blocking FIFO read is possible.
func readDoctrine(path string) (string, string, int, error) {
	clean, err := validateDoctrinePath(path)
	if err != nil {
		return "", "", 0, err
	}
	f, err := openNoFollow(clean)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	return readOpenedDoctrine(f)
}

func readOpenedDoctrine(f *os.File) (string, string, int, error) {
	return readOpenedDoctrineWith(f, io.ReadAll)
}

func readOpenedDoctrineWith(f *os.File, read func(io.Reader) ([]byte, error)) (string, string, int, error) {
	raw, err := readBoundedText(f, read)
	if err != nil {
		return "", "", 0, err
	}
	sum := sha256.Sum256(raw)
	return NormalizeInstructionBytes(raw), hex.EncodeToString(sum[:]), len(raw), nil
}

// ReadInstructionRaw reads one allowlisted harness instruction file and
// returns its raw bytes. Missing files return os.ErrNotExist. Symlinks are
// refused and are not followed. A byte cap must cut these bytes before
// NormalizeInstructionBytes; folding CR/CRLF first hides bytes past the cap.
func ReadInstructionRaw(path string) ([]byte, error) {
	f, err := openInstructionNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := readBoundedText(f, io.ReadAll)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// NormalizeInstructionBytes strips a leading UTF-8 BOM and folds CR and CRLF to LF.
func NormalizeInstructionBytes(raw []byte) string {
	text := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	text = bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
	text = bytes.ReplaceAll(text, []byte("\r"), []byte("\n"))
	return string(text)
}

func readBoundedText(f *os.File, read func(io.Reader) ([]byte, error)) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect doctrine descriptor")
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if info.Size() > MaxFileBytes {
		return nil, ErrByteBound
	}
	raw, err := read(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read doctrine descriptor")
	}
	if len(raw) > MaxFileBytes {
		return nil, ErrByteBound
	}
	if bytes.ContainsRune(raw, 0) || !utf8.Valid(raw) {
		return nil, ErrNotText
	}
	return raw, nil
}
