// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing

import (
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
)

func TestCoordinatorScopeSetStaysNarrow(t *testing.T) {
	if !slices.Equal(CoordinatorPermissions, authz.CoordinatorKeyScopes) {
		t.Fatal("pairing coordinator ceiling drifted from authz")
	}
	if !authz.IsCoordinatorKey(CoordinatorPermissions) || authz.IsCoordinatorKey(RuntimePermissions) {
		t.Fatal("coordinator recognition")
	}
	for _, scope := range []string{"models.read", "rules.read"} {
		if !slices.Contains(CoordinatorPermissions, scope) {
			t.Fatalf("coordinator ceiling missing %s", scope)
		}
	}
	if !slices.Contains(RuntimePermissions, "models.read") || slices.Contains(RuntimePermissions, "rules.read") {
		t.Fatal("paired runtime keys resolve models and do not read rules")
	}
	for _, forbidden := range []string{"rules.write", "rules.publish", "models.manage"} {
		if slices.Contains(CoordinatorPermissions, forbidden) || slices.Contains(RuntimePermissions, forbidden) {
			t.Fatalf("scope set includes %s", forbidden)
		}
	}
}
