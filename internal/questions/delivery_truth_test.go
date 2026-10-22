// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"encoding/json"
	"testing"
	"time"
)

// Risk: a shared principal broadens fan-out, related notices leak ask context,
// retries duplicate FYIs, or queued answers claim delivery/seen. Existing
// successor and permission tests retain their assertions alongside this case.
func exerciseRelatedDeliveryTruth(t *testing.T) {
	f := deliveryFixtureFor(t)
	var parent string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT tenant_id,kind_id,'PARENT-1','Parent',$2 FROM nodes WHERE id=$1 RETURNING id::text`, f.ticket, f.project).Scan(&parent); err != nil {
		t.Fatal(err)
	}

	session := func(project, ticket string, stopped bool) string {
		t.Helper()
		var id string
		err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,stopped_at)
 VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','scout',$5,$6,CASE WHEN $7 THEN clock_timestamp() END) RETURNING id::text`, f.person.TenantID, project, f.otherAgent.ID, nullable(ticket), []byte(uid()), []byte(uid()), stopped).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	same := session(f.project, f.ticket, false)
	parentSession := session(f.project, parent, false)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, parent); err != nil {
		t.Fatal(err)
	}
	lead := session(f.project, "", false)
	unrelated := session(f.project, "", false)
	ended := session(f.project, f.ticket, true)
	hidden := session(f.hidden, f.hiddenTicket, false)
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,state,reason,session_id) VALUES($1,$2,$3,'working','',$4)`, f.person.TenantID, f.project, f.person.ID, lead); err != nil {
		t.Fatal(err)
	}
	in := input()
	in.SessionID = f.session
	in.TicketID = f.ticket
	q := f.answer(t, f.ask(t, in), "approved decision")
	find := func(q Question) Pending {
		t.Helper()
		for _, e := range q.Pending {
			if e.Kind == "inbox" && e.AskerID == q.Askers[0].ID {
				return e
			}
		}
		t.Fatal("missing inbox effect")
		return Pending{}
	}
	if e := find(q); e.Delivery == nil || e.Delivery.State != "decided-grace" || e.Delivery.DeliveredAt != nil {
		t.Fatalf("grace claimed delivery: %+v", e)
	}
	f.advance(10 * time.Second)
	f.dispatch(t)
	f.dispatch(t)
	e := find(f.status(t, q.ID))
	if e.Delivery == nil || e.Delivery.State != "inbox" || e.Delivery.SessionID != f.session || e.Delivery.DeliveredAt != nil || e.Delivery.SeenAt != nil {
		t.Fatalf("offline fallback claimed input: %+v", e.Delivery)
	}
	for _, id := range []string{same, parentSession, lead} {
		if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_session_id=$1 AND idempotency_key=$2`, id, "desk-fyi/"+q.Answer.ID+"/"+id); n != 1 {
			t.Fatalf("related notice count=%d", n)
		}
		var raw string
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT body FROM inbox_messages WHERE recipient_session_id=$1 AND idempotency_key=$2`, id, "desk-fyi/"+q.Answer.ID+"/"+id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var notice map[string]any
		if json.Unmarshal([]byte(raw), &notice) != nil || notice["type"] != "question.fyi" || notice["answer"] != nil || notice["context"] != nil || notice["notice"] == nil {
			t.Fatal("FYI leaked answer/context or requested work")
		}
	}
	for _, id := range []string{unrelated, ended, hidden} {
		if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_session_id=$1`, id); n != 0 {
			t.Fatal("FYI escaped related active project scope")
		}
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_wakes`); n != 0 {
		t.Fatal("answer or FYI created an idle wake")
	}
	// A provider handoff, rather than a lease, supplies the timestamp. Simulate
	// the existing receipt projection here; the production native case proves it
	// from correlated P3a observations through the actual server/daemon path.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET state='handed_off',handed_off_at=clock_timestamp() WHERE message_id::text=$1`, e.EffectRef); err != nil {
		t.Fatal(err)
	}
	e = find(f.status(t, q.ID))
	if e.Delivery.State != "seen" || e.Delivery.DeliveredAt == nil || e.Delivery.SeenAt == nil {
		t.Fatal("confirmed receipt missing from memo truth")
	}
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET state='failed',failure_reason='transport_error',handed_off_at=NULL WHERE message_id::text=$1`, e.EffectRef); err != nil {
		t.Fatal(err)
	}
	e = find(f.status(t, q.ID))
	if e.Delivery.State != "failed" || e.Delivery.FailureReason != "transport_error" {
		t.Fatal("failure hidden from memo")
	}
	// The selector is evaluated under the final access fence; losing recipient
	// permissions suppresses its next revision's FYI without blocking the asker.
	if _, err := f.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.otherAgent.ID); err != nil {
		t.Fatal(err)
	}
	q = f.answer(t, f.status(t, q.ID), "correction after access loss")
	f.advance(10 * time.Second)
	f.dispatch(t)
	if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE $1`, "desk-fyi/"+q.Answer.ID+"/%"); n != 0 {
		t.Fatal("revoked related agent received a new FYI")
	}
}
