// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bytes"
	"encoding/json"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"gopkg.in/yaml.v3"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/jackc/pgx/v5"
)

type recoveryAgentFixture struct {
	f                                           *harnessFixture
	path, session, run, lease, profile, account string
	identity                                    ownedprocess.Identity
}

func recoveryAgent(t *testing.T) recoveryAgentFixture {
	t.Helper()
	f := fixture(t)
	// The shared handler fixture does not encode its tenant in the key prefix.
	// Use its synthetic secret with a routable prefix for real auth middleware.
	parts := strings.SplitN(strings.TrimPrefix(f.key, "aeon_"), "_", 2)
	prefix := strings.ReplaceAll(f.agent.TenantID, "-", "") + strings.Repeat("7", 16)
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET prefix=$1 WHERE principal_id=$2`, prefix, f.agent.ID)
		return err
	})
	f.key = "aeon_" + prefix + "_" + parts[1]
	order, run := stateRun(t, f, f.project, "running", "REC-10")
	v := recoveryAgentFixture{f: f, run: run, lease: "fixture-recovery-lease-000000000000001", profile: uid(), account: uid(), identity: ownedprocess.Identity{DaemonID: "fixture", Generation: strings.Repeat("a", 32), ProcessID: strings.Repeat("b", 32), RootPID: 1234, GroupID: 1234, StartedAt: time.Now().UTC().Truncate(time.Second)}}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'recovery-model','1','claude','anthropic','fixture-model','high','standard')`, f.person.TenantID, v.profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,$2,'recovery-fixture','claude','fixture',$3,'Recovery account')`, f.person.TenantID, v.account, f.agent.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_orders SET status='running',assignee_principal_id=$2 WHERE node_id=$1`, order, f.agent.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_id=$2,daemon_generation=$3,account_id=$4,model_profile_id=$5 WHERE id=$1`, run, v.identity.DaemonID, v.identity.Generation, v.account, v.profile); err != nil {
			return err
		}
		req, computer := uid(), uid()
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,$2,'731000001',$3,$4,$5,'{}','fixture','redeemed',$6)`, f.person.TenantID, req, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,last_seen_at) SELECT $1,$2,$3,$4,id,'fixture',$5,clock_timestamp() FROM agent_keys WHERE principal_id=$4 LIMIT 1`, f.person.TenantID, computer, req, f.agent.ID, strings.Repeat("3", 64))
		return err
	})
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "harness_session_ref": "fixture-recovery-reference-00000001", "worker_lease": v.lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship", "model": "fixture-model", "reasoning_effort": "high", "advertised_capabilities": []string{"managed_control_v1", "session_recovery_v1", "stop", "inbox"}}, "")
	expect(t, w, 201)
	v.session = decode(t, w)["id"].(string)
	v.path = base + "/" + v.session
	expect(t, f.call(f.agent, "POST", v.path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "process_ownership": v.identity}, v.lease), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp()-interval '3 minutes' WHERE id=$1`, v.session)
		return err
	})
	return v
}
func (v recoveryAgentFixture) request(t *testing.T) map[string]any {
	t.Helper()
	w := v.f.call(v.f.person, "GET", v.path+"/recover-agent", nil, "")
	expect(t, w, 200)
	d := decode(t, w)
	if d["action"] != "restart" || d["cause"] != "heartbeat_overdue" {
		t.Fatalf("diagnosis=%v", d)
	}
	return map[string]any{"request_id": uid(), "expected_revision": d["observed_revision"], "action": "restart"}
}

// Exercise the real bearer authentication, paired-resource ceiling and route
// declarations as well as the handler; injecting a principal would miss them.
func (v recoveryAgentFixture) runtimeCall(t *testing.T, path string, body any, lease ...string) *httptest.ResponseRecorder {
	t.Helper()
	module, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{1}, 32)}, v.f.db.App)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+v.f.key)
	if len(lease) > 0 {
		r.Header.Set("X-Aeon-Worker-Lease", lease[0])
	}
	_, r.Pattern = v.f.mux.Handler(r)
	w := httptest.NewRecorder()
	module.Middleware(v.f.mux).ServeHTTP(w, r)
	return w
}
func (v recoveryAgentFixture) claim(t *testing.T) []map[string]any {
	t.Helper()
	w := v.runtimeCall(t, "/api/harness-recoveries/claim", map[string]any{"daemon_id": v.identity.DaemonID, "generation": v.identity.Generation})
	expect(t, w, 200)
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAgentRecoveryRequestAuthorityAndSingleClaim(t *testing.T) {
	v := recoveryAgent(t)
	f := v.f
	body := v.request(t)
	expect(t, f.call(f.agent, "POST", v.path+"/recover-agent", body, v.lease), 403)
	dbtest.BindRole(t, f.db, f.foreign.TenantID, f.foreign.ID, "admin")
	expect(t, f.call(f.foreign, "POST", v.path+"/recover-agent", body, ""), 404)
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
	different := v.request(t)
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", different, ""), 409)
	body["action"] = "reconnect"
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 409)
	body["action"] = "restart"
	expect(t, f.call(f.agent, "POST", "/api/harness-recoveries/claim", map[string]any{"daemon_id": "other", "generation": v.identity.Generation}, v.lease), 403)
	if got := v.claim(t); len(got) != 1 || got[0]["id"] != body["request_id"] {
		t.Fatalf("claim=%v", got)
	}
	if got := v.claim(t); len(got) != 0 {
		t.Fatal("uncertain claim was reoffered")
	}
	completion := map[string]any{"daemon_id": v.identity.DaemonID, "generation": v.identity.Generation, "outcome": "exited"}
	endpoint := "/api/harness-recoveries/" + body["request_id"].(string) + "/complete"
	expect(t, v.runtimeCall(t, endpoint, completion), 409) // no exit evidence
	completion["outcome"] = "rejected"
	expect(t, v.runtimeCall(t, endpoint, completion), 200)
	expect(t, v.runtimeCall(t, endpoint, completion), 200)
	completion["outcome"] = "exited"
	expect(t, v.runtimeCall(t, endpoint, completion), 409)
}

func TestAgentRecoveryRestartQueuesOneBoundContinuation(t *testing.T) {
	v := recoveryAgent(t)
	f := v.f
	agentruns.New(f.db.App).Mount(f.mux)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET display_label='Keep this name',service_tier='fast',continuation_handover='{"handover":{"state":"Existing recovery work is committed","next_steps":["Finish validation"],"open_questions":[],"worktree_state":"Keep local changes"}}' WHERE id=$1`, v.session)
		return err
	})
	body := v.request(t)
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
	v.claim(t)
	// An explicit stop and terminal run settlement model the daemon's verified
	// Wait result. Silence and run status alone are tested as insufficient above.
	expect(t, f.call(f.agent, "POST", v.path+"/stop", map[string]any{"reason": "stopped"}, v.lease), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='cancelled',started_at=clock_timestamp()-interval '1 minute',ended_at=clock_timestamp() WHERE id=$1`, v.run)
		return err
	})
	endpoint := "/api/harness-recoveries/" + body["request_id"].(string) + "/complete"
	completion := map[string]any{"daemon_id": v.identity.DaemonID, "generation": v.identity.Generation, "outcome": "exited"}
	w := v.runtimeCall(t, endpoint, completion)
	expect(t, w, 200)
	q := decode(t, w)
	if q["next_run_id"] != nil {
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET display_label='Changed old label',service_tier='default',continuation_handover=NULL WHERE id=$1`, v.session)
			return err
		})
		next := f.call(f.person, "GET", "/api/runs/"+q["next_run_id"].(string), nil, "")
		expect(t, next, 200)
		run := decode(t, next)
		if run["recovery_service_tier"] != "fast" || run["recovery_display_label"] != "Keep this name" || !strings.Contains(run["recovery_brief"].(string), "Existing recovery work is committed") {
			t.Fatal("continuation lost handover/name/tier")
		}
	}
	if q["outcome"] != "continuation_queued" || q["next_run_id"] == nil {
		t.Fatalf("completion=%v", q)
	}
	d := f.call(f.person, "GET", v.path+"/recover-agent", nil, "")
	expect(t, d, 200)
	if diagnosis := decode(t, d); diagnosis["action"] != "" || diagnosis["cause"] != "continuation_queued" {
		t.Fatal("old session offers another continuation")
	}
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", map[string]any{"request_id": uid(), "expected_revision": decode(t, d)["observed_revision"], "action": "restart"}, ""), 409)
	w = v.runtimeCall(t, endpoint, completion)
	expect(t, w, 200)
	if decode(t, w)["next_run_id"] != q["next_run_id"] {
		t.Fatal("completion retry created a different run")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		var account, profile string
		if err := tx.QueryRow(t.Context(), `SELECT count(*),min(requested_account_id::text),min(model_profile_id::text) FROM agent_runs WHERE retry_of_run_id=$1`, v.run).Scan(&count, &account, &profile); err != nil {
			return err
		}
		if count != 1 || account != v.account || profile != v.profile {
			t.Fatalf("continuation lost binding: count=%d account=%s profile=%s", count, account, profile)
		}
		return nil
	})
}

func TestAgentRecoveryRevocationExpiryAndObservationFence(t *testing.T) {
	v := recoveryAgent(t)
	f := v.f
	body := v.request(t)
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	if got := v.claim(t); len(got) != 0 {
		t.Fatal("revoked requester reached daemon")
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	body = v.request(t)
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET revision=revision+1 WHERE id=$1`, v.session)
		return err
	})
	if got := v.claim(t); len(got) != 0 {
		t.Fatal("changed observation reached daemon")
	}
	body = v.request(t)
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
	v.claim(t)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	endpoint := "/api/harness-recoveries/" + body["request_id"].(string) + "/complete"
	completion := map[string]any{"daemon_id": v.identity.DaemonID, "generation": v.identity.Generation, "outcome": "rejected"}
	expect(t, v.runtimeCall(t, endpoint, completion), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_recoveries SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, body["request_id"])
		return err
	})
	w := f.call(f.person, "GET", v.path+"/recover-agent/"+body["request_id"].(string), nil, "")
	expect(t, w, 200)
	if q := decode(t, w); q["state"] != "expired" || q["outcome"] != "unconfirmed" {
		t.Fatalf("expired claim=%v", q)
	}
}

func TestAgentRecoveryOpenAPIContractsAgree(t *testing.T) {
	read := func(path string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err = yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	api, module := read("../../api/openapi.yaml"), read("openapi.yaml")
	for _, path := range []string{"/projects/{projectId}/harness-sessions/{sessionId}/recover-agent", "/projects/{projectId}/harness-sessions/{sessionId}/recover-agent/{requestId}", "/harness-recoveries/claim", "/harness-recoveries/{requestId}/complete"} {
		want := api["paths"].(map[string]any)[path]
		got := module["paths"].(map[string]any)["/api"+path]
		if want == nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("recovery contract diverges: %s", path)
		}
	}
	for _, name := range []string{"AgentRecoveryDiagnosis", "AgentRecoveryReceipt", "HarnessProcessOwnership"} {
		want := api["components"].(map[string]any)["schemas"].(map[string]any)[name]
		got := module["components"].(map[string]any)["schemas"].(map[string]any)[name]
		if want == nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("recovery schema diverges: %s", name)
		}
	}
}

func TestAttachedAgentRecoveryRequiresPairedHookAndCurrentAuthority(t *testing.T) {
	v := recoveryAgent(t)
	f := v.f
	dbtest.BindRole(t, f.db, f.foreign.TenantID, f.foreign.ID, "admin")
	inbox.New(f.db.App).Mount(f.mux)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET management='unmanaged',run_id=NULL,capabilities=ARRAY['inbox','status'],process_ownership=NULL,process_observed_at=NULL WHERE id=$1`, v.session)
		return err
	})
	beat := map[string]any{"phase": "working", "activity_sequence": 2, "attached_hook": true, "process_ownership": v.identity}
	expect(t, v.runtimeCall(t, v.path+"/heartbeat", beat, v.lease), 200)
	expect(t, f.call(f.agent, "POST", v.path+"/heartbeat", beat, "wrong-lease"), 403)
	wrong := v.identity
	wrong.DaemonID = "unpaired"
	beat["process_ownership"] = wrong
	expect(t, f.call(f.agent, "POST", v.path+"/heartbeat", beat, v.lease), 403)
	beat["process_ownership"] = v.identity
	expect(t, v.runtimeCall(t, v.path+"/heartbeat", beat, v.lease), 200)
	d := f.call(f.person, "GET", v.path+"/recover-agent", nil, "")
	expect(t, d, 200)
	diagnosis := decode(t, d)
	if diagnosis["action"] != "reconnect" || diagnosis["cause"] != "inbox_not_listening" {
		t.Fatalf("attached diagnosis=%v", diagnosis)
	}
	body := map[string]any{"request_id": uid(), "expected_revision": diagnosis["observed_revision"], "action": "restart"}
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 409)
	body["action"] = "reconnect"
	expect(t, f.call(f.agent, "POST", v.path+"/recover-agent", body, v.lease), 403)
	expect(t, f.call(f.foreign, "POST", v.path+"/recover-agent", body, ""), 404)
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	if len(v.claim(t)) != 0 {
		t.Fatal("revoked attached recovery reached daemon")
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	body["request_id"] = uid()
	expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
	claimed := v.claim(t)
	if len(claimed) != 1 || claimed[0]["run_id"] != nil {
		t.Fatalf("attached claim=%v", claimed)
	}
	endpoint := "/api/harness-recoveries/" + body["request_id"].(string) + "/complete"
	completion := map[string]any{"daemon_id": v.identity.DaemonID, "generation": v.identity.Generation, "outcome": "reconnected"}
	expect(t, v.runtimeCall(t, endpoint, completion), 409)
	expect(t, v.runtimeCall(t, v.path+"/heartbeat", beat, v.lease), 200)
	// The paired hook uses the existing session-scoped replayable drain.
	messageID := uid()
	for _, id := range []string{uid(), messageID} {
		f.tx(t, f.person, func(tx pgx.Tx) error {
			var target any
			if id == messageID {
				target = v.session
			}
			event, err := events.Append(t.Context(), tx, f.person, events.Change{Type: "inbox.sent", After: map[string]any{"id": id}})
			if err != nil {
				return err
			}
			_, err = tx.Exec(t.Context(), `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key,recipient_session_id) VALUES($1,$2,$3,$4,$5,'attached fixture message',$6,$7)`, f.person.TenantID, id, f.person.ID, f.agent.ID, event.ID, uid(), target)
			return err
		})
	}
	var deliveryID any
	for round := 0; round < 2; round++ {
		drain := v.runtimeCall(t, v.path+"/drain", map[string]any{}, v.lease)
		expect(t, drain, 200)
		var deliveries []map[string]any
		if err := json.Unmarshal(drain.Body.Bytes(), &deliveries); err != nil || len(deliveries) != 1 || deliveries[0]["message_id"] != messageID {
			t.Fatal("attached drain consumed a broadcast or lost its exact message")
		}
		if round == 0 {
			deliveryID = deliveries[0]["delivery_id"]
		} else if deliveryID != deliveries[0]["delivery_id"] {
			t.Fatal("reconnect did not preserve the replayable delivery")
		}
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var acked bool
		if err := tx.QueryRow(t.Context(), `SELECT acked_at IS NOT NULL FROM inbox_messages WHERE id=$1`, messageID).Scan(&acked); err != nil {
			return err
		}
		if acked {
			t.Fatal("reconnect acknowledged a message before foreground handoff")
		}
		return nil
	})
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "viewer")
	expect(t, v.runtimeCall(t, endpoint, completion), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	w := v.runtimeCall(t, endpoint, completion)
	expect(t, w, 200)
	if got := decode(t, w); got["outcome"] != "reconnected" || got["next_run_id"] != nil {
		t.Fatalf("attached completion=%v", got)
	}
	expect(t, v.runtimeCall(t, endpoint, completion), 200)
}
