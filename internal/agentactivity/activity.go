// SPDX-License-Identifier: AGPL-3.0-only

// Package agentactivity reduces tool observations to public, bounded phrases.
// Raw tool input is used only for classification, never retained or forwarded.
package agentactivity

import (
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Off     = "off"
	Tool    = "tool_activity"
	Summary = "agent_summary"
	Fresh   = 10 * time.Minute
)

type Activity struct {
	Text   string    `json:"text"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

func Current(doing *string, doingAt *time.Time, tool *string, toolAt *time.Time, mode string, now time.Time) *Activity {
	if mode == Off {
		return nil
	}
	if mode == Summary && doing != nil && doingAt != nil && now.Sub(*doingAt) < Fresh {
		if clean, valid := CleanSummary(*doing); valid {
			return &Activity{Text: clean, Source: "agent", At: *doingAt}
		}
	}
	if tool != nil && toolAt != nil && ValidAuto(*tool) {
		return &Activity{Text: *tool, Source: "auto", At: *toolAt}
	}
	return nil
}

func Mode(mode string) bool { return mode == Off || mode == Tool || mode == Summary }

var fileName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,39}\.(go|ts|tsx|js|mjs|vue|css|html|sql|yaml|yml|json|md|sh|py|rs)$`)
var command = regexp.MustCompile(`(?:^|[;&|]\s*|\s)(go\s+test|npm\s+(?:run\s+)?test(?::[a-z-]+)?|npx\s+playwright|playwright|git\s+commit|git\s+push|gh\s+pr\s+checks|git\s+worktree\s+(?:remove|prune))\b`)

func basename(target string) string {
	if len(target) > 1024 || strings.ContainsAny(target, "\x00\r\n\\:@?=$%") || strings.Contains(strings.ToLower(target), ".env") {
		return ""
	}
	name := path.Base(target)
	if !fileName.MatchString(name) || !SafeText(name) {
		return ""
	}
	return name
}

func ToolText(tool, target, shell string) string {
	switch strings.ToLower(tool) {
	case "edit", "write", "multiedit", "apply_patch", "filechange":
		if name := basename(target); name != "" {
			return "Editing " + name
		}
		return "Editing code"
	case "read", "read_file", "glob", "grep", "rg", "search":
		return "Reading code"
	case "bash", "exec_command", "shell", "commandexecution":
		// Only matched verbs select a fixed phrase. No argument survives.
		match := command.FindStringSubmatch(shell)
		if len(match) < 2 {
			return "Working"
		}
		verb := strings.Join(strings.Fields(match[1]), " ")
		switch {
		case strings.Contains(verb, "playwright"):
			return "Running browser tests"
		case strings.HasPrefix(verb, "go test"):
			return "Running Go tests"
		case strings.HasPrefix(verb, "npm "):
			return "Running web tests"
		case verb == "git commit":
			return "Committing"
		case verb == "git push":
			return "Pushing"
		case verb == "gh pr checks":
			return "Waiting for CI"
		case strings.HasPrefix(verb, "git worktree "):
			return "Cleaning up"
		}
	case "wait", "wait_agent", "sleep":
		return "Waiting for CI"
	}
	return "Working"
}

// ValidAuto is applied again at the API boundary: clients cannot smuggle tool
// arguments by labelling arbitrary text automatic.
func ValidAuto(text string) bool {
	if !SafeText(text) {
		return false
	}
	switch text {
	case "Working", "Reading code", "Editing code", "Running Go tests", "Running web tests", "Running browser tests", "Committing", "Pushing", "Waiting for CI", "Cleaning up":
		return true
	}
	return strings.HasPrefix(text, "Editing ") && strings.TrimPrefix(text, "Editing ") != "" && basename(strings.TrimPrefix(text, "Editing ")) == strings.TrimPrefix(text, "Editing ")
}

func CleanSummary(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" || utf8.RuneCountInString(text) > 60 || !SafeText(raw) {
		return "", false
	}
	return text, true
}
