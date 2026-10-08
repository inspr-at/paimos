// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for business/quotes/confirmation.
func init() {
	registerRoutes("business_quotes_confirmation", map[string]string{
		"GET /api/quotes/acceptances":                                       "quotes.read",
		"GET /api/quotes/readiness":                                         "quotes.read",
		"GET /api/quotes/{quoteId}/versions/{version}/confirmation":         "quotes.read",
		"GET /api/quotes/{quoteId}/versions/{version}/confirmation/receipt": "quotes.read",
		"POST /api/quotes/{quoteId}/versions/{version}/confirmation/retry":  "quotes.manage",
	})
}
