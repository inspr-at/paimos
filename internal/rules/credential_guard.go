// SPDX-License-Identifier: AGPL-3.0-only

package rules

import "github.com/inspr-at/paimos/internal/credentialguard"

// Single, batch and restored publications all pass through publishSetIn.
// Inspect every free-text field copied into the immutable snapshot, including
// disabled rules and explanations that are visible only to people.
func guardPublicationCredentials(s Set, note string) error {
	texts := []string{s.Name, note}
	addTLDR := func(t *TLDR) {
		if t != nil {
			texts = append(texts, t.EN, t.DE)
		}
	}
	addTLDR(s.TLDR)
	for _, r := range s.Rules {
		texts = append(texts, r.Identity, r.Text, r.Why, r.Details, r.Source.Reference, r.Source.Revision, r.Source.Identity)
		addTLDR(r.TLDR)
	}
	for _, text := range texts {
		if credentialguard.Contains(text) {
			return fail(422, "credential_text", "This publication contains credential-shaped text. Remove credentials before publishing rules.")
		}
	}
	return nil
}
