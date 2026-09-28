// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestRemoveAnyStateAndFenceGeneration(t *testing.T) {
	for _, state := range []string{"unmanaged", "legacy-managed-offline", "pending-force", "claimed-force", "stale-revision", "stopped"} {
		t.Run(state, func(t *testing.T) {
			f := fixture(t)
			base := "/api/projects/" + f.project + "/harness-sessions"
			lease := "remove-lease-" + uid()
			registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "offline-host", "harness_session_ref": "remove-ref-" + uid(), "worker_lease": lease, "management_mode": "unmanaged", "role": "worker", "ticket_node_id": f.ticket, "work_shape": "ship"}
			if state == "legacy-managed-offline" || strings.Contains(state, "force") {
				order, run := stateRun(t, f, f.project, "running", "RMV-10")
				registration["management_mode"], registration["run_id"], registration["work_order_id"], registration["advertised_capabilities"] = "managed", run, order, []string{"stop"}
			}
			w := f.call(f.person, "POST", base, registration, "")
			expect(t, w, 201)
			id := decode(t, w)["id"].(string)
			path := base + "/" + id
			var control string
			beat := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "activity_note": "Retain this history"}
			if strings.Contains(state, "force") {
				beat["process_ownership"] = map[string]any{"daemon_id": "test-owned-daemon", "generation": strings.Repeat("1", 32), "process_id": strings.Repeat("2", 32), "root_pid": 1234, "group_id": 1234, "started_at": "2026-01-01T10:00:00Z"}
			}
			expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 200)
			if strings.Contains(state, "force") {
				preview := decode(t, f.call(f.person, "GET", path+"/recovery", nil, ""))
				w = f.call(f.person, "POST", path+"/controls/force-stop", map[string]any{"request_id": uid(), "expected_revision": preview["observed_revision"], "confirmation": preview["force_confirmation"], "reason": "Explicit test authorization"}, "")
				expect(t, w, 201)
				control = decode(t, w)["id"].(string)
				if state == "claimed-force" {
					expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
				}
			}
			if state == "stale-revision" {
				// The old recovery observation is stale, but removal needs no client CAS.
				old := decode(t, f.call(f.person, "GET", path+"/recovery", nil, ""))
				beat["activity_sequence"], beat["display_label"] = 2, "changed after observation"
				expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 200)
				fresh := decode(t, f.call(f.person, "GET", path+"/recovery", nil, ""))
				if old["observed_revision"] == fresh["observed_revision"] {
					t.Fatal("test did not stale the observation")
				}
			}
			if state == "stopped" {
				expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "process_exited"}, lease), 200)
			}
			body := map[string]any{"reason": "Remove ghost registration"}
			expect(t, f.call(f.agent, "POST", path+"/remove", body, lease), 403)
			expect(t, f.call(f.foreign, "POST", path+"/remove", body, ""), 404)
			// Project members can remove records without process-termination permission.
			dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "member")
			w = f.call(f.person, "POST", path+"/remove", body, "")
			expect(t, w, 200)
			result := decode(t, w)
			removed := result["session"].(map[string]any)
			if removed["archived_at"] == nil || removed["ticket_node_id"] != f.ticket || result["processes_signalled"] != false || result["process_state"] != "unknown" {
				t.Fatalf("removal facts: %v", result)
			}
			body["reason"] = "Retry from another tab"
			w = f.call(f.person, "POST", path+"/remove", body, "")
			expect(t, w, 200)
			if decode(t, w)["session"].(map[string]any)["archived_at"] != removed["archived_at"] {
				t.Fatal("retry changed receipt")
			}
			expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 410)
			expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "process_exited"}, lease), 410)
			expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 410)
			if control != "" {
				expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "owned_group_signalled_root_exited"}, lease), 410)
				c := decode(t, f.call(f.person, "GET", path+"/controls/"+control, nil, ""))
				if c["state"] != "completed" || c["outcome"] != "rejected" {
					t.Fatalf("control survived: %v", c)
				}
			}
			detail := decode(t, f.call(f.person, "GET", path, nil, ""))
			if len(detail["activity_history"].([]any)) != 1 {
				t.Fatal("lost history")
			}
			expect(t, f.call(f.person, "POST", base, registration, ""), 409)
			// Live/inactive tiles must not resurrect removed registrations.
			w = f.call(f.person, "GET", "/api/harness-sessions/live?include_inactive=true", nil, "")
			expect(t, w, 200)
			if strings.Contains(w.Body.String(), id) {
				t.Fatal("removed session in live feed")
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var count int
				err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.removed' AND actor_principal_id=$1 AND "after"->'session'->>'id'=$2 AND "after"->>'reason'='Remove ghost registration'`, f.person.ID, id).Scan(&count)
				if err == nil && count != 1 {
					t.Fatalf("removal audit events %d", count)
				}
				return err
			})
		})
	}
}

func TestRemoveRevokesRulesReceiptReplay(t *testing.T) {
	f, path, lease, receipt := receiptFixture(t)
	expect(t, f.call(f.agent, "POST", path+"/rules-receipts", receipt, lease), 200)
	expect(t, f.call(f.person, "POST", path+"/remove", map[string]any{"reason": "Ghost receipt producer"}, ""), 200)
	expect(t, f.call(f.agent, "POST", path+"/rules-receipts", receipt, lease), 410)
	w := f.call(f.person, "GET", path+"/rules-receipts", nil, "")
	expect(t, w, 200)
	if len(decode(t, w)["receipts"].([]any)) != 1 {
		t.Fatal("receipt history lost")
	}
}

func TestRemoveStaleUsesAcceptedHeartbeatAndIsIdempotent(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	staleIDs := map[string]bool{}
	for _, state := range []string{"old-beat", "never-beat", "old-stopped", "recent-beat", "recent-stopped", "recent-created"} {
		w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test", "harness_session_ref": "batch-ref-" + uid(), "worker_lease": "batch-lease-" + uid(), "management_mode": "unmanaged", "role": "worker"}, "")
		expect(t, w, 201)
		id := decode(t, w)["id"].(string)
		stale := state == "old-beat" || state == "never-beat" || state == "old-stopped"
		if stale {
			staleIDs[id] = true
		}
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET created_at=clock_timestamp()-interval '1 hour',heartbeat_at=CASE WHEN $2 THEN clock_timestamp()-interval '16 minutes' ELSE clock_timestamp() END,stopped_at=CASE WHEN $3 THEN clock_timestamp() END,phase=CASE WHEN $3 THEN 'stopped' ELSE phase END WHERE id=$1`, id, stale, strings.HasSuffix(state, "stopped"))
			if err != nil {
				return err
			}
			if state == "never-beat" || state == "recent-created" {
				_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=NULL,created_at=CASE WHEN $2 THEN clock_timestamp() ELSE created_at END WHERE id=$1`, id, state == "recent-created")
			}
			return err
		})
	}
	body := map[string]any{"reason": "Batch ghost cleanup"}
	expect(t, f.call(f.agent, "POST", base+"/remove-stale", body, ""), 403)
	expect(t, f.call(f.foreign, "POST", base+"/remove-stale", body, ""), 404)
	w := f.call(f.person, "POST", base+"/remove-stale", body, "")
	expect(t, w, 200)
	items := decode(t, w)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("removed %d, want 3", len(items))
	}
	for _, item := range items {
		if !staleIDs[item.(map[string]any)["session"].(map[string]any)["id"].(string)] {
			t.Fatal("removed a fresh session")
		}
	}
	w = f.call(f.person, "POST", base+"/remove-stale", body, "")
	expect(t, w, 200)
	if len(decode(t, w)["items"].([]any)) != 0 {
		t.Fatal("batch retry removed records twice")
	}
}
