// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: a malicious lead expands a person grant, spends another actor's budget,
// switches trust contexts, or receives a result intended for another session.
// All work stays in the real app-role transactions; no provider is contacted.
func TestAssistantLeadDelegationPolicyAndResults(t *testing.T) {
	f := agentOwnedSetup(t)
	f.m.WithAssistantPolicy("https://personal.example.invalid")
	mux := f.handler.(*http.ServeMux)
	assistants.New(f.d.App).Mount(mux)
	inbox.New(f.d.App).Mount(mux)
	r, assistant := f.agentRoutine()
	policy := assistants.Policy{AllowedProjects: []string{f.project}, AllowedScopes: slices.Clone(assistant.Scopes), WeeklyBudget: assistants.Budget{Amount: "3", Unit: "USD"}, OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true}
	policy.AllowedScopes = append(policy.AllowedScopes, "nodes.write")
	link := map[string]any{"principal_id": assistant.ID, "display_name": "Governed researcher", "vendor": "grok", "owner_person_id": f.p.ID, "policy": policy}
	// Classification itself requires an existing explicit person-bound key;
	// it never widens a legacy messaging-only principal or creates authority.
	f.call(f.p, "POST", "/api/assistants", link, 403)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id,person_id) VALUES($1,$2,'Inert research fixture','assistant-policy-research',repeat('0',64),$3,$4,$4)`, f.p.TenantID, assistant.ID, assistant.Scopes, f.p.ID)
		return err
	})
	var linked assistants.Assistant
	if err := json.Unmarshal(f.call(f.p, "POST", "/api/assistants", link, 201), &linked); err != nil || linked.MessagingOnly {
		t.Fatalf("person link changed restriction: %v", err)
	}
	f.call(f.p, "PUT", "/api/recurrences/"+r.ID+"/agent-owner", map[string]any{"expected_revision": r.Revision, "owner_principal_id": assistant.ID, "destination": AgentDestination{Kind: "knowledge"}, "budget_ceiling": assistants.Budget{Amount: "1", Unit: "USD"}}, 200)
	r = f.get(r.ID)
	lead := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Agent, Name: "Delegating lead", Scopes: slices.Clone(authz.CoordinatorKeyScopes), KeyCreatorID: f.p.ID, KeyPersonID: f.p.ID}
	var session, unboundKey string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,agent_access_configured) VALUES($1,'agent','Delegating lead',true) RETURNING id::text`, f.p.TenantID).Scan(&lead.ID); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'delegating_coordinator','Delegating coordinator') RETURNING id::text`, f.p.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, f.p.TenantID, role, lead.Scopes); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.p.TenantID, lead.ID, role); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id,person_id) VALUES($1,$2,'Inert lead fixture','assistant-policy-lead',repeat('1',64),$3,$4,$4) RETURNING id::text`, f.p.TenantID, lead.ID, lead.Scopes, f.p.ID).Scan(&lead.KeyID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'Unbound fixture','assistant-policy-unbound',repeat('2',64),$3,$4) RETURNING id::text`, f.p.TenantID, lead.ID, lead.Scopes, f.p.ID).Scan(&unboundKey); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,ref_digest,lease_digest,phase,heartbeat_at) VALUES($1,$2,$3,$4,'codex','fixture','unmanaged','coordinator',decode(repeat('01',32),'hex'),decode(repeat('02',32),'hex'),'working',clock_timestamp()) RETURNING id::text`, f.p.TenantID, f.project, lead.ID, f.p.ID).Scan(&session)
	})
	lead.AuthKeyID = lead.KeyID
	input := func(key string) map[string]any {
		return map[string]any{"session_id": session, "instance": "https://personal.example.invalid", "scopes": slices.Clone(assistant.Scopes), "idempotency_key": key, "expected_revision": r.Revision}
	}
	path := "/api/recurrences/" + r.ID + "/delegate"
	var otherOwner string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other session owner') RETURNING id::text`, f.p.TenantID).Scan(&otherOwner); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, session, otherOwner)
		return err
	})
	f.call(lead, "POST", path, input("different-owner"), 403)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, session, f.p.ID)
		return err
	})
	bad := input("outside-grant")
	bad["scopes"] = append(slices.Clone(assistant.Scopes), "nodes.write")
	if raw := f.call(lead, "POST", path, bad, 409); !strings.Contains(string(raw), "assistant_delegation_scope_exceeds_grant") {
		t.Fatal("wrong scope refusal")
	}
	bad = input("cross-instance")
	bad["instance"] = "https://business.example.invalid"
	if raw := f.call(lead, "POST", path, bad, 409); !strings.Contains(string(raw), "assistant_instance_mismatch") {
		t.Fatal("wrong instance refusal")
	}
	unbound := lead
	unbound.KeyPersonID = ""
	unbound.KeyID = unboundKey
	unbound.AuthKeyID = unboundKey
	f.call(unbound, "POST", path, input("unbound-key"), 403)
	// A valid foreign key cannot resolve this tenant's routine or assistant.
	foreign := tenant.Principal{TenantID: "20000000-0000-4000-8000-000000000001", Kind: tenant.Agent, Scopes: slices.Clone(lead.Scopes)}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'foreign-assistant-delegation','Foreign delegation')`, foreign.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign owner') RETURNING id::text`, foreign.TenantID).Scan(&foreign.KeyPersonID); err != nil {
		t.Fatal(err)
	}
	foreign.KeyCreatorID = foreign.KeyPersonID
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,agent_access_configured) VALUES($1,'agent','Foreign lead',true) RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id,person_id) VALUES($1,$2,'Foreign inert fixture','assistant-policy-foreign',repeat('3',64),$3,$4,$4) RETURNING id::text`, foreign.TenantID, foreign.ID, foreign.Scopes, foreign.KeyPersonID).Scan(&foreign.KeyID); err != nil {
		t.Fatal(err)
	}
	foreign.AuthKeyID = foreign.KeyID
	f.call(foreign, "POST", path, input("cross-tenant"), 404)
	// A coordinator cannot manufacture a person grant on an ordinary routine.
	plain := f.input()
	plain.Definition = &Definition{Scope: DefinitionScope{Kind: "project", ProjectID: f.project}, OwnerPrincipalID: f.p.ID}
	noGrant := f.create(plain)
	noGrantInput := input("missing-grant")
	noGrantInput["expected_revision"] = noGrant.Revision
	f.call(lead, "POST", "/api/recurrences/"+noGrant.ID+"/delegate", noGrantInput, 403)
	var occurrences []Occurrence
	for _, key := range []string{"one", "two", "three"} {
		var out Occurrence
		if err := json.Unmarshal(f.call(lead, "POST", path, input(key), 200), &out); err != nil || out.Run == nil {
			t.Fatalf("delegation %s: %v", key, err)
		}
		occurrences = append(occurrences, out)
	}
	var replay Occurrence
	if err := json.Unmarshal(f.call(lead, "POST", path, input("one"), 200), &replay); err != nil || replay.Run == nil || replay.Run.ID != occurrences[0].Run.ID {
		t.Fatal("delegation retry duplicated work")
	}
	bad = input("one")
	bad["instance"] = "https://business.example.invalid"
	if raw := f.call(lead, "POST", path, bad, 409); !strings.Contains(string(raw), "delegation_idempotency_conflict") {
		t.Fatal("changed delegation retry accepted")
	}
	if raw := f.call(lead, "POST", path, input("over-budget"), 409); !strings.Contains(string(raw), "assistant_weekly_budget_exhausted") {
		t.Fatal("budget did not refuse before run creation")
	}
	if err := f.m.DispatchAgentRuns(t.Context(), f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	resultPath := "/api/recurrences/" + r.ID + "/runs/" + occurrences[0].Run.ID + "/agent-result"
	zero := int64(0)
	resultInput := AgentResultInput{Status: "succeeded", Title: "Research findings", Body: "Bounded findings from the delegated task", CostMicroUSD: &zero}
	var result AgentResult
	if err := json.Unmarshal(f.call(assistant, "POST", resultPath, resultInput, 200), &result); err != nil || result.NodeID == "" || result.LeadMessageID == "" {
		t.Fatalf("result/lead receipt absent: %v", err)
	}
	var again AgentResult
	if err := json.Unmarshal(f.call(assistant, "POST", resultPath, resultInput, 200), &again); err != nil || !reflect.DeepEqual(again, result) {
		t.Fatal("result retry changed its receipt")
	}
	f.tx(func(tx pgx.Tx) error {
		var reservations, runs, messages int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM assistant_budget_reservations WHERE principal_id=$1`, assistant.ID).Scan(&reservations); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_runs WHERE recurrence_id=$1`, r.ID).Scan(&runs); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages m JOIN inbox_receipts r ON r.message_id=m.id WHERE m.id=$1 AND m.recipient_principal_id=$2 AND m.recipient_session_id=$3 AND r.state='queued'`, result.LeadMessageID, lead.ID, session).Scan(&messages); err != nil {
			return err
		}
		if reservations != 3 || runs != 3 || messages != 1 {
			t.Fatalf("charges=%d runs=%d lead receipts=%d", reservations, runs, messages)
		}
		return nil
	})
	// Agent-reported zero cost does not turn the booked allowance into free work.
	f.call(lead, "POST", path, input("reported-free"), 409)
	policy.AllowedScopes = []string{"nodes.read", Permission, "inbox.read"}
	f.tx(func(tx pgx.Tx) error {
		raw, _ := json.Marshal(policy)
		_, err := tx.Exec(t.Context(), `UPDATE assistant_profiles SET policy=$2,revision=revision+1 WHERE principal_id=$1`, assistant.ID, raw)
		return err
	})
	deniedPath := "/api/recurrences/" + r.ID + "/runs/" + occurrences[1].Run.ID + "/agent-result"
	f.call(assistant, "POST", deniedPath, resultInput, 409)
	f.tx(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM assistant_policy_receipts WHERE principal_id=$1 AND operation='routine.result' AND NOT allowed AND reason='assistant_scope_denied: knowledge.write'`, assistant.ID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("result scope denial receipts=%d", count)
		}
		return nil
	})
	// A granted task queued while the lead key was valid must not dispatch
	// after that exact key is revoked; another usable lead key is no substitute.
	policy.AllowedScopes = append(slices.Clone(assistant.Scopes), "nodes.write")
	policy.WeeklyBudget.Amount = "5"
	f.tx(func(tx pgx.Tx) error {
		raw, _ := json.Marshal(policy)
		_, err := tx.Exec(t.Context(), `UPDATE assistant_profiles SET policy=$2,revision=revision+1 WHERE principal_id=$1`, assistant.ID, raw)
		return err
	})
	var revoked Occurrence
	if err := json.Unmarshal(f.call(lead, "POST", path, input("lead-revoked-after-acceptance"), 200), &revoked); err != nil || revoked.Run == nil {
		t.Fatal("missing revoke fixture run")
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE id=$1`, lead.KeyID)
		return err
	})
	if err := f.m.DispatchAgentRuns(t.Context(), f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	f.tx(func(tx pgx.Tx) error {
		var state, reason string
		var messages, events int
		if err := tx.QueryRow(t.Context(), `SELECT o.state,o.result->>'reason',(SELECT count(*) FROM inbox_messages WHERE idempotency_key='routine-run/'||o.run_id::text),(SELECT count(*) FROM events WHERE type='recurrence.agent_dispatch_held' AND after->>'run_id'=o.run_id::text) FROM routine_effect_outbox o WHERE run_id=$1 AND effect_key='agent-dispatch'`, revoked.Run.ID).Scan(&state, &reason, &messages, &events); err != nil {
			return err
		}
		if state != "cancelled" || reason != "agent_dispatch_refused" || messages != 0 || events != 1 {
			t.Fatalf("revoked lead state=%s reason=%s messages=%d events=%d", state, reason, messages, events)
		}
		return nil
	})
	// The third run was already dispatched under the live grant. Ending the
	// original lead (even with a successor) cannot discard its completed work.
	// The result is still authorized by the person grant and assistant policy;
	// the pointer must fail for the original session instead of being rerouted.
	var successor string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),stop_reason='completed' WHERE id=$1`, session); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,ref_digest,lease_digest,phase,heartbeat_at) VALUES($1,$2,$3,$4,'codex','fixture','unmanaged','coordinator',decode(repeat('03',32),'hex'),decode(repeat('04',32),'hex'),'working',clock_timestamp()) RETURNING id::text`, f.p.TenantID, f.project, lead.ID, f.p.ID).Scan(&successor)
	})
	stoppedPath := "/api/recurrences/" + r.ID + "/runs/" + occurrences[2].Run.ID + "/agent-result"
	var completed AgentResult
	if err := json.Unmarshal(f.call(assistant, "POST", stoppedPath, resultInput, 200), &completed); err != nil || completed.NodeID == "" || completed.LeadMessageID == "" {
		t.Fatalf("result after lead stopped must retain output and pointer receipt: %+v err=%v", completed, err)
	}
	var retried AgentResult
	if err := json.Unmarshal(f.call(assistant, "POST", stoppedPath, resultInput, 200), &retried); err != nil || !reflect.DeepEqual(completed, retried) {
		t.Fatalf("stopped-lead result retry changed output or receipt: %+v err=%v", retried, err)
	}
	f.tx(func(tx pgx.Tx) error {
		var state, reason, boundSession string
		var stored, outputs, rerouted, failures int
		if err := tx.QueryRow(t.Context(), `SELECT r.state,r.failure_reason,m.recipient_session_id::text,
 (SELECT count(*) FROM routine_runs WHERE id=$2 AND state='done' AND agent_result->>'node_id'=$3),
 (SELECT count(*) FROM nodes WHERE id=$3 AND fields->>'status'='proposed'),
 (SELECT count(*) FROM inbox_messages WHERE idempotency_key='assistant-result/'||$2::text AND (recipient_session_id=$4 OR recipient_session_id IS NULL)),
 (SELECT count(*) FROM events WHERE type='inbox.receipt_failed' AND after->>'message_id'=$1::text)
 FROM inbox_messages m JOIN inbox_receipts r ON r.message_id=m.id WHERE m.id=$1`, completed.LeadMessageID, occurrences[2].Run.ID, completed.NodeID, successor).Scan(&state, &reason, &boundSession, &stored, &outputs, &rerouted, &failures); err != nil {
			return err
		}
		if state != "failed" || reason != "session_ended" || boundSession != session || stored != 1 || outputs != 1 || rerouted != 0 || failures != 1 {
			t.Fatalf("stopped-lead result: state=%s reason=%s session=%s stored=%d outputs=%d rerouted=%d failures=%d", state, reason, boundSession, stored, outputs, rerouted, failures)
		}
		return nil
	})
}
