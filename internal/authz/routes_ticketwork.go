// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for ticketwork.
func init() {
	registerRoutes("ticketwork", map[string]string{
		"GET /api/nodes/{nodeId}/agent-work": "nodes.read",
	})
}
