// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for modelprovider.
func init() {
	registerRoutes("modelprovider", map[string]string{
		"GET /api/settings/model-provider":       "settings.manage",
		"POST /api/settings/model-provider/test": "settings.manage",
		"PUT /api/settings/model-provider":       "settings.manage",
	})
}
