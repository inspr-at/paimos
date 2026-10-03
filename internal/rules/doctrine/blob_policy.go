// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode"
	"unicode/utf8"
)

type blobDisposition int

const (
	blobIndex blobDisposition = iota
	blobSkip
	blobReject
)

// Text is valid UTF-8 without control codes except the text delimiters TAB,
// LF and CR. No extension, signature, ASCII-run or encoding heuristic can
// silently exempt a private file. An operator exception binds the exact path
// and SHA-256 of the entire blob, including compressed/embedded content.
func classifyPrivateBlob(path string, raw []byte, allowlists ...map[string]string) blobDisposition {
	valid := utf8.Valid(raw)
	if valid {
		for _, r := range string(raw) {
			if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
				valid = false
				break
			}
		}
	}
	if valid {
		return blobIndex // Even whitespace is text; never exclude valid text.
	}
	if len(allowlists) == 1 {
		sum := sha256.Sum256(raw)
		if allowlists[0][path] == hex.EncodeToString(sum[:]) {
			return blobSkip
		}
	}
	return blobReject
}

// Include policy in the corpus version so removing/changing an exception
// invalidates a cached guard before the next proposal (and at startup).
func blobPolicyVersion(allowlists ...map[string]string) [32]byte {
	policy := map[string]string{}
	if len(allowlists) == 1 && allowlists[0] != nil {
		policy = allowlists[0]
	}
	raw, _ := json.Marshal(policy) // encoding/json orders string keys.
	return sha256.Sum256(raw)
}
