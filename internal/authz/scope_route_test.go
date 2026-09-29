// SPDX-License-Identifier: AGPL-3.0-only

package authz

import "testing"

func TestDeliveryRatingTargetsItsSession(t *testing.T) {
	const sessionID = "11111111-1111-4111-8111-111111111111"
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		kind, id := routeTarget(method+" /api/harness-sessions/{sessionId}/delivery-rating", map[string]string{"sessionId": sessionID})
		if kind != "session" || id != sessionID {
			t.Fatalf("%s resolved %s %s", method, kind, id)
		}
	}
	kind, id := routeTarget("PATCH /api/quotes/{quoteId}/presence/{sessionId}", map[string]string{
		"quoteId":   "22222222-2222-4222-8222-222222222222",
		"sessionId": sessionID,
	})
	if kind != "" || id != "" {
		t.Fatalf("quote presence resolved as %s %s", kind, id)
	}
}
