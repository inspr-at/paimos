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

const deletedCwdPrefix = "The current working directory was deleted"

var probeANSI = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
var probeGenericHome = regexp.MustCompile(`(?:/Users|/home)/[^/\s"'()\[\]{}|;<>:=]+`)
var deletedCwdDetail = regexp.MustCompile(`^The current working directory was deleted(?:: ~(?:/[A-Za-z0-9._-]+)*)?$`)
var commandNotFoundDetail = regexp.MustCompile(`(?i)^(?:[A-Za-z][A-Za-z0-9._+-]*: ){0,2}command not found(?:: [A-Za-z][A-Za-z0-9._+-]*)?$`)
var permissionDeniedDetail = regexp.MustCompile(`(?i)^(?:[A-Za-z][A-Za-z0-9._+-]*: ){0,2}permission denied$`)
var missingModuleDetail = regexp.MustCompile(`(?i)^(?:error: )?cannot find module ['"]((?:@[A-Za-z0-9][A-Za-z0-9._-]*/)?[A-Za-z][A-Za-z0-9._-]*)['"]$`)
var safeProbeCommand = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,64}$`)
var probeShapedSecret = regexp.MustCompile(`(?i)(?:\bsk-[A-Za-z0-9_-]{4,}|\baeon_[A-Za-z0-9_]{4,}|\bgh[pousr]_[A-Za-z0-9_]{4,}|\bAKIA[A-Z0-9]{8,}|\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}|[A-Za-z0-9+/_=-]{32,})`)
var probeEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

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

// commandDir resolves the first usable directory, then the user home. The
// filesystem root remains only when home itself is unavailable, so a removed
// process folder is never the working directory of a probe.
func commandDir(candidates ...string) string {
	candidates = append(append([]string{}, candidates...), userHome())
	seen := map[string]bool{}
	for _, dir := range candidates {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if physical, ok := usableProbeDir(dir); ok && physical != "/" {
			return physical
		}
	}
	return "/"
}

func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
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
	line := capturedProbeLine([]byte(exit.stderr), false)
	if line == "" {
		return ""
	}
	return safeProbeDetail(line, homes, exit.command)
}

func probeCommandName(path string) string {
	base := filepath.Base(path)
	if safeProbeCommand.MatchString(base) {
		return base
	}
	return ""
}

func genericProbeDetail(cmd string) string {
	name := "the harness"
	if safeProbeCommand.MatchString(cmd) && !probeShapedSecret.MatchString(cmd) && !probeEmail.MatchString(cmd) {
		name = cmd
	}
	return "the harness printed an error; run " + name + " --version in a terminal to see it"
}

// redactProbeLine keeps a stderr line only when the whole line is a known-safe
// diagnostic. Anything else becomes a generic hint that does not echo the line.
func redactProbeLine(line string, homes []string) string {
	return safeProbeDetail(line, homes, "")
}

func safeProbeDetail(line string, homes []string, cmd string) string {
	line = strings.TrimSpace(probeANSI.ReplaceAllString(line, ""))
	if line == "" || !utf8.ValidString(line) || strings.Contains(line, "-----BEGIN ") {
		return ""
	}
	for _, r := range line {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Mn) {
			return ""
		}
	}
	line = strings.Join(strings.Fields(line), " ")
	if safe := allowProbeLine(line, homes); safe != "" {
		return capProbeDetail(safe)
	}
	return genericProbeDetail(cmd)
}

func allowProbeLine(line string, homes []string) string {
	var safe string
	switch {
	case strings.HasPrefix(line, deletedCwdPrefix):
		shortened := line
		known := probeHomes(homes)
		for _, home := range known {
			shortened = replaceHome(shortened, home)
		}
		shortened = probeGenericHome.ReplaceAllString(shortened, "~")
		if !deletedCwdDetail.MatchString(shortened) {
			return ""
		}
		for _, home := range known {
			if strings.Contains(shortened, home) {
				return ""
			}
		}
		if strings.Contains(shortened, "/Users/") || strings.Contains(shortened, "/home/") {
			return ""
		}
		safe = shortened
	case commandNotFoundDetail.MatchString(line) || permissionDeniedDetail.MatchString(line):
		safe = line
	default:
		if m := missingModuleDetail.FindStringSubmatch(line); m != nil {
			safe = "cannot find module " + m[1]
		}
	}
	if safe == "" || probeShapedSecret.MatchString(safe) || probeEmail.MatchString(safe) {
		return ""
	}
	return safe
}

func probeHomes(homes []string) []string {
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
	return cleaned
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
