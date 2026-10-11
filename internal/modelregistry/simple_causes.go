// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/modelprefs"
)

func simpleOwnRow(s modelprefs.BoardState, column string) bool {
	for _, order := range s.Orders {
		if order.ProfileID == s.Workspace.ID && order.Column == column && order.Situation == "first" && order.Rank != nil {
			return true
		}
	}
	return false
}

// Only classify the resolver's privacy-filtered evidence. A generic private
// account failure stays generic; the presentation never reads private quotas.
func simpleCause(reason string) (string, *time.Time) {
	text := strings.ToLower(reason)
	cause := "no_account"
	switch {
	case strings.Contains(text, "daily cap") && !strings.Contains(text, "could not be checked"):
		cause = "daily_cap"
	case strings.Contains(text, "no tools"):
		cause = "no_tools"
	case strings.Contains(text, "retired"), strings.Contains(text, "no registered route"), strings.Contains(text, "known invalid model"), strings.Contains(text, "model invalid until"):
		cause = "retired"
	case strings.HasPrefix(text, "unavailable until "), strings.HasPrefix(text, "conserved until "), strings.HasPrefix(text, "budget_limited until "), strings.HasPrefix(text, "model paused"), strings.HasPrefix(text, "paused:"), strings.HasPrefix(text, "paused until "):
		cause = "paused"
	case strings.HasPrefix(text, "not allowed: "), strings.Contains(text, "residency"):
		cause = "residency"
	}
	// ModelWaitReason and model holds already redact times for private accounts.
	// Read only their explicit RFC3339 timestamp, never infer a reset time.
	if _, tail, ok := strings.Cut(reason, " until "); ok {
		if fields := strings.Fields(tail); len(fields) > 0 {
			if at, err := time.Parse(time.RFC3339, strings.TrimSuffix(fields[0], ":")); err == nil {
				return cause, &at
			}
		}
	}
	return cause, nil
}

func simpleCausesFor(column, line string, chosen simplePick, resolved *WorkResolution, c boardCatalog) []simpleUnavailable {
	out := []simpleUnavailable{}
	seen := map[string]bool{}
	// The harness that runs instead, so the page can tell "runs through Cursor instead" from another model.
	var via *string
	if chosen.Line != nil && chosen.Harness != "" {
		harness := chosen.Harness
		via = &harness
	}
	add := func(reason string, route *string) {
		for _, part := range strings.Split(reason, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			cause, until := simpleCause(part)
			if seen[cause] {
				continue
			}
			seen[cause] = true
			out = append(out, simpleUnavailable{Column: column, Line: line, RunsInstead: chosen.Line, Effort: chosen.Effort, Reason: part, Cause: cause, Until: until, Route: route, RunsVia: via, Waits: chosen.Line == nil})
		}
	}
	if resolved != nil {
		for _, step := range resolved.Ladder {
			for _, profile := range c.profiles[line] {
				if profile.ID == step.ProfileID {
					harness := profile.Harness
					for _, reason := range step.SkipReasons {
						add(reason, &harness)
					}
				}
			}
		}
		if len(out) == 0 {
			for _, step := range resolved.Trace.Held {
				if step.Line == line {
					add(step.Reason, nil)
				}
			}
		}
	}
	if len(out) == 0 {
		add("no qualified account with available capacity", nil)
	}
	return out
}
