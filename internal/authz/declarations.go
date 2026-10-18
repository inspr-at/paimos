// SPDX-License-Identifier: AGPL-3.0-only

package authz

import "strings"

// Domain files contribute to the existing maps in init. Registration is local
// to this package, so no composition import can silently omit a domain.
func registerDeclarations[V any](domain, kind string, target, entries map[string]V) {
	for key, value := range entries {
		if _, exists := target[key]; exists {
			panic("authz: duplicate " + kind + " " + key + " in " + domain)
		}
		target[key] = value
	}
}

func registerRoutes(domain string, entries map[string]string) {
	for pattern := range entries {
		_, path, hasMethod := strings.Cut(pattern, " ")
		if !hasMethod {
			path = pattern
		}
		// These ServeMux fallbacks must remain unknown to RequirePattern. A
		// public declaration would bypass authentication for arbitrary URLs.
		if path == "/api/" || path == "/api" || path == "/" {
			panic("authz: fallback route " + pattern + " in " + domain)
		}
	}
	registerDeclarations(domain, "route", RoutePermissions, entries)
}
