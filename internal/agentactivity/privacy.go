// SPDX-License-Identifier: AGPL-3.0-only

package agentactivity

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The browser imports these same rules; every activity text path uses them.
//
//go:embed privacy.json
var privacyJSON []byte

type privacyPolicy struct {
	CredentialWords     []string `json:"credential_words"`
	OpaquePattern       string   `json:"opaque_pattern"`
	ForbiddenCharacters string   `json:"forbidden_characters"`
	UnicodeCategories   []string `json:"unicode_categories"`
}

var privacy = func() privacyPolicy {
	var policy privacyPolicy
	if err := json.Unmarshal(privacyJSON, &policy); err != nil {
		panic("invalid embedded activity privacy policy")
	}
	return policy
}()

var opaque = regexp.MustCompile(privacy.OpaquePattern)

// SafeText is length-independent so notes are screened before truncation.
// Drop the whole value: normalizing split credentials could expose them.
func SafeText(text string) bool {
	if !utf8.ValidString(text) || opaque.MatchString(text) || strings.ContainsAny(text, privacy.ForbiddenCharacters) || strings.ContainsFunc(text, func(r rune) bool {
		for _, category := range privacy.UnicodeCategories {
			if unicode.Is(unicode.Categories[category], r) {
				return true
			}
		}
		return false
	}) {
		return false
	}
	lower := strings.ToLower(text)
	for _, word := range privacy.CredentialWords {
		if strings.Contains(lower, word) {
			return false
		}
	}
	return true
}

// CleanNote preserves legacy control stripping and the 120-character limit,
// then applies the same credential checks as summaries and tool basenames.
func CleanNote(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	text := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	if text == "" || utf8.RuneCountInString(text) > 120 || !SafeText(text) {
		return "", false
	}
	return text, true
}
