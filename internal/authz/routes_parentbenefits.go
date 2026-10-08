// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for parentbenefits.
func init() {
	registerRoutes("parentbenefits", map[string]string{
		"GET /api/nodes/{nodeId}/benefit-generation":        "nodes.read",
		"POST /api/nodes/{nodeId}/benefit-generation/retry": "nodes.write",
	})
}
