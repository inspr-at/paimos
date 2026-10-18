// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"fmt"
	"strings"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

// VerifiedModel is evidence read from the immutable run, never a routine's
// declared provider. A vendor report must still satisfy the requested pin.
type VerifiedModel struct {
	Harness        string `json:"harness"`
	RequestedModel string `json:"requested_model"`
	EffectiveModel string `json:"effective_model"`
	Evidence       string `json:"model_evidence"`
}

func (m VerifiedModel) Family() (string, error) {
	if len(m.Harness) > 32 || len(m.RequestedModel) > 256 || len(m.EffectiveModel) > 256 || m.Evidence != "vendor_reported" || !reviewgate.ModelMatches(m.RequestedModel, m.EffectiveModel) {
		return "", fmt.Errorf("model evidence is unverified or does not match its pin")
	}
	family := harnesslaunch.ModelFamily(m.Harness, m.EffectiveModel)
	if !reviewgate.ValidFamily(family) {
		return "", fmt.Errorf("effective model provider is unknown")
	}
	return family, nil
}

// NormalizeAuthorFamily accepts provider families and unambiguous harness
// aliases. An empty family is allowed; callers enforce it for review gates.
func NormalizeAuthorFamily(author string) (string, error) {
	author = strings.TrimSpace(author)
	switch author {
	case "claude":
		return "anthropic", nil
	case "codex":
		return "openai", nil
	case "gemini":
		return "google", nil
	case "grok":
		return "xai", nil
	case "":
		return "", nil
	}
	if validFamily(author) {
		return author, nil
	}
	const accepted = "use openai, anthropic, xai, cursor, google or local (aliases: codex, claude, grok, gemini)"
	if author == "pi" || author == "opencode" {
		return "", fmt.Errorf("author family %q is ambiguous: pass the model family; %s", author, accepted)
	}
	return "", fmt.Errorf("unknown author family %q: %s", author, accepted)
}
