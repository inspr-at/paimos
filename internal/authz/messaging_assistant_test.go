// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: shared inbox scopes, authenticated/public exceptions or future routes
// accidentally open non-inbox authority to a permanently marked principal.
func TestMessagingAssistantDeniesEveryNonInboxRoute(t *testing.T) {
	p := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", TenantID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Agent, MessagingOnly: true, FullAccess: true}
	ctx := tenant.WithPrincipal(t.Context(), p)
	var checked int
	for pattern := range RoutePermissions {
		parts := strings.SplitN(pattern, " ", 2)
		if len(parts) == 2 && strings.HasPrefix(parts[1], "/api/inbox/") {
			continue
		}
		if err := RequirePattern(ctx, pattern, Scope{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("non-inbox route %s: %v", pattern, err)
		}
		checked++
	}
	if checked < 100 {
		t.Fatalf("route coverage incomplete: %d", checked)
	}
	for _, pattern := range []string{"GET /api/inbox/messages", "POST /api/inbox/messages", "POST /api/inbox/messages/{messageId}/ack", "GET /api/inbox/messages/{messageId}/receipt"} {
		if _, ok := RoutePermissions[pattern]; !ok || !MessagingAssistantInboxRoute(pattern) {
			t.Errorf("principal inbox route omitted: %s", pattern)
		}
	}
}
