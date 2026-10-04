// SPDX-License-Identifier: AGPL-3.0-only

// Package testfixture supplies identical Markdown examples to its consumers' tests.
package testfixture

// Blocks contains code examples followed by an ordinary paragraph, so a caller
// can append a real heading/rule without accidentally continuing an indented block.
func Blocks() map[string]string {
	return map[string]string{
		"tilde":            "~~~md\n## Changelog\n- Example instruction.\n~~~\n\nEnd.\n\n",
		"long outer fence": "````md\n```\n## Changelog\n- Example instruction.\n```\n````\n\nEnd.\n\n",
		"closer with info": "~~~md\n~~~still-code\n## Changelog\n- Example instruction.\n~~~\n\nEnd.\n\n",
		"indented":         "    ## Changelog\n    - Example instruction.\n\nEnd.\n\n",
	}
}
