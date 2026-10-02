// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

func TestAttachRefusalInventory(t *testing.T) {
	// Inventory of POST /api/agent-pairing/attach's register/request/poll
	// responses, including inherited activity/consent validation. Known legacy
	// text selects a fixed hint; arbitrary message and diagnostic text stay out.
	for _, tc := range []struct {
		status              int
		cause, code, action string
		messages            []string
	}{
		{401, "", "pairing_revoked", "Check paired computers", []string{"unauthorized"}},
		{410, "", "pairing_revoked", "Check paired computers", []string{"pairing revoked or setup not redeemed"}},
		{403, attachwatch.RefusalPairing, "pairing_revoked", "Check paired computers", []string{"computer proof rejected"}},
		{403, attachwatch.RefusalTicket, "ticket_not_visible", "Check the ticket", []string{"owner delegation no longer valid"}},
		{409, attachwatch.RefusalTicket, "ticket_not_visible", "Check the ticket", []string{"project, ticket or harness enrollment changed"}},
		{409, attachwatch.RefusalVersion, "version_mismatch", "Update Aeon", []string{"ignored"}},
		{410, attachwatch.RefusalExpired, "code_expired", "approve the new code", []string{"watch ended; new approval required"}},
		{409, attachwatch.RefusalDraining, "draining", "Let owned work finish", []string{"project, ticket or harness enrollment changed"}},
		{403, attachwatch.RefusalDraining, "draining", "Let owned work finish", []string{"computer proof rejected"}},
		{409, attachwatch.RefusalEnrollment, "enrollment_unavailable", "aeon-agentd add-harness", []string{"project, ticket or harness enrollment changed"}},
		{403, "", "live_limit", "approve or decline", []string{attachwatch.LiveLimitMessage}},
		{429, "", "live_limit", "approve or decline", []string{attachwatch.LiveLimitMessage}},
		{429, "", "computer_limit", "Stop an attached helper", []string{"computer attach limit reached"}},
		{429, "", "attempt_limit", "Wait 10 minutes", []string{"attach attempt cap reached"}},
		{429, "", "registration_limit", "Ask the Aeon administrator", []string{"daemon registration capacity reached"}},
		{429, "", "poll_limit", "Update paimos-agentd", []string{"poll at most once per second"}},
		{429, "", "rate_limit", "Wait before retrying", []string{"unrecognized"}},
		{403, "", "registration_lost", "restart agentd", []string{"daemon poll key rejected", "fresh daemon poll key required"}},
		{403, "", "pairing_unavailable", "Check the computer", []string{"paired daemon required", "computer proof rejected", "owner delegation no longer valid", "agent key scope required", "outside this computer's runtime authority"}},
		{403, "", "snapshot_changed", "Check the running process", []string{"attach proof rejected", "attach approval binding changed"}},
		{403, "", "consent_required", "complete fresh browser approval", []string{"boolean local confirmation is refused; upgrade agentd and re-pair", "local confirmation challenge already consumed or unavailable", "signed local confirmation required", "signed local confirmation rejected"}},
		{409, "", "version_mismatch", "Update Aeon", []string{"update agentd to attach protocol 2", "upgrade paimos-agentd to local consent proof v2", "update agentd to attach protocol 2; fresh approval required", "upgrade paimos-agentd to local consent proof v2 and restart; existing pairing keys remain valid; fresh approval required"}},
		{409, "", "draining", "Let owned work finish", []string{"enrollment is draining; no new work"}},
		{409, "", "scope_changed", "Check the ticket", []string{"project, ticket or harness enrollment changed"}},
		{409, "", "snapshot_changed", "Check the running process", []string{"snapshot digest required", "attach snapshot is immutable", "process or approval snapshot changed", "snapshot platform does not match the paired computer"}},
		{409, "", "consent_required", "complete fresh browser approval", []string{"consent binding required or mismatched; update agentd and attach again", "consent binding mismatch"}},
		{409, "", "ended", "approve the new request", []string{"attach request already consumed; create a new request"}},
		{409, "", "poll_limit", "Update paimos-agentd", []string{"poll sequence must increase"}},
		{410, "", "ended", "approve the new request", []string{"watch ended; new approval required", "session binding changed or ended"}},
		{400, "", "invalid_request", "Check the attach arguments", []string{"invalid attach request", "invalid local auth capability", "invalid local confirmation proof", "invalid attach operation", "activity requires an active watch", "consent is selected by the server at approval", "invalid snapshot or cwd outside approved workspace", "approval discovery rejects conversation text", "metadata-only attach rejects conversation text", "watch not active; conversation text rejected", "local confirmation required before conversation text", "activation contains no transcript", "poll sequence required", "doing must be a public summary of at most 60 characters", "invalid activity time", "invalid sanitized tool activity"}},
		{500, "", "server_unavailable", "inspect the server logs", []string{"pairing operation failed"}},
		{502, "", "server_unavailable", "inspect the server logs", []string{"private proxy body"}},
		{503, "", "server_unavailable", "inspect the server logs", []string{"unavailable"}},
		{408, "", "offline", "Check the connection", []string{"timeout"}},
		{403, attachwatch.RefusalVersion, "unknown", "inspect the attach request", []string{"unrecognized"}},
		{409, "unknown diagnostic", "unknown", "inspect the attach request", []string{"unrecognized"}},
		{404, "", "unknown", "inspect the attach request", []string{"not found"}},
		{302, "", "unknown", "inspect the attach request", []string{"redirect"}},
	} {
		for _, message := range tc.messages {
			t.Run(fmt.Sprintf("%d/%s/%s", tc.status, tc.cause, message), func(t *testing.T) {
				check := func(message string) *AttachLocalError {
					err := attachRefusal(fmt.Errorf("private wrapper: %w", &client.StatusError{Status: tc.status, AttachRefusal: tc.cause, Message: message}))
					var detail *AttachLocalError
					if !errors.As(err, &detail) || detail.Code != "attach_"+tc.code || !strings.Contains(detail.Hint, tc.action) {
						t.Fatalf("wanted %s with %q; got %v", tc.code, tc.action, err)
					}
					if strings.Contains(detail.Hint, "private") || strings.ContainsAny(detail.Hint, "\x1b\r\n") || len(detail.Hint) > 512 {
						t.Fatal("unsafe or oversized fixed hint")
					}
					return detail
				}
				got := check(message)
				if tc.cause != "" {
					if replacement := check("private response\x1b[31m"); replacement.Hint != got.Hint {
						t.Fatal("server text changed a typed hint")
					}
				}
			})
		}
	}
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, errors.New("private partial body"), &url.Error{Op: "Post", URL: "https://private.invalid", Err: errors.New("private transport")}} {
		got := attachRefusal(err)
		if !strings.HasPrefix(got.Error(), "attach_offline:") || strings.Contains(got.Error(), "private") {
			t.Fatal("transport error leaked or lost its action", got)
		}
	}
	if got := attachRefusal(fmt.Errorf("wrapped: %w", ErrDraining)); !strings.HasPrefix(got.Error(), "attach_draining:") {
		t.Fatal("local drain lost its cause", got)
	}
	for _, status := range []int{400, 403, 409, 410, 429, 500} {
		got := attachRefusal(&client.StatusError{Status: status, Message: "private response\x1b[31m", AttachRefusal: "private diagnostic"})
		if strings.Contains(got.Error(), "private") || strings.ContainsRune(got.Error(), '\x1b') {
			t.Fatal("arbitrary server text reached the terminal")
		}
	}
}
