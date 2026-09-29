// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
)

// AEON-279 integration: a managed hand-off that fails (AEON-282) goes through
// AEON-280's one terminal failure path. The sender gets exactly one System
// notice, also when agentd replays the completion or a late success follows;
// the receipt fails with an allowed reason and the message is never drained again.
func TestManagedDeliveryFailureNotifiesSenderOnce(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "generation-lease-000000000000000000000279"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host", "harness_session_ref": "vendor-ref-00000000000000279", "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox"}}
	w := f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	sessionID := decode(t, w)["id"].(string)
	path := base + "/" + sessionID
	messageID := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		e, err := events.Append(t.Context(), tx, f.person, events.Change{Type: "inbox.sent", After: map[string]any{"id": messageID}})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key,recipient_session_id) VALUES($1,$2,$3,$4,$5,'managed message',$6,$7)`, f.person.TenantID, messageID, f.person.ID, f.agent.ID, e.ID, uid(), sessionID); err != nil {
			return err
		}
		// A post-AEON-280 message: queued receipt with a delivery deadline.
		_, err = tx.Exec(t.Context(), `INSERT INTO inbox_receipts(tenant_id,message_id,state,deliver_by) VALUES($1,$2,'queued',clock_timestamp()+interval '5 minutes')`, f.person.TenantID, messageID)
		return err
	})
	w = f.call(f.agent, "POST", path+"/drain", map[string]any{}, lease)
	expect(t, w, 200)
	var deliveries []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &deliveries); err != nil || len(deliveries) != 1 || deliveries[0]["message_id"] != messageID {
		t.Fatalf("drain %s: %v", w.Body.String(), err)
	}
	d := deliveries[0]
	failed := map[string]any{"delivery_id": d["delivery_id"], "cursor": d["cursor"], "effective_level": "simple", "outcome": "failed", "failure_reason": "child_unavailable"}
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", failed, lease), 200)
	// Replay and a late success are idempotent: no second notice, no upgrade.
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", failed, lease), 200)
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", map[string]any{"delivery_id": d["delivery_id"], "cursor": d["cursor"], "effective_level": "simple"}, lease), 200)

	f.tx(t, f.person, func(tx pgx.Tx) error {
		var state, reason string
		if err := tx.QueryRow(t.Context(), `SELECT state,failure_reason FROM inbox_receipts WHERE message_id=$1`, messageID).Scan(&state, &reason); err != nil {
			return err
		}
		if state != "failed" || reason != "unavailable" {
			t.Fatalf("receipt %s %s, want failed unavailable", state, reason)
		}
		var acked, hidden bool
		if err := tx.QueryRow(t.Context(), `SELECT acked_at IS NOT NULL, expires_at<=clock_timestamp() FROM inbox_messages WHERE id=$1`, messageID).Scan(&acked, &hidden); err != nil {
			return err
		}
		if acked || !hidden {
			t.Fatalf("failed message acked=%t hidden=%t", acked, hidden)
		}
		rows, err := tx.Query(t.Context(), `SELECT recipient_principal_id::text,body FROM inbox_messages WHERE sender_label='System' AND idempotency_key LIKE 'delivery-failed/'||$1||'%'`, messageID)
		if err != nil {
			return err
		}
		defer rows.Close()
		var notices []string
		for rows.Next() {
			var recipient, body string
			if err := rows.Scan(&recipient, &body); err != nil {
				return err
			}
			if recipient != f.person.ID || !strings.Contains(body, "Not delivered") || !strings.Contains(body, "could not take it") {
				t.Fatalf("notice to %s: %q", recipient, body)
			}
			notices = append(notices, body)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(notices) != 1 {
			t.Fatalf("sender notices %d, want exactly 1", len(notices))
		}
		return nil
	})
	w = f.call(f.agent, "POST", path+"/drain", map[string]any{}, lease)
	expect(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("failed message drained again: %s", w.Body.String())
	}
}
