// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for business/quotes/collaboration.
func init() {
	registerRoutes("business_quotes_collaboration", map[string]string{
		"DELETE /api/quotes/{quoteId}/presence/{sessionId}": "quotes.delete",
		"GET /api/quotes/{quoteId}/collaboration/stream":    "quotes.read",
		"GET /api/quotes/{quoteId}/presence":                "quotes.read",
		"PATCH /api/quotes/{quoteId}/presence/{sessionId}":  "quotes.write",
		"POST /api/quotes/{quoteId}/presence":               "quotes.write",
	})
}
