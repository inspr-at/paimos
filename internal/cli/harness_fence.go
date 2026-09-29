// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// harnessFileKind names the only vendor files the heartbeat may read from a
// harness home, session directory or worktree. Everything else is denied.
type harnessFileKind int

const (
	// harnessClaudeTranscript is <CLAUDE_CONFIG_DIR>/projects/<slug>/<session>.jsonl
	// (and a subagent transcript up to two levels further down).
	harnessClaudeTranscript harnessFileKind = iota + 1
	// harnessCodexRollout is <CODEX_HOME>/sessions/YYYY/MM/DD/rollout-*.jsonl.
	harnessCodexRollout
	// harnessCodexIndex is <CODEX_HOME>/session_index.jsonl, by exact name.
	harnessCodexIndex
	// harnessGrokUsage is <GROK_HOME>/sessions/<enc-cwd>/<session>/usage.json.
	harnessGrokUsage
	// harnessCursorUsage is the launcher's stream-json copy <state>/cursor.jsonl.
	harnessCursorUsage
	// harnessAgentStatus is <worktree>/.agent-status.json.
	harnessAgentStatus
)

var errHarnessFileDenied = errors.New("refusing a file outside the harness usage allowlist")

func harnessKindForSource(source string) (harnessFileKind, bool) {
	switch source {
	case "", "claude":
		return harnessClaudeTranscript, true
	case "codex":
		return harnessCodexRollout, true
	case "grok":
		return harnessGrokUsage, true
	case "cursor":
		return harnessCursorUsage, true
	default:
		return 0, false
	}
}

// resolveHarnessPath is the single allowlist every harness reader passes
// through. It resolves the path to an absolute one first, so a relative path
// is judged by its real ancestors, then denies by default:
//   - any ".." component in the path as given,
//   - any absolute component that names a credential store,
//   - a final name or root-relative shape that is not this kind's vendor file.
//
// The root is the harness anchor inside the absolute path (projects/ for
// Claude, sessions/ for Codex rollouts and Grok, the file's own directory for
// exact-name kinds). Symlinks and hard links are refused later, per component,
// by openNoFollow.
func resolveHarnessPath(kind harnessFileKind, path string) (string, bool) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", false
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	parts := usagePathParts(abs)
	if len(parts) == 0 {
		return "", false
	}
	for _, part := range parts {
		if !printablePathPart(part) || credentialUsageName(part) || credentialUsageName(fsFold(part)) {
			return "", false
		}
	}
	base := parts[len(parts)-1]
	depth, ok := 1, true
	switch kind {
	case harnessClaudeTranscript:
		depth, ok = depthBelow(parts, "projects")
		ok = ok && depth >= 2 && depth <= 4 && len(base) > len(".jsonl") && strings.HasSuffix(base, ".jsonl")
	case harnessCodexRollout:
		depth, ok = depthBelow(parts, "sessions")
		ok = ok && depth >= 1 && depth <= 5 && strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl")
	case harnessGrokUsage:
		depth, ok = depthBelow(parts, "sessions")
		ok = ok && depth >= 1 && depth <= 4 && base == "usage.json"
	case harnessCodexIndex:
		ok = base == "session_index.jsonl"
	case harnessCursorUsage:
		ok = base == "cursor.jsonl"
	case harnessAgentStatus:
		ok = base == ".agent-status.json"
	default:
		ok = false
	}
	if !ok {
		return "", false
	}
	// Every component the vendor writes below its anchor is plain ASCII
	// (Claude slugs, dates, URL-encoded Grok paths, ids). Requiring it means
	// no Unicode spelling can alias a different name inside the allowed shape.
	for _, part := range parts[len(parts)-depth:] {
		if !plainFileName(part) {
			return "", false
		}
	}
	return abs, true
}

// fsFold maps a path component to a canonical form under which every name a
// case- and normalization-insensitive filesystem (APFS, HFS+) treats as the
// same, and more, compare equal: compatibility decomposition, dropping
// combining marks and default-ignorable format characters, Unicode case
// folding, then composition. Used only to deny; it may over-match.
func fsFold(part string) string {
	decomposed := norm.NFKD.String(part)
	var b strings.Builder
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) {
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFKC.String(cases.Fold().String(b.String()))
}

// openHarnessFile is the only way the heartbeat opens a file that lives in a
// harness home, session directory or worktree. The allowlist runs on the
// absolute path; the open walks every component from / with O_NOFOLLOW and
// requires a regular, single-link file owned by this user.
func openHarnessFile(kind harnessFileKind, path string) (*os.File, error) {
	abs, ok := resolveHarnessPath(kind, path)
	if !ok {
		return nil, errHarnessFileDenied
	}
	return openNoFollow(abs)
}

// statHarnessFile reports the size of an allowed harness file through the
// same fence as openHarnessFile. It never follows a link.
func statHarnessFile(kind harnessFileKind, path string) (os.FileInfo, error) {
	f, err := openHarnessFile(kind, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// depthBelow returns how many components follow the last anchor directory.
func depthBelow(parts []string, anchor string) (int, bool) {
	for i := len(parts) - 2; i >= 0; i-- {
		if parts[i] == anchor {
			return len(parts) - 1 - i, true
		}
	}
	return 0, false
}

func plainFileName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_', c == '%':
		default:
			return false
		}
	}
	return true
}

func printablePathPart(part string) bool {
	for _, r := range part {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
