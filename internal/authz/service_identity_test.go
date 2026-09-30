// SPDX-License-Identifier: AGPL-3.0-only

package authz

import "testing"

// AEON-470: the service classification is default-deny. Only an agent with no
// legacy role tag is a person's agent; every tag, listed or not, marks a service.
func TestServiceIdentityClassificationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		roles   []string
		service bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"system"}, true},
		{[]string{"quote_public_service"}, true},
		{[]string{"portal_public_service"}, true},
		{[]string{"portal_future_service"}, true},
		{[]string{"anything"}, true},
		{[]string{"", "operator"}, true},
	} {
		if got := IsServiceIdentity(tc.roles); got != tc.service {
			t.Errorf("IsServiceIdentity(%q) = %v, want %v", tc.roles, got, tc.service)
		}
		if got := IsPersonAgent(tc.roles); got == tc.service {
			t.Errorf("IsPersonAgent(%q) = %v, want %v", tc.roles, got, !tc.service)
		}
	}
}
