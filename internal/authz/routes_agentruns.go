// SPDX-License-Identifier: AGPL-3.0-only

package authz

// Route and scope declarations for agentruns.
func init() {
	registerRoutes("agentruns", map[string]string{
		"DELETE /api/queue-snapshots/{snapshotId}":     "nodes.read",
		"DELETE /api/queue/{nodeId}":                   "nodes.read",
		"GET /api/queue":                               "nodes.read",
		"GET /api/queue-snapshots/{snapshotId}":        "nodes.read",
		"GET /api/queue/{nodeId}/readiness":            "nodes.read",
		"GET /api/runs":                                "run.read",
		"GET /api/runs/queued":                         "run.read",
		"GET /api/runs/{runId}":                        "run.read",
		"GET /api/runs/{runId}/handoff":                "run.read",
		"POST /api/queue":                              "nodes.read",
		"POST /api/queue-snapshots/{snapshotId}/apply": "nodes.read",
		"POST /api/queue/next":                         "nodes.read",
		"POST /api/queue/reset":                        "nodes.read",
		"POST /api/queue/{nodeId}/estimate":            "nodes.read",
		"POST /api/queue/{nodeId}/move":                "nodes.read",
		"POST /api/queue/{nodeId}/snapshots":           "nodes.read",
		"POST /api/queue/{nodeId}/undo":                "nodes.read", // Handler rechecks queue write permission and receipt ownership in the mutation transaction.
		"POST /api/runs/{runId}/cancel":                "run.create", // Person-only, queued run; handler checks work-order edit access (AEON-402).
		"POST /api/runs/{runId}/capacity-override":     "run.create", // Person-only, queued managed run; handler checks work-order edit access.
		"POST /api/runs/{runId}/claim":                 "run.claim",
		"POST /api/runs/{runId}/telemetry":             "run.telemetry",
	})
	registerDeclarations("agentruns", "project_filtered", ProjectFilteredRoutes, map[string]bool{
		"GET /api/queue": true,
	})
	registerDeclarations("agentruns", "project_decided", ProjectDecidedRoutes, map[string]bool{
		"DELETE /api/queue-snapshots/{snapshotId}":     true,
		"GET /api/queue-snapshots/{snapshotId}":        true,
		"POST /api/queue":                              true,
		"POST /api/queue-snapshots/{snapshotId}/apply": true,
		"POST /api/queue/next":                         true,
		"POST /api/queue/reset":                        true,
		"POST /api/queue/{nodeId}/snapshots":           true,
	})
}
