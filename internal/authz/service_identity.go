// SPDX-License-Identifier: AGPL-3.0-only

package authz

import "strings"

// serviceRoles are the legacy role tags that mark an agent principal as an
// internal service identity (system, importer, operator, embedding and the
// public-facing quote and portal services). Such identities belong to Aeon, not
// to a person's Access list: the directory flags them and the lifecycle refuses
// to deactivate them.
var serviceRoles = []string{"system", "importer", "operator", "embedding", "quote_public_service", "quote_confirmation_service", "portal_public_service"}

// IsServiceIdentity reports whether an agent with these legacy roles is an
// internal service identity. The quote_ prefix stays reserved for future quote
// services.
func IsServiceIdentity(roles []string) bool {
	for _, v := range roles {
		if strings.HasPrefix(v, "quote_") {
			return true
		}
		for _, s := range serviceRoles {
			if v == s {
				return true
			}
		}
	}
	return false
}
