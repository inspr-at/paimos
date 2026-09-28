// SPDX-License-Identifier: AGPL-3.0-only
package authz

import "testing"

func TestRulesPermissionsAndRoutes(t *testing.T) {
	read, ok := Lookup("rules.read")
	if !ok || read.Risk != "low" || !read.AgentGrantable {
		t.Fatal("rules.read registry")
	}
	write, ok := Lookup("rules.write")
	if !ok || !write.AgentGrantable {
		t.Fatal("rules.write registry")
	}
	publish, ok := Lookup("rules.publish")
	if !ok || publish.Risk != "high" || publish.AgentGrantable {
		t.Fatal("rules.publish must be high risk and person-only")
	}
	for _, role := range []string{"member", "viewer", "guest", "customer"} {
		perms, _ := BuiltinPermissions(role)
		if contains(perms, "rules.publish") {
			t.Fatal("publish granted to", role)
		}
	}
	for _, role := range []string{"owner", "admin"} {
		perms, _ := BuiltinPermissions(role)
		if !contains(perms, "rules.publish") {
			t.Fatal("publish missing for", role)
		}
	}
	for _, p := range []string{"POST /api/rules/sets/{setId}/publish", "POST /api/rules/sets/{setId}/restore", "POST /api/rules/publish"} {
		if RoutePermissions[p] != "rules.publish" {
			t.Fatal("publish route permission")
		}
	}
}
