// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for aithema/tokens.
func init() {
	registerRoutes("aithema_tokens", map[string]string{
		"GET /api/aithema/jwks": "public",
	})
}
