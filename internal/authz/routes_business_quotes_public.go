// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for business/quotes/public.
func init() {
	registerRoutes("business_quotes_public", map[string]string{
		"GET /api/public/quotes/{publicTenant}/{token}":                    "public",
		"GET /api/public/quotes/{publicTenant}/{token}/pdf":                "public",
		"GET /api/quotes/{quoteId}/versions/{version}/public-link":         "quotes.manage",
		"POST /api/public/quotes/{publicTenant}/{token}/accept":            "public",
		"POST /api/quotes/{quoteId}/versions/{version}/public-link":        "quotes.manage",
		"POST /api/quotes/{quoteId}/versions/{version}/public-link/revoke": "quotes.manage",
	})
}
