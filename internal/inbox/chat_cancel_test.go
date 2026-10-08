// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: cancelling a queued send either recalls an already exposed payload,
// loses history, or lets an edited resend name someone else's original.
func TestSessionChatCancelAndLinkedResend(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Chat generation")
	input := compatInput(w.agent.ID, "cancel-original")
	input.RecipientSessionID = &session
	original := mustCompatSend(t, m, w.sender, project, input)
	path := "/api/projects/" + project + "/messages/" + original.ID + "/cancel"
	for _, p := range []string{w.agent.ID, w.recipient.ID, w.outsider.ID} {
		status, body := do(t, srv, p, "POST", path, "", nil)
		if status != 404 {
			t.Fatalf("foreign cancellation %d %s", status, body)
		}
	}
	for range 2 {
		status, body := do(t, srv, w.sender.ID, "POST", path, "", nil)
		result := mustJSON[chatCancelResult](t, body)
		if status != 200 || result.Result != "cancelled" || result.Receipt.State != "cancelled" || result.Receipt.Deadline.IsZero() {
			t.Fatalf("cancel %d %s", status, body)
		}
	}
	// Inspection keeps the original and exposes cancellation to every permitted
	// reader, so an already loaded participant view can remove the queue row.
	status, body := do(t, srv, w.recipient.ID, "GET", "/api/projects/"+project+"/messages?session="+session, "", nil)
	history := mustJSON[compatPage](t, body)
	if status != 200 || len(history.Items) != 1 || history.Items[0].ID != original.ID || !history.Items[0].Cancelled {
		t.Fatalf("cancelled history metadata %d %s", status, body)
	}
	status, body = do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+session, "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 0 {
		t.Fatalf("cancelled message offered %d %s", status, body)
	}
	status, body = do(t, srv, w.sender.ID, "GET", "/api/inbox/message-status?ids="+original.ID, "", nil)
	statuses := mustJSON[struct {
		Items []MessageStatus `json:"items"`
	}](t, body)
	if status != 200 || len(statuses.Items) != 1 || !statuses.Items[0].Cancelled {
		t.Fatalf("cancel receipt %d %s", status, body)
	}
	resend := input
	resend.Key = "edited-resend"
	resend.Body = "edited text"
	resend.ResendOf = &original.ID
	edited := mustCompatSend(t, m, w.sender, project, resend)
	if edited.ID == original.ID || edited.ResendOf == nil || *edited.ResendOf != original.ID {
		t.Fatalf("unlinked edit %+v", edited)
	}
	if again := mustCompatSend(t, m, w.sender, project, resend); again.ID != edited.ID {
		t.Fatal("retry duplicated edited send")
	}
	bad := resend
	bad.Key = "foreign-link"
	if _, err := m.commitMessage(t.Context(), w.recipient, project, bad); err == nil {
		t.Fatal("foreign sender linked original")
	}
	status, body = do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+session, "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 1 || mustJSON[Page](t, body).Items[0].ID != edited.ID {
		t.Fatalf("edited delivery %d %s", status, body)
	}
	status, body = do(t, srv, w.sender.ID, "POST", "/api/projects/"+project+"/messages/"+edited.ID+"/cancel", "", nil)
	if status != 200 || mustJSON[chatCancelResult](t, body).Result != "too_late" {
		t.Fatalf("offered recall %d %s", status, body)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_compat_messages WHERE id=ANY($1::uuid[])`, []string{original.ID, edited.ID}).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			return fmt.Errorf("lost history: %d", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Permissions are resolved again from current rows, not the principal's
	// earlier authenticated role snapshot.
	if _, err = w.db.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, w.sender.ID); err != nil {
		t.Fatal(err)
	}
	status, body = do(t, srv, w.sender.ID, "POST", path, "", nil)
	if status != 404 {
		t.Fatalf("revoked cancel authority %d %s", status, body)
	}
}
