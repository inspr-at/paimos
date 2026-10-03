// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var harnessVersionLine = regexp.MustCompile(`^(?:(?:codex(?:-cli)?|grok|pi|gemini|opencode|cursor-agent|Cursor Agent|Claude Code) )?(v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?)(?: \(Claude Code\))?$`)

const harnessVersionMax = 80 // Matches harness session registration validation.

// An absent or slow binary never blocks registration. Capture bounded stdout,
// discard stderr, and accept only a version-shaped line, never diagnostics.
func harnessVersionOrProbe(ctx context.Context, harness, supplied string) string {
	if supplied != "" {
		return heartbeatText(supplied, harnessVersionMax)
	}
	binary := map[string]string{"codex": "codex", "claude": "claude", "cursor": "cursor-agent", "grok": "grok", "pi": "pi", "gemini": "gemini", "opencode": "opencode"}[harness]
	if binary == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.WaitDelay = 100 * time.Millisecond
	output := &versionOutput{}
	cmd.Stdout, cmd.Stderr = output, io.Discard
	if err := cmd.Run(); err != nil || output.overflow {
		return ""
	}
	match := harnessVersionLine.FindStringSubmatch(strings.TrimSpace(string(output.data)))
	if len(match) != 2 || len(match[1]) > harnessVersionMax {
		return ""
	}
	return match[1]
}

type versionOutput struct {
	data     []byte
	overflow bool
}

func (w *versionOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(w.data)+n > 4096 {
		w.overflow = true
		p = p[:max(0, 4096-len(w.data))]
	}
	w.data = append(w.data, p...)
	return n, nil
}
