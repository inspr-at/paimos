// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func attentionSession(t *testing.T, f *harnessFixture) string {
	t.Helper()
	w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture-host",
		"harness_session_ref": "aeon-240-" + uid(), "worker_lease": "aeon-240-lease-" + uid(),
		"management_mode": "managed", "role": "worker",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='working',activity='busy',heartbeat_at=now() WHERE id=$1`, id)
		return err
	})
	return id
}

func attentionMessage(t *testing.T, f *harnessFixture, project, sender, recipient string, held bool) (string, string) {
	t.Helper()
	id, inboxID := uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		event, err := events.Append(t.Context(), tx, f.person, events.Change{Type: "inbox.sent", NodeID: &project, After: map[string]any{"fixture": true}})
		if err != nil {
			return err
		}
		var inbox any
		if !held {
			inbox = inboxID
			if _, err = tx.Exec(t.Context(), `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key)
				VALUES($1,$2::text::uuid,$3,$4,$5,'Synthetic private body',$2::text)`, f.person.TenantID, inboxID, sender, recipient, event.ID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,recipient_address,body,key_digest,request_digest,inbox_message_id,sent_event_id,is_action_request,expects_reply,delivery_level)
			VALUES($1,$2::text::uuid,$3,$4,$5,'paimos:fixture','Synthetic private body',$2::text,$2::text,$6,$7,$8,true,'simple')`, f.person.TenantID, id, project, sender, recipient, inbox, event.ID, held)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO inbox_reply_obligations(tenant_id,message_id) VALUES($1,$2)`, f.person.TenantID, id)
		return err
	})
	return id, inboxID
}

func TestSessionAttentionDoesNotSpreadPrincipalObligations(t *testing.T) {
	for _, kind := range []string{"approval", "held action", "peer reply"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t)
			if kind == "approval" {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at)
						VALUES($1,$2,$2,'nodes.write','node',$3,'Synthetic private rationale',now()+interval '1 hour')`, f.person.TenantID, f.agent.ID, f.ticket)
					return err
				})
			} else {
				attentionMessage(t, f, f.project, f.agent.ID, f.person.ID, kind == "held action")
			}
			// Historical principal work predates both simultaneous worker generations.
			for range 2 {
				id := attentionSession(t, f)
				data := attentionRead(t, f, f.person, id, false)
				reasons := data["attention_reasons"].([]any)
				if len(reasons) != 1 || reasons[0].(map[string]any)["scope"] != "shared" || reasons[0].(map[string]any)["blocking"] != false {
					t.Errorf("%s must remain visible as shared attention: %v", kind, reasons)
				}
			}
		})
	}
}

// All existing projections must agree, including explicit false/empty evidence.
func attentionRead(t *testing.T, f *harnessFixture, viewer tenant.Principal, id string, needs bool) map[string]any {
	t.Helper()
	var first map[string]any
	for _, endpoint := range []string{"/api/projects/" + f.project + "/harness-sessions/" + id, "/api/projects/" + f.project + "/harness-sessions", "/api/harness-sessions", "/api/harness-sessions/live?include_inactive=true"} {
		w := f.call(viewer, "GET", endpoint, nil, "")
		expect(t, w, 200)
		var rawData any
		if err := json.Unmarshal(w.Body.Bytes(), &rawData); err != nil {
			t.Fatal(err)
		}
		data, ok := rawData.(map[string]any)
		if !ok {
			data = map[string]any{"items": rawData}
		}
		if items, ok := data["items"].([]any); ok {
			data = nil
			for _, item := range items {
				s := item.(map[string]any)
				if s["id"] == id || s["session_id"] == id {
					data = s
				}
			}
		}
		if data == nil || data["needs_attention"] != needs {
			t.Fatalf("%s: needs_attention=%v, want %v", endpoint, data["needs_attention"], needs)
		}
		if first == nil {
			first = data
		} else if !reflect.DeepEqual(data["attention_reasons"], first["attention_reasons"]) {
			t.Fatalf("%s changed reason projection: %v vs %v", endpoint, data["attention_reasons"], first["attention_reasons"])
		}
		raw, _ := json.Marshal(data["attention_reasons"])
		for _, forbidden := range []string{"Synthetic private", "request_id", "message_id", "principal_id", "resource_id", "rationale", "body"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("reason evidence exposed %s", forbidden)
			}
		}
	}
	return first
}

func TestSessionAttentionExactRunAndFutureGeneration(t *testing.T) {
	f := fixture(t)
	owner, sibling := attentionSession(t, f), attentionSession(t, f)
	_, run := stateRun(t, f, f.project, "running", "BK5-1")
	approval := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET run_id=$2 WHERE id=$1`, owner, run); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,id,proposed_by_principal_id,agent_principal_id,run_id,scope,resource_kind,resource_id,rationale,expires_at)
			VALUES($1,$2,$3,$3,$4,'nodes.write','node',$5,'Synthetic private rationale',now()+interval '1 hour')`, f.person.TenantID, approval, f.agent.ID, run, f.ticket)
		return err
	})
	data := attentionRead(t, f, f.person, owner, true)
	reason := data["attention_reasons"].([]any)[0].(map[string]any)
	if reason["kind"] != "approval" || reason["scope"] != "run" || reason["actor"] != "person" || reason["blocking"] != true {
		t.Fatalf("lost exact run evidence: %v", reason)
	}
	attentionRead(t, f, f.person, sibling, false)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'denied')`, f.person.TenantID, approval, f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,run_id,scope,resource_kind,resource_id,rationale,proposed_at,expires_at)
			VALUES($1,$2,$2,$3,'nodes.write','node',$4,'Synthetic expired proposal',now()-interval '2 hours',now()-interval '1 hour')`, f.person.TenantID, f.agent.ID, run, f.ticket)
		return err
	})
	if reasons := attentionRead(t, f, f.person, owner, false)["attention_reasons"].([]any); len(reasons) != 0 {
		t.Fatalf("expired approval still visible: %v", reasons)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,run_id,scope,resource_kind,resource_id,rationale,expires_at)
			VALUES($1,$2,$2,$3,'nodes.write','node',$4,'Synthetic new proposal',now()+interval '1 hour')`, f.person.TenantID, f.agent.ID, run, f.ticket); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=now(),stop_reason='completed' WHERE id=$1`, owner)
		return err
	})
	attentionRead(t, f, f.person, owner, false)
	attentionRead(t, f, f.person, attentionSession(t, f), false)
	expect(t, f.call(f.foreign, "GET", "/api/projects/"+f.project+"/harness-sessions/"+owner, nil, ""), 404)
}

func TestSessionAttentionReplyLifecycleAndExplicitDelivery(t *testing.T) {
	f := fixture(t)
	owner, sibling := attentionSession(t, f), attentionSession(t, f)
	peer := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','Synthetic peer')`, f.person.TenantID, peer)
		return err
	})
	held, _ := attentionMessage(t, f, f.project, f.agent.ID, peer, true)
	outgoing, outgoingInbox := attentionMessage(t, f, f.project, f.agent.ID, peer, false)
	incoming, incomingInbox := attentionMessage(t, f, f.project, peer, f.agent.ID, false)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_deliveries(tenant_id,session_id,message_id,cursor)
			SELECT tenant_id,$2,id,sent_event_id FROM inbox_messages WHERE id=$1`, incomingInbox, owner)
		return err
	})
	check := func(id string, want int) {
		t.Helper()
		reasons := attentionRead(t, f, f.person, id, false)["attention_reasons"].([]any)
		if len(reasons) != want {
			t.Fatalf("got %v reasons, want %d", reasons, want)
		}
		for _, value := range reasons {
			r := value.(map[string]any)
			if r["kind"] == "held_action" && r["actor"] != "person" || r["kind"] == "reply" && r["actor"] != "agent" || r["kind"] == "reply_due" && (r["scope"] != "session" || id != owner) {
				t.Fatalf("wrong actor or session attribution: %v", r)
			}
		}
	}
	check(owner, 3)
	check(sibling, 2)
	// Acknowledgement completes delivery, not the reply obligation.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_deliveries SET completed_at=now() WHERE session_id=$1`, owner)
		return err
	})
	check(owner, 3)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := events.Append(t.Context(), tx, f.person, events.Change{Type: "inbox.action_resolved", NodeID: &f.project, After: map[string]any{"message_id": held, "decision": "resolved"}}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE inbox_reply_obligations SET closed_at=now(),reply_message_id=$2 WHERE message_id=$1`, outgoing, incoming); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_deliveries SET completed_at=NULL,released_at=now() WHERE session_id=$1`, owner)
		return err
	})
	check(owner, 0)
	check(sibling, 0)
	// Expiry excludes open obligations even if they have not yet been closed.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE inbox_reply_obligations SET closed_at=NULL,reply_message_id=NULL WHERE message_id=$1`, outgoing); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE inbox_messages SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1`, outgoingInbox)
		return err
	})
	check(owner, 0)
}

func TestSessionAttentionVisibilityAndMessageProjectIsolation(t *testing.T) {
	f := fixture(t)
	id := attentionSession(t, f)
	otherProject := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title)
			SELECT $1,$2,'BK5-2',kind_id,'Other fixture project' FROM nodes WHERE id=$3`, f.person.TenantID, otherProject, f.project)
		return err
	})
	attentionMessage(t, f, otherProject, f.agent.ID, f.person.ID, true)
	attentionMessage(t, f, otherProject, f.agent.ID, f.person.ID, false)
	if reasons := attentionRead(t, f, f.person, id, false)["attention_reasons"].([]any); len(reasons) != 0 {
		t.Fatalf("another project leaked attention: %v", reasons)
	}
	attentionMessage(t, f, f.project, f.agent.ID, f.person.ID, true)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at)
			VALUES($1,$2,$2,'nodes.write','node',$3,'Synthetic private rationale',now()+interval '1 hour')`, f.person.TenantID, f.agent.ID, f.ticket)
		return err
	})
	if reasons := attentionRead(t, f, f.person, id, false)["attention_reasons"].([]any); len(reasons) != 2 {
		t.Fatal("authorized shared inbox attention disappeared")
	}
	// Agent keys with only harness scopes cannot use the projection to bypass a
	// message inspection 403, even though the session itself is readable.
	f.agent.Scopes = []string{"harness.read"}
	if reasons := attentionRead(t, f, f.agent, id, false)["attention_reasons"].([]any); len(reasons) != 0 {
		t.Fatalf("harness-only agent read message evidence: %v", reasons)
	}
	for _, permissions := range [][]string{{"nodes.read"}, {"nodes.read", "harness.read"}} {
		reader := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
		role := uid()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Synthetic reader')`, reader.TenantID, reader.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO roles(tenant_id,id,key,name) VALUES($1,$2,$3,'Fixture role')`, reader.TenantID, role, "fixture_"+strings.ReplaceAll(role[:18], "-", "")); err != nil {
				return err
			}
			for _, permission := range permissions {
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, reader.TenantID, role, permission); err != nil {
					return err
				}
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, reader.TenantID, reader.ID, role)
			return err
		})
		w := f.call(reader, "GET", "/api/harness-sessions/live?include_inactive=true", nil, "")
		expect(t, w, 200)
		items := decode(t, w)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("expected one visible session, got %d", len(items))
		}
		data := items[0].(map[string]any)
		if len(permissions) == 1 {
			if _, found := data["attention_reasons"]; found {
				t.Fatal("project indicator exposed reasons without harness.read")
			}
		} else if reasons := data["attention_reasons"].([]any); len(reasons) != 0 {
			t.Fatalf("harness reader bypassed inbox.manage: %v", reasons)
		}
	}
	w := f.call(f.foreign, "GET", "/api/harness-sessions/live?include_inactive=true", nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 0 {
		t.Fatal("attention crossed tenant boundary")
	}
}
