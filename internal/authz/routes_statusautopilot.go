// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for statusautopilot.
func init() {
	registerRoutes("statusautopilot", map[string]string{
		"GET /api/projects/{projectId}/status-autopilot":            "nodes.read",
		"GET /api/settings/status-autopilot":                        "nodes.read",
		"GET /api/status-autopilot/attention":                       "nodes.read",
		"GET /api/status-autopilot/attention/groups":                "nodes.read",
		"GET /api/status-autopilot/changes":                         "nodes.read",
		"GET /api/status-autopilot/projects":                        "nodes.read",
		"GET /api/status-autopilot/proposals":                       "nodes.read",
		"POST /api/status-autopilot/attention/actions":              "nodes.write",
		"POST /api/status-autopilot/attention/bulk":                 "nodes.write",
		"POST /api/status-autopilot/attention/bulk/{batch_id}/undo": "nodes.write",
		"PUT /api/projects/{projectId}/status-autopilot":            "settings.manage",
		"PUT /api/settings/status-autopilot":                        "settings.manage",
		"PUT /api/status-autopilot/proposals/{eventId}":             "settings.manage",
	})
	registerDeclarations("statusautopilot", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/settings/status-autopilot":         true,
		"GET /api/status-autopilot/attention":        true,
		"GET /api/status-autopilot/attention/groups": true,
		"GET /api/status-autopilot/changes":          true,
		"GET /api/status-autopilot/projects":         true,
		"GET /api/status-autopilot/proposals":        true,
	})
	registerDeclarations("statusautopilot", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/status-autopilot/attention/actions":              true,
		"POST /api/status-autopilot/attention/bulk":                 true,
		"POST /api/status-autopilot/attention/bulk/{batch_id}/undo": true,
	})
}
