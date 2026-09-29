// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CollectInstructionFiles hashes explicit allowlisted instruction files.
// It does not enumerate directories or follow user symlinks. Linux and Darwin
// require physical paths; Darwin's root /var and /tmp aliases are also supported.
// Other platforms fail closed. Private-store path components are refused.
// The result carries a logical name, digest and byte size, never a path or contents.
func CollectInstructionFiles(paths []string) ([]ProvenanceItem, error) {
	if len(paths) > maxProvenanceItems {
		return nil, errors.New("at most 16 instruction files can be recorded")
	}
	items := make([]ProvenanceItem, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		item, err := hashInstructionFile(path)
		if err != nil {
			return nil, err
		}
		if seen[item.LogicalName] {
			return nil, errors.New("duplicate instruction logical name")
		}
		seen[item.LogicalName] = true
		items = append(items, item)
	}
	return items, nil
}

// CollectPresentInstructionFiles hashes each named instruction file that
// exists directly in dir, one at a time through CollectInstructionFiles.
// A missing name is skipped. settled is false when a name could not be checked
// (other than not existing), so the caller can try again later. A present file
// that the reader refuses is skipped, like an absent one.
func CollectPresentInstructionFiles(dir string, names ...string) ([]ProvenanceItem, bool) {
	settled := true
	var items []ProvenanceItem
	for _, name := range names {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			settled = false
			continue
		}
		got, err := CollectInstructionFiles([]string{path})
		if err != nil {
			continue
		}
		items = append(items, got...)
	}
	return items, settled
}

// PromptTemplateProvenance records a version identifier. digest is an
// explicit lowercase sha256 of the template bytes when the caller has one.
// An empty digest stores hash_kind absent and a null content digest. This
// function does not hash the version and does not open a file.
func PromptTemplateProvenance(version, digest string) (ProvenanceItem, error) {
	version = strings.TrimSpace(version)
	if !validProvenanceVersion(&version, true) {
		return ProvenanceItem{}, errors.New("prompt template version is not a version identifier")
	}
	item := ProvenanceItem{Kind: "prompt_template", LogicalName: "prompt-template", HashKind: "absent", Version: &version}
	if digest == "" {
		return item, nil
	}
	if !provenanceSHA256.MatchString(digest) {
		return ProvenanceItem{}, errors.New("prompt template digest must be lowercase sha256")
	}
	item.HashKind = "content"
	item.ContentSHA256 = &digest
	return item, nil
}

// SetProvenanceVersion attaches a version identifier to an allowlisted item
// already collected. It does not accept a new path.
func SetProvenanceVersion(items []ProvenanceItem, logicalName, version string) ([]ProvenanceItem, error) {
	version = strings.TrimSpace(version)
	if !validProvenanceVersion(&version, true) {
		return nil, errors.New("instruction version is not a version identifier")
	}
	found := false
	out := append([]ProvenanceItem(nil), items...)
	for i := range out {
		if out[i].LogicalName != logicalName {
			continue
		}
		out[i].Version = &version
		found = true
	}
	if !found {
		return nil, errors.New("instruction version does not match a recorded file")
	}
	return out, nil
}

func hashInstructionFile(path string) (ProvenanceItem, error) {
	return hashInstructionFileWithIO(path, openInstructionFile, io.ReadAll)
}

// The per-call seams let tests replace a name exactly at open time and prove
// that rejected targets never reach a content read. Production uses only the
// descriptor-relative opener; there is no mutable global hook or path fallback.
func hashInstructionFileWithIO(path string, open func(string) (*os.File, error), readAll func(io.Reader) ([]byte, error)) (ProvenanceItem, error) {
	if refusedInstructionPath(path) {
		return ProvenanceItem{}, errInstructionPath
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil || refusedInstructionPath(abs) {
		return ProvenanceItem{}, errInstructionPath
	}
	kind, logical, ok := instructionIdentity(abs)
	if !ok || refusedInstructionPath(logical) {
		return ProvenanceItem{}, errInstructionPath
	}
	f, err := open(abs)
	if err != nil {
		return ProvenanceItem{}, errInstructionPath
	}
	defer f.Close()
	// Check the very descriptor we will read, never a prior pathname snapshot.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ProvenanceItem{}, errInstructionPath
	}
	if info.Size() < 0 || info.Size() > maxProvenanceBytes {
		return ProvenanceItem{}, errors.New("instruction file is too large")
	}
	body, err := readAll(io.LimitReader(f, maxProvenanceBytes+1))
	if err != nil || int64(len(body)) != info.Size() {
		return ProvenanceItem{}, errInstructionPath
	}
	sum := sha256.Sum256(body)
	size := info.Size()
	digest := hex.EncodeToString(sum[:])
	return ProvenanceItem{Kind: kind, LogicalName: logical, HashKind: "content", ContentSHA256: &digest, ByteSize: &size}, nil
}

var errInstructionPath = errors.New("instruction file must be an explicit allowlisted AGENTS.md, CLAUDE.md or SKILL.md outside private stores; use a physical path on Linux or Darwin (Darwin /var and /tmp aliases are supported)")

func instructionIdentity(abs string) (kind, logical string, ok bool) {
	base := filepath.Base(abs)
	switch base {
	case "AGENTS.md":
		return "agents", "AGENTS.md", true
	case "CLAUDE.md":
		return "claude", "CLAUDE.md", true
	case "SKILL.md":
		parent := filepath.Base(filepath.Dir(abs))
		if !provenanceSkillSlug(parent) {
			return "", "", false
		}
		return "skill", parent + "/SKILL.md", true
	default:
		return "", "", false
	}
}

func provenanceSkillSlug(name string) bool {
	if name == "" || name == "." || name == ".." || privatePathComponent(name) || strings.ContainsAny(name, `/\:`) {
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
	return len(name) <= 64
}

func refusedInstructionPath(path string) bool {
	if path == "" || strings.Contains(path, "\x00") || strings.HasPrefix(path, "~") || strings.Contains(path, `\`) {
		return true
	}
	cleaned := filepath.Clean(path)
	for _, part := range strings.Split(cleaned, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || privatePathComponent(part) {
			return true
		}
	}
	return false
}

func privatePathComponent(name string) bool {
	switch strings.ToLower(name) {
	case ".ssh", ".inspr", ".aws", ".gnupg", ".age", ".kube", ".docker", ".npm", ".config",
		".secrets", "secrets", "credentials", "keychains", "cookies",
		".netrc", "id_rsa", "id_ed25519", ".env",
		"transcripts", "agent-transcripts", ".agent-transcripts":
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".env") || strings.HasSuffix(lower, ".key") || strings.HasSuffix(lower, ".pem") || strings.Contains(lower, "secret")
}
