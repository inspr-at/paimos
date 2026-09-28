// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import "regexp"

// modelWord matches reviewer-model names that doctrine must not keep as rules.
// Tool prohibitions that do not assign a reviewer stay importable.
var modelWord = regexp.MustCompile(`(?i)\b(?:claude|codex|grok|gpt-[0-9]|opus|sonnet|haiku|gemini)\b`)

var reviewWord = regexp.MustCompile(`(?i)\b(?:reviewers|reviewer|reviews|review|fallback)\b`)

var resolveWord = regexp.MustCompile(`(?i)\bmodel\s+resolve\b`)

func staleModelRoute(line string) bool {
	if !modelWord.MatchString(line) {
		return false
	}
	return reviewWord.MatchString(line) || resolveWord.MatchString(line)
}
