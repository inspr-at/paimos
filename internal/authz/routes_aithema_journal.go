// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for aithema/journal.
func init() {
	registerRoutes("aithema_journal", map[string]string{
		"GET /api/aithema/journal/sessions/{sid}/authority":  "public",
		"GET /api/aithema/journal/sessions/{sid}/cursor":     "public",
		"GET /api/aithema/journal/sessions/{sid}/records":    "public",
		"GET /api/aithema/ledger/sessions/{sid}/holds":       "public",
		"POST /api/aithema/journal/sessions/{sid}/op.result": "public",
		// Aithema delegates authentication to the journal module: a public outer
		// declaration never bypasses its JWT, exact capability or transaction fence.
		"POST /api/aithema/journal/sessions/{sid}/records":   "public",
		"POST /api/aithema/journal/sessions/{sid}/snapshots": "public",
		"POST /api/aithema/ledger/sessions/{sid}/admit":      "public",
		"POST /api/aithema/ledger/sessions/{sid}/claim":      "public",
		"POST /api/aithema/ledger/sessions/{sid}/recover":    "public",
		"POST /api/aithema/ledger/sessions/{sid}/settle":     "public",
	})
}
