// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const deletedCwdPrefix = "The current working directory was deleted"

const (
	detailDeletedFolder    = "the working folder was deleted"
	detailCommandNotFound  = "a command the harness needs was not found"
	detailPermissionDenied = "permission denied while starting the harness"
	detailMissingModule    = "a module the harness needs is missing"
)

var probeANSI = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
var commandNotFoundDetail = regexp.MustCompile(`(?i)^(?:[A-Za-z][A-Za-z0-9._+-]*: ){0,2}command not found(?:: [A-Za-z][A-Za-z0-9._+-]*)?$`)
var permissionDeniedDetail = regexp.MustCompile(`(?i)^(?:[A-Za-z][A-Za-z0-9._+-]*: ){0,2}permission denied$`)
var missingModuleDetail = regexp.MustCompile(`(?i)^(?:error: )?cannot find module ['"][^'"]+['"]$`)

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
	if knownProbeHarness(base) {
		return base
	}
	return ""
}

func genericProbeDetail(cmd string) string {
	name := "the harness"
	if knownProbeHarness(cmd) {
		name = cmd
	}
	return "the harness printed an error; run " + name + " --version in a terminal to see it"
}

func knownProbeHarness(name string) bool {
	switch name {
	case "claude", "codex", "cursor", "grok", "pi":
		return true
	default:
		return false
	}
}

// redactProbeLine maps a known stderr shape to a fixed sentence. The line
// itself is never copied; an unknown line becomes a generic hint.
func redactProbeLine(line string, homes []string) string {
	return safeProbeDetail(line, homes, "")
}

func safeProbeDetail(line string, _ []string, cmd string) string {
	line = strings.TrimSpace(probeANSI.ReplaceAllString(line, ""))
	if line == "" {
		return ""
	}
	if !utf8.ValidString(line) || strings.Contains(line, "-----BEGIN ") || probeLineUnsafe(line) {
		return genericProbeDetail(cmd)
	}
	line = strings.Join(strings.Fields(line), " ")
	if sentence := classifyProbeLine(line); sentence != "" {
		return sentence
	}
	return genericProbeDetail(cmd)
}

func probeLineUnsafe(line string) bool {
	for _, r := range line {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Mn) {
			return true
		}
	}
	return false
}

func classifyProbeLine(line string) string {
	switch {
	case strings.HasPrefix(line, deletedCwdPrefix):
		return detailDeletedFolder
	case commandNotFoundDetail.MatchString(line):
		return detailCommandNotFound
	case permissionDeniedDetail.MatchString(line):
		return detailPermissionDenied
	case missingModuleDetail.MatchString(line):
		return detailMissingModule
	default:
		return ""
	}
}
