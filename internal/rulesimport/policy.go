// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import "regexp"

// Model/review words are hints for human resolution, never grounds to drop
// source instructions or infer precedence (including safety prohibitions).
var modelWord = regexp.MustCompile(`(?i)\b(?:claude|codex|grok|gpt-[0-9]|opus|sonnet|haiku|gemini)\b`)

var reviewWord = regexp.MustCompile(`(?i)\b(?:reviewers|reviewer|reviews|review|fallback)\b`)

var resolveWord = regexp.MustCompile(`(?i)\bmodel\s+resolve\b`)

func staleModelRoute(line string) bool {
	if !modelWord.MatchString(line) {
		return false
	}
	return reviewWord.MatchString(line) || resolveWord.MatchString(line)
}
