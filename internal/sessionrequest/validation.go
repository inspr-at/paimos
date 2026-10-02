// SPDX-License-Identifier: AGPL-3.0-only

// Package sessionrequest validates data crossing into a harness request consumer.
package sessionrequest

import (
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

var (
	labelPattern = regexp.MustCompile(`^[A-Za-z0-9 _.:()/#-]+$`)
	modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
)

func ValidLabel(label string) bool {
	return len(label) <= 64 && strings.TrimSpace(label) != "" && labelPattern.MatchString(label)
}

// ValidModel is a syntax/effort fence, not catalog authorization. Callers must
// also match the exact model and effort to an enabled tenant catalog profile.
func ValidModel(harness, model, effort string) bool {
	if len(model) > 128 || !modelPattern.MatchString(model) {
		return false
	}
	switch harness {
	case "codex", "claude", "pi", "grok":
		return effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh"
	case "cursor":
		return effort == "default" || effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh"
	case "gemini":
		_, err := harnesslaunch.GeminiBudgetForModel(model, effort)
		return err == nil
	case "opencode":
		return strings.Contains(model, "/") && len(effort) <= 32 && modelPattern.MatchString(effort)
	default:
		return false
	}
}
