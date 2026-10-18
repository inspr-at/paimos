// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/inspr-at/paimos/internal/credentialguard"
)

// Method learnings can be read more widely than their source. The tagger
// refuses credential-shaped candidates; a person may explicitly confirm a
// false positive. Both use the same detector as rule/doctrine publication.
// Only positions are returned to the caller, never matched values.
type SensitiveRange = credentialguard.Range

func looksSensitive(text string) bool { return credentialguard.Contains(text) }

func sensitiveRanges(field, text string) []SensitiveRange {
	return credentialguard.Ranges(field, text)
}

// sourceHash is the revision of the text a nomination's excerpt came from.
// A stored excerpt is served only while its source still hashes the same.
func sourceHash(material string) string {
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}
