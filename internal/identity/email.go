// SPDX-License-Identifier: AGPL-3.0-only

package identity

import "strings"

// FoldEmailASCII folds only A-Z, matching the SQL translate predicates used
// for invitations. Unicode code points remain distinct mailbox identities.
func FoldEmailASCII(email string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, email)
}
