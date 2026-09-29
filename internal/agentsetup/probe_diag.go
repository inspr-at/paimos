// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const probeDetailLimit = 160

var probeANSI = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
var probeGenericHome = regexp.MustCompile(`(?:/Users|/home)/[^/\s"'()\[\]{}|;<>:=]+`)
var probeBearer = regexp.MustCompile(`(?i)bearer\s+\S+`)
var probeAssignment = regexp.MustCompile(`(?i)(?:api[ _-]?key|access[ _-]?token|refresh[ _-]?token|client[ _-]?secret|private[ _-]?key|device[ _-]?proof|lifecycle[ _-]?secret|password|passwd|authorization|secret|token)\s*[:=]\s*\S+`)
var probeAeonKey = regexp.MustCompile(`(?i)\baeon_[A-Za-z0-9_]{4,}\b`)
var probeSK = regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_-]{4,}\b`)
var probeGitHub = regexp.MustCompile(`(?i)\bgh[pousr]_[A-Za-z0-9_]{4,}\b`)
var probeAWS = regexp.MustCompile(`\bAKIA[A-Z0-9]{8,}\b`)
var probeJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\b`)
var probeURLUser = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/?#]*@`)
var probeOpaque = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
var probeResidual = regexp.MustCompile(`(?i)(bearer\s+\S+|sk-[A-Za-z0-9_-]{4,}|gh[pousr]_[A-Za-z0-9_]{4,}|aeon_[A-Za-z0-9_]{4,}|AKIA[A-Z0-9]{8,}|(?:secret|token|password|passwd|authorization|api[ _-]?key)\s*[:=]\s*\S+)`)

// probeDirectory is the approved workspace, then the home directory.
// Version and login probes use this directory as their working folder.
func (d Discovery) probeDirectory() (string, error) {
	for _, dir := range []string{d.Workspace, d.Home} {
		if physical, ok := usableProbeDir(dir); ok && physical != "/" {
			return physical, nil
		}
	}
	return "", errors.New("harness probe directory unavailable")
}

func usableProbeDir(dir string) (string, bool) {
	if dir == "" || !filepath.IsAbs(dir) {
		return "", false
	}
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil || !filepath.IsAbs(physical) {
		return "", false
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return physical, true
}

// commandDir resolves the first usable directory. The filesystem root remains
// available after the process folder has been removed.
func commandDir(candidates ...string) string {
	for _, dir := range candidates {
		if physical, ok := usableProbeDir(dir); ok {
			return physical
		}
	}
	return "/"
}

type truncBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *truncBuffer) Write(p []byte) (int, error) {
	maximum := b.max
	if maximum <= 0 {
		maximum = 512
	}
	if remain := maximum - b.buf.Len(); remain > 0 {
		chunk := p
		if len(chunk) > remain {
			chunk = chunk[:remain]
			b.truncated = true
		}
		_, _ = b.buf.Write(chunk)
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func capturedProbeLine(raw []byte, truncated bool) string {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return ""
	}
	line, _, found := strings.Cut(string(raw), "\n")
	if !found && truncated {
		return ""
	}
	line = strings.TrimSpace(strings.TrimRight(line, "\r"))
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return ""
	}
	return line
}

func annotateProbe(base, cause error, homes ...string) error {
	detail := probeFailureDetail(cause, homes...)
	if detail == "" {
		return base
	}
	return fmt.Errorf("%w (%s)", base, detail)
}

func probeFailureDetail(err error, homes ...string) string {
	var exit *CommandError
	if err == nil || !errors.As(err, &exit) || exit == nil {
		return ""
	}
	return redactProbeLine(capturedProbeLine([]byte(exit.stderr), false), homes)
}

func redactProbeLine(line string, homes []string) string {
	line = strings.TrimSpace(probeANSI.ReplaceAllString(line, ""))
	if line == "" || !utf8.ValidString(line) || strings.Contains(line, "-----BEGIN ") {
		return ""
	}
	for _, r := range line {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Mn) {
			return ""
		}
	}
	cleaned := make([]string, 0, len(homes)+1)
	seen := map[string]bool{}
	for _, home := range homes {
		home = filepath.Clean(strings.TrimSpace(home))
		if len(home) < 2 || !filepath.IsAbs(home) || seen[home] {
			continue
		}
		seen[home] = true
		cleaned = append(cleaned, home)
		if physical, err := filepath.EvalSymlinks(home); err == nil {
			physical = filepath.Clean(physical)
			if len(physical) > 1 && filepath.IsAbs(physical) && !seen[physical] {
				seen[physical] = true
				cleaned = append(cleaned, physical)
			}
		}
	}
	if user, err := os.UserHomeDir(); err == nil {
		user = filepath.Clean(user)
		if len(user) > 1 && filepath.IsAbs(user) && !seen[user] {
			cleaned = append(cleaned, user)
		}
	}
	sort.Slice(cleaned, func(i, j int) bool { return len(cleaned[i]) > len(cleaned[j]) })
	for _, home := range cleaned {
		line = replaceHome(line, home)
	}
	line = probeGenericHome.ReplaceAllString(line, "~")
	for _, home := range cleaned {
		if strings.Contains(line, home) {
			return ""
		}
	}
	if strings.Contains(line, "/Users/") || strings.Contains(line, "/home/") {
		return ""
	}
	line = probeBearer.ReplaceAllString(line, "[redacted]")
	line = probeAssignment.ReplaceAllString(line, "[redacted]")
	line = probeAeonKey.ReplaceAllString(line, "[redacted]")
	line = probeSK.ReplaceAllString(line, "[redacted]")
	line = probeGitHub.ReplaceAllString(line, "[redacted]")
	line = probeAWS.ReplaceAllString(line, "[redacted]")
	line = probeJWT.ReplaceAllString(line, "[redacted]")
	line = probeURLUser.ReplaceAllString(line, "${1}")
	line = probeOpaque.ReplaceAllString(line, "[redacted]")
	line = strings.Join(strings.Fields(line), " ")
	check := strings.ReplaceAll(line, "[redacted]", "x")
	if line == "" || probeResidual.MatchString(check) || probeOpaque.MatchString(check) || probeJWT.MatchString(check) || probeURLUser.MatchString(check) {
		return ""
	}
	return capProbeDetail(line)
}

func replaceHome(line, home string) string {
	if home == "" || home == "/" {
		return line
	}
	var b strings.Builder
	rest := line
	for {
		i := strings.Index(rest, home)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		end := i + len(home)
		beforeOK := i == 0 || !isPathNameByte(rest[i-1])
		afterOK := end == len(rest) || !isPathNameByte(rest[end])
		b.WriteString(rest[:i])
		if beforeOK && afterOK {
			b.WriteByte('~')
		} else {
			b.WriteString(rest[i:end])
		}
		rest = rest[end:]
	}
}

func isPathNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

func capProbeDetail(line string) string {
	r := []rune(line)
	if len(r) <= probeDetailLimit {
		return line
	}
	cut := probeDetailLimit - 3
	out := strings.TrimSpace(string(r[:cut]))
	if i := strings.LastIndex(out, "["); i >= 0 && !strings.Contains(out[i:], "]") {
		out = strings.TrimSpace(out[:i])
	}
	if out == "" {
		return ""
	}
	return out + "..."
}
