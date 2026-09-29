// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/inbox"
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

// AEON-280: a drain marks the generation listening and hands the message over;
// stopping it fails its undelivered bound messages at once, never silently.
func TestDrainMarksListeningAndStopFailsBoundMessages(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "delivery-guarantee-lease-generation"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "harness_session_ref": "ref-" + lease, "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	detail := decode(t, f.call(f.person, "GET", base+"/"+id, nil, ""))
	if _, ok := detail["inbox_seen_at"]; ok {
		t.Fatalf("never pulled but seen: %v", detail["inbox_seen_at"])
	}
	messages := []string{}
	for range 2 {
		compat, inbox := attentionMessage(t, f, f.project, f.person.ID, f.agent.ID, false)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			for _, stmt := range []string{`UPDATE inbox_compat_messages SET recipient_session_id=$2 WHERE id=$1`, `UPDATE inbox_messages SET recipient_session_id=$2 WHERE id=$1`} {
				target := compat
				if stmt[7:21] == "inbox_messages" {
					target = inbox
				}
				if _, err := tx.Exec(t.Context(), stmt, target, id); err != nil {
					return err
				}
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO inbox_receipts(tenant_id,message_id,state,deliver_by) VALUES($1,$2,'queued',now()+interval '5 minutes')`, f.person.TenantID, inbox)
			return err
		})
		messages = append(messages, inbox)
	}
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/drain", map[string]any{}, lease), 200)
	detail = decode(t, f.call(f.person, "GET", base+"/"+id, nil, ""))
	if detail["inbox_seen_via"] != "drain" || detail["inbox_seen_at"] == nil {
		t.Fatalf("drain not recorded: %v %v", detail["inbox_seen_via"], detail["inbox_seen_at"])
	}
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/stop", map[string]any{"reason": "stopped"}, lease), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for i, message := range messages {
			var state, reason string
			var fetched, released bool
			if err := tx.QueryRow(t.Context(), `SELECT r.state,r.failure_reason,m.fetched_at IS NOT NULL,NOT EXISTS(SELECT 1 FROM harness_deliveries d WHERE d.message_id=m.id AND d.released_at IS NULL AND d.completed_at IS NULL)
				FROM inbox_receipts r JOIN inbox_messages m ON m.id=r.message_id WHERE r.message_id=$1`, message).Scan(&state, &reason, &fetched, &released); err != nil {
				return err
			}
			if state != "failed" || reason != "session_ended" || fetched != (i == 0) || !released {
				t.Fatalf("message %d: %s %s fetched=%t released=%t", i, state, reason, fetched, released)
			}
			var notices int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE recipient_principal_id=$1 AND idempotency_key=$2`, f.person.ID, "delivery-failed/"+message).Scan(&notices); err != nil {
				return err
			}
			if notices != 1 {
				t.Fatalf("sender notices for %d: %d", i, notices)
			}
		}
		return nil
	})
}

// AEON-280 review: a live drain lease holds off the deadline, so a managed
// session that already injected the message still completes it; once the
// lease is stale the message fails and a late completion is refused.
func TestDrainLeaseHoldsOffTheSweeper(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "delivery-guarantee-lease-sweeper-race"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "harness_session_ref": "ref-" + lease, "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	bound := func() string {
		compat, message := attentionMessage(t, f, f.project, f.person.ID, f.agent.ID, false)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE inbox_compat_messages SET recipient_session_id=$2 WHERE id=$1`, compat, id); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `UPDATE inbox_messages SET recipient_session_id=$2 WHERE id=$1`, message, id); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO inbox_receipts(tenant_id,message_id,state,deliver_by) VALUES($1,$2,'queued',now()-interval '1 second')`, f.person.TenantID, message)
			return err
		})
		return message
	}
	receipt := func(message string) (state string) {
		f.tx(t, f.person, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT state FROM inbox_receipts WHERE message_id=$1`, message).Scan(&state)
		})
		return
	}
	drain := func() map[string]any {
		w := f.call(f.agent, "POST", base+"/"+id+"/drain", map[string]any{}, lease)
		expect(t, w, 200)
		var items []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 {
			t.Fatalf("drain %s", w.Body.String())
		}
		return items[0]
	}
	sweep := func() {
		if _, err := inbox.NewSweeper(f.db.App).SweepTenant(t.Context(), f.person.TenantID); err != nil {
			t.Fatal(err)
		}
	}
	first := bound()
	d := drain()
	sweep()
	if state := receipt(first); state != "queued" {
		t.Fatalf("leased message failed under the drain: %s", state)
	}
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/complete-delivery", map[string]any{"delivery_id": d["delivery_id"], "cursor": d["cursor"]}, lease), 200)
	if state := receipt(first); state != "handed_off" {
		t.Fatalf("completed receipt %s", state)
	}
	second := bound()
	d = drain()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_deliveries SET leased_at=now()-interval '3 minutes' WHERE message_id=$1`, second)
		return err
	})
	sweep()
	if state := receipt(second); state != "failed" {
		t.Fatalf("stale lease receipt %s", state)
	}
	expect(t, f.call(f.agent, "POST", base+"/"+id+"/complete-delivery", map[string]any{"delivery_id": d["delivery_id"], "cursor": d["cursor"]}, lease), 409)
}
