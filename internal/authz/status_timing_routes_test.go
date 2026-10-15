// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Status timing is mounted by delivery and had no route permission at
// f088b1fc. Reads are nodes.read; baseline backfill is nodes.write. Handlers
// also require events.read inside the tenant transaction. The outer gate must
// stay those node permissions: not public, and not a neighbouring delivery grant.
func TestStatusTimingRoutePermissions(t *testing.T) {
	person := tenant.WithPrincipal(t.Context(), tenant.Principal{ID: "caller", TenantID: "tenant", Kind: tenant.Person})
	for _, tc := range []struct{ route, want string }{
		{"GET /api/projects/{projectId}/delivery/tickets", "nodes.read"},
		{"GET /api/nodes/{nodeId}/lifeline", "nodes.read"},
		{"POST /api/projects/{projectId}/delivery/tickets/backfill", "nodes.write"},
	} {
		if PatternIsPublic(tc.route) {
			t.Errorf("%s is public", tc.route)
		}
		got, ok := PermissionForPattern(tc.route)
		if !ok || got != tc.want {
			t.Errorf("%s permission %q declared=%v, want %s", tc.route, got, ok, tc.want)
		}
		if err := RequirePattern(t.Context(), tc.route, Scope{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s anonymous: %v", tc.route, err)
		}
		if err := RequirePattern(person, tc.route, Scope{}); !errors.Is(err, ErrNoStore) {
			t.Errorf("%s person: %v, want store lookup", tc.route, err)
		}
	}
}
