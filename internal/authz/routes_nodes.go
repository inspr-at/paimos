// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for nodes.
func init() {
	registerRoutes("nodes", map[string]string{
		"DELETE /api/kinds/{kindId}":                                  "kinds.manage",
		"DELETE /api/nodes/{nodeId}":                                  "nodes.delete",
		"DELETE /api/nodes/{nodeId}/work-lifecycle/{actionId}":        "nodes.write",
		"DELETE /api/tags/{tagId}":                                    "tags.manage",
		"GET /api/kinds":                                              "nodes.read",
		"GET /api/kinds/{kindId}":                                     "nodes.read",
		"GET /api/node-keys/{key}":                                    "nodes.read",
		"GET /api/nodes":                                              "nodes.read",
		"GET /api/nodes/lookup":                                       "nodes.read",
		"GET /api/nodes/tree":                                         "nodes.read",
		"GET /api/nodes/{nodeId}":                                     "nodes.read",
		"GET /api/nodes/{nodeId}/work-lifecycle":                      "nodes.read",
		"GET /api/projects":                                           "nodes.read",
		"GET /api/settings/work-vocabulary":                           "nodes.read",
		"GET /api/status/help":                                        "nodes.read", // Authenticated agents have a read-only exception in RequirePattern.
		"GET /api/tickets/graph":                                      "nodes.read",
		"PATCH /api/kinds/{kindId}":                                   "kinds.manage",
		"PATCH /api/nodes/{nodeId}":                                   "nodes.write",
		"PATCH /api/tags/{tagId}":                                     "tags.write",
		"POST /api/kinds":                                             "kinds.manage",
		"POST /api/nodes":                                             "nodes.write",
		"POST /api/nodes/bulk":                                        "nodes.write",
		"POST /api/nodes/{nodeId}/convert":                            "nodes.write",
		"POST /api/nodes/{nodeId}/move":                               "nodes.move",
		"POST /api/nodes/{nodeId}/project-move":                       "nodes.move",
		"POST /api/nodes/{nodeId}/work-lifecycle":                     "nodes.write",
		"POST /api/nodes/{nodeId}/work-lifecycle/{actionId}/continue": "nodes.write",
		"PUT /api/settings/work-vocabulary":                           "settings.manage",
	})
	registerDeclarations("nodes", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/kinds":                    true,
		"GET /api/kinds/{kindId}":           true,
		"GET /api/nodes":                    true,
		"GET /api/nodes/lookup":             true,
		"GET /api/nodes/tree":               true,
		"GET /api/projects":                 true,
		"GET /api/settings/work-vocabulary": true,
		"GET /api/status/help":              true,
		"GET /api/tickets/graph":            true,
	})
	registerDeclarations("nodes", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"POST /api/nodes": true,
	})
}
