// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"fmt"
	"strings"
)

// NormalizeAuthorFamily accepts provider families and unambiguous harness
// aliases. An empty family is allowed; callers enforce it for review gates.
func NormalizeAuthorFamily(author string) (string, error) {
	author = strings.TrimSpace(author)
	switch author {
	case "claude":
		return "anthropic", nil
	case "codex":
		return "openai", nil
	case "grok":
		return "xai", nil
	case "":
		return "", nil
	}
	if validFamily(author) {
		return author, nil
	}
	const accepted = "use openai, anthropic, xai or cursor (aliases: codex, claude, grok)"
	if author == "pi" {
		return "", fmt.Errorf("author family %q is ambiguous: pass the model family; %s", author, accepted)
	}
	return "", fmt.Errorf("unknown author family %q: %s", author, accepted)
}
