// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for aithema/host.
func init() {
	registerRoutes("aithema_host", map[string]string{
		"GET /api/aithema/callbacks/{callbackId}":                                 "plugins.manage",
		"GET /api/plugins/aithema/settings":                                       "plugins.manage",
		"GET /api/projects/{projectId}/aithema/sessions/{sid}/proxy/{operation}":  "intake.write",
		"POST /api/aithema/deprovision":                                           "plugins.manage",
		"POST /api/projects/{projectId}/aithema/sessions":                         "intake.write",
		"POST /api/projects/{projectId}/aithema/sessions/{sid}/control":           "intake.write",
		"POST /api/projects/{projectId}/aithema/sessions/{sid}/host-event":        "intake.write",
		"POST /api/projects/{projectId}/aithema/sessions/{sid}/proxy/{operation}": "intake.write",
		"POST /api/projects/{projectId}/aithema/sessions/{sid}/tokens":            "intake.write",
		"PUT /api/plugins/aithema/settings":                                       "plugins.manage",
	})
}
