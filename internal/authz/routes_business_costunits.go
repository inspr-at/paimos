// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for business/costunits.
func init() {
	registerRoutes("business_costunits", map[string]string{
		"GET /api/cost-units/{costUnitId}/rates":  "cost_units.read",
		"POST /api/cost-units/{costUnitId}/rates": "cost_units.manage",
	})
}
