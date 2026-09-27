// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// composerFastAlias is the Cursor model id that internal/agentd substitutes
// when the Aeon profile model is composer-2.5. It is the only alias this
// package canonicalizes.
const composerFastAlias = "composer-2.5[fast=true]"

func canonicalModel(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == composerFastAlias {
		s = "composer-2.5"
	}
	if s == "" || len(s) > 120 || !utf8.ValidString(s) {
		return "", fmt.Errorf("%w: model is not an exact identifier", ErrRejected)
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == ':' || r == '/' || r == '-'):
		default:
			return "", fmt.Errorf("%w: model is not an exact identifier", ErrRejected)
		}
	}
	switch strings.ToLower(s) {
	case "auto", "default", "unknown":
		return "", fmt.Errorf("%w: model is not an exact identifier", ErrRejected)
	}
	return s, nil
}
