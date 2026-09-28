// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestHarnessSessionMessagesNeverMoveToAnotherGeneration(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	ids := []string{}
	leases := []string{"session-message-lease-first-generation", "session-message-lease-second-generation"}
	for _, lease := range leases {
		w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "harness_session_ref": "ref-" + lease, "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox"}}, "")
		expect(t, w, 201)
		ids = append(ids, decode(t, w)["id"].(string))
	}
	messages := []string{}
	for _, id := range ids {
		compat, inbox := attentionMessage(t, f, f.project, f.person.ID, f.agent.ID, false)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE inbox_compat_messages SET recipient_session_id=$2 WHERE id=$1`, compat, id); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE inbox_messages SET recipient_session_id=$2 WHERE id=$1`, inbox, id)
			return err
		})
		messages = append(messages, inbox)
	}
	// The newer generation drains first; it cannot steal the older message.
	for _, i := range []int{1, 0} {
		w := f.call(f.agent, "POST", base+"/"+ids[i]+"/drain", map[string]any{}, leases[i])
		expect(t, w, 200)
		var deliveries []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &deliveries); err != nil || len(deliveries) != 1 || deliveries[0]["message_id"] != messages[i] {
			t.Fatalf("drain crossed generation: %s", w.Body.String())
		}
	}
	// Stop releases the old lease but does not transfer its targeted message.
	expect(t, f.call(f.agent, "POST", base+"/"+ids[0]+"/stop", map[string]any{"reason": "stopped"}, leases[0]), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_messages SET acked_at=now(),acked_by_principal_id=$2 WHERE id=$1`, messages[1], f.agent.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE harness_deliveries SET completed_at=now() WHERE session_id=$1`, ids[1])
		return err
	})
	w := f.call(f.agent, "POST", base+"/"+ids[1]+"/drain", map[string]any{}, leases[1])
	expect(t, w, 200)
	if w.Body.String() != "[]\n" {
		t.Fatalf("ended session message migrated: %s", w.Body.String())
	}
}

func TestSessionBoundAttentionStaysWithItsGeneration(t *testing.T) {
	f := fixture(t)
	first, second := attentionSession(t, f), attentionSession(t, f)
	msg, _ := attentionMessage(t, f, f.project, f.agent.ID, f.person.ID, false)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_compat_messages SET sender_session_id=$2 WHERE id=$1`, msg, first)
		return err
	})
	for i, id := range []string{first, second} {
		data := attentionRead(t, f, f.person, id, false)
		reasons := data["attention_reasons"].([]any)
		if i == 0 {
			if len(reasons) != 1 || reasons[0].(map[string]any)["scope"] != "session" {
				t.Fatalf("missing generation attention %v", reasons)
			}
		} else if len(reasons) != 0 {
			t.Fatalf("inherited reply obligations %v", reasons)
		}
	}
}
