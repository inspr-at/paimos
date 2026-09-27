// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestRecoveryAuthorizationFencingHistoryAndRetry(t *testing.T) {
	f := fixture(t)
	lease := "recovery-generation-lease-00000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "offline-host", "harness_session_ref": "recovery-ref-00000000000000000001", "worker_lease": lease, "management_mode": "unmanaged", "role": "worker", "display_label": "stale-worker", "ticket_node_id": f.ticket, "work_shape": "ship"}
	w := f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	preview := func() map[string]any {
		t.Helper()
		w := f.call(f.person, "GET", path+"/recovery", nil, "")
		expect(t, w, 200)
		return decode(t, w)
	}
	request := func(v map[string]any) map[string]any {
		return map[string]any{"expected_revision": v["observed_revision"], "confirmation": v["confirmation"], "request_id": uid(), "reason": "Host offline; close registration with process state unknown"}
	}
	v := preview()
	if v["force_stop_available"] != false || v["process_state"] != "unknown" || !strings.Contains(v["confirmation"].(string), id) || !strings.Contains(v["confirmation"].(string), "offline-host") {
		t.Fatalf("unsafe preview: %v", v)
	}
	body := request(v)
	// No agent, including the registration owner, may grant itself recovery.
	expect(t, f.call(f.agent, "POST", path+"/archive", body, lease), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "member")
	expect(t, f.call(f.person, "GET", path+"/recovery", nil, ""), 403)
	expect(t, f.call(f.person, "POST", path+"/archive", body, ""), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	dbtest.BindRole(t, f.db, f.foreign.TenantID, f.foreign.ID, "admin")
	expect(t, f.call(f.foreign, "GET", path+"/recovery", nil, ""), 404)
	expect(t, f.call(f.foreign, "POST", path+"/archive", body, ""), 404)
	expect(t, f.call(f.person, "GET", "/api/projects/"+uid()+"/harness-sessions/"+id+"/recovery", nil, ""), 404)
	// A heartbeat or rename invalidates even an unchanged binding revision.
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "activity_note": "Testing recovery", "display_label": "renamed worker"}, lease), 200)
	expect(t, f.call(f.person, "POST", path+"/archive", body, ""), 409)
	body = request(preview())
	confirmation := body["confirmation"]
	body["confirmation"] = "archive the wrong session"
	expect(t, f.call(f.person, "POST", path+"/archive", body, ""), 400)
	body["confirmation"] = confirmation
	w = f.call(f.person, "POST", path+"/archive", body, "")
	expect(t, w, 200)
	archived := decode(t, w)
	if archived["archived_at"] == nil || archived["recovery_process_state"] != "unknown" || archived["stop_reason"] != "archived_process_unknown" || archived["ticket_node_id"] != f.ticket {
		t.Fatalf("archive facts: %v", archived)
	}
	// Network retry returns the same record and emits no duplicate event.
	w = f.call(f.person, "POST", path+"/archive", body, "")
	expect(t, w, 200)
	if decode(t, w)["archived_at"] != archived["archived_at"] {
		t.Fatal("archive replay changed receipt")
	}
	body["reason"] = "different reason"
	expect(t, f.call(f.person, "POST", path+"/archive", body, ""), 409)
	for _, endpoint := range []string{"heartbeat", "yield", "drain", "stop"} {
		req := map[string]any{}
		if endpoint == "heartbeat" {
			req = map[string]any{"phase": "working", "activity_sequence": 2}
		}
		if endpoint == "stop" {
			req = map[string]any{"reason": "process_exited"}
		}
		expect(t, f.call(f.agent, "POST", path+"/"+endpoint, req, lease), 410)
	}
	expect(t, f.call(f.person, "POST", base, registration, ""), 409)
	registration["harness_session_ref"] = "recovery-new-ref-0000000000000001"
	expect(t, f.call(f.person, "POST", base, registration, ""), 409)
	registration["worker_lease"] = "recovery-new-lease-0000000000000001"
	expect(t, f.call(f.person, "POST", base, registration, ""), 201)
	detail := decode(t, f.call(f.person, "GET", path, nil, ""))
	if len(detail["activity_history"].([]any)) != 1 || len(detail["metadata_history"].([]any)) != 1 {
		t.Fatal("archive lost history")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.archived' AND "after"->'session'->>'id'=$1`, id).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("archive events %d", count)
		}
		return nil
	})
}

func TestRecoveryClosesControlsAndPreservesOtherGenerations(t *testing.T) {
	f := fixture(t)
	order, run := stateRun(t, f, f.project, "running", "RCV-10")
	base := "/api/projects/" + f.project + "/harness-sessions"
	register := func(ref, lease string) string {
		t.Helper()
		w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host", "harness_session_ref": ref, "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "advertised_capabilities": []string{"stop", "interrupt"}}, "")
		expect(t, w, 201)
		return decode(t, w)["id"].(string)
	}
	lease := "recovery-managed-lease-0000000000001"
	id := register("recovery-managed-ref-00000000000001", lease)
	path := base + "/" + id
	otherLease := "recovery-other-lease-00000000000001"
	other := register("recovery-other-ref-0000000000000001", otherLease)
	unverified := decode(t, f.call(f.person, "GET", path+"/recovery", nil, ""))
	if unverified["can_archive"] != false {
		t.Fatal("legacy managed archive could indirectly stop child")
	}
	expect(t, f.call(f.person, "POST", path+"/archive", map[string]any{"expected_revision": unverified["observed_revision"], "confirmation": unverified["confirmation"], "request_id": uid(), "reason": "Must not make legacy daemon stop"}, ""), 409)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "process_ownership": map[string]any{"daemon_id": "recovery-aware-daemon", "generation": strings.Repeat("1", 32), "process_id": strings.Repeat("2", 32), "root_pid": 1234, "group_id": 1234, "started_at": "2026-01-01T10:00:00Z"}}, lease), 200)

	w := f.call(f.person, "POST", path+"/controls/stop", map[string]any{}, "")
	expect(t, w, 201)
	control := decode(t, w)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 200)
	v := decode(t, f.call(f.person, "GET", path+"/recovery", nil, ""))
	w = f.call(f.person, "POST", path+"/archive", map[string]any{"expected_revision": v["observed_revision"], "confirmation": v["confirmation"], "request_id": uid(), "reason": "Close lost ownership registration"}, "")
	expect(t, w, 200)
	c := decode(t, f.call(f.person, "GET", path+"/controls/"+control, nil, ""))
	if c["outcome"] != "rejected" || c["state"] != "completed" {
		t.Fatalf("pending control survived: %v", c)
	}
	expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "late_completion"}, lease), 410)
	expect(t, f.call(f.person, "POST", path+"/controls/stop", map[string]any{}, ""), 409)
	expect(t, f.call(f.agent, "POST", base+"/"+other+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, otherLease), 200)
}

func TestManagedForceStopHumanConfirmationAndGenerationFences(t *testing.T) {
	f := fixture(t)
	order, run := stateRun(t, f, f.project, "running", "HTS-3")
	lease := "force-managed-lease-000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "verified-host", "harness_session_ref": "force-managed-ref-000000000000001", "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": f.ticket, "work_shape": "ship", "advertised_capabilities": []string{"stop"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	preview := func() map[string]any {
		t.Helper()
		w := f.call(f.person, "GET", path+"/recovery", nil, "")
		expect(t, w, 200)
		return decode(t, w)
	}
	if preview()["force_stop_available"] != false {
		t.Fatal("unverified process was forceable")
	}
	identity := map[string]any{"daemon_id": "local-test-daemon", "generation": "11111111111111111111111111111111", "process_id": "22222222222222222222222222222222", "root_pid": 1234, "group_id": 1234, "started_at": "2026-01-01T10:00:00Z"}
	beat := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1, "process_ownership": identity}
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 200)
	v := preview()
	if v["force_stop_available"] != true {
		t.Fatalf("verified process unavailable: %v", v)
	}
	// Routine heartbeats do not invalidate the human's identity confirmation.
	beat["activity_sequence"] = 2
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 200)
	if preview()["observed_revision"] != v["observed_revision"] {
		t.Fatal("routine heartbeat starves confirmation")
	}
	body := map[string]any{"expected_revision": v["observed_revision"], "confirmation": v["force_confirmation"], "request_id": uid(), "reason": "Explicit test force-stop authorization"}
	expect(t, f.call(f.agent, "POST", path+"/controls/force-stop", body, lease), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "member")
	expect(t, f.call(f.person, "POST", path+"/controls/force-stop", body, ""), 403)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	valid := body["confirmation"]
	body["confirmation"] = "force stop other process"
	expect(t, f.call(f.person, "POST", path+"/controls/force-stop", body, ""), 409)
	body["confirmation"] = valid
	// A daemon restart or different child cannot mutate the same generation.
	identity["process_id"] = "33333333333333333333333333333333"
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 409)
	identity["process_id"] = "22222222222222222222222222222222"
	w = f.call(f.person, "POST", path+"/controls/force-stop", body, "")
	expect(t, w, 201)
	control := decode(t, w)
	if control["kind"] != "force_stop" || control["expires_at"] == nil || control["expected_ownership"].(map[string]any)["process_id"] != identity["process_id"] {
		t.Fatalf("unfenced force control: %v", control)
	}
	retry := f.call(f.person, "POST", path+"/controls/force-stop", body, "")
	expect(t, retry, 201)
	if decode(t, retry)["id"] != control["id"] {
		t.Fatal("force retry duplicated command")
	}

	fresh := preview()
	expect(t, f.call(f.person, "POST", path+"/archive", map[string]any{"expected_revision": fresh["observed_revision"], "confirmation": fresh["confirmation"], "request_id": uid(), "reason": "Cannot revoke force already handed to daemon"}, ""), 409)
	original := body["request_id"]
	body["request_id"] = uid()
	expect(t, f.call(f.person, "POST", path+"/controls/force-stop", body, ""), 409)
	body["request_id"] = original
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	claimed := decode(t, w)["controls"].([]any)
	if len(claimed) != 1 {
		t.Fatal("force command missing or duplicated")
	}
	completePath := path + "/controls/" + control["id"].(string) + "/complete"
	expect(t, f.call(f.agent, "POST", completePath, map[string]any{"outcome": "applied", "reason": "accepted"}, lease), 400)
	expect(t, f.call(f.agent, "POST", completePath, map[string]any{"outcome": "applied", "reason": "owned_group_signalled_root_exited"}, lease), 200)
	// Offline ownership cannot authorize another force, even while the public
	// registration is active and the process snapshot is still stored.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET process_observed_at=clock_timestamp()-interval '2 minutes' WHERE id=$1`, id)
		return err
	})
	v = preview()
	if v["force_stop_available"] != false {
		t.Fatal("offline process remained forceable")
	}
	body["request_id"] = uid()
	body["expected_revision"] = v["observed_revision"]
	expect(t, f.call(f.person, "POST", path+"/controls/force-stop", body, ""), 409)
}

func TestRecoveryProjectPermissionDoesNotReachAnotherProject(t *testing.T) {
	f := fixture(t)
	second, role := uid(), uid()
	recoveryPerson := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'RCV-3',kind_id,'Other project' FROM nodes WHERE id=$3`, []any{f.person.TenantID, second, f.project}},
			{`INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Recovery operator')`, []any{f.person.TenantID, recoveryPerson.ID}},
			{`INSERT INTO roles(tenant_id,id,key,name) VALUES($1,$2,'recovery_operator','Recovery operator')`, []any{f.person.TenantID, role}},
			{`INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','harness.read','harness.recover'])`, []any{f.person.TenantID, role}},
			{`INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, []any{f.person.TenantID, recoveryPerson.ID, role, f.project}},
		} {
			if _, err := tx.Exec(t.Context(), statement.sql, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
	for _, project := range []string{second, f.project} {
		base := "/api/projects/" + project + "/harness-sessions"
		w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "recovery-project-ref-" + uid(), "worker_lease": "recovery-project-lease-" + uid()}, "")
		expect(t, w, 201)
		path := base + "/" + decode(t, w)["id"].(string)
		v := decode(t, f.call(f.person, "GET", path+"/recovery", nil, ""))
		body := map[string]any{"expected_revision": v["observed_revision"], "confirmation": v["confirmation"], "request_id": uid(), "reason": "Authorized project recovery"}
		if project == second {
			expect(t, f.call(recoveryPerson, "GET", path+"/recovery", nil, ""), 403)
			expect(t, f.call(recoveryPerson, "POST", path+"/archive", body, ""), 403)
		} else {
			expect(t, f.call(recoveryPerson, "GET", path+"/recovery", nil, ""), 200)
			expect(t, f.call(recoveryPerson, "POST", path+"/controls/force-stop", body, ""), 403)
			expect(t, f.call(recoveryPerson, "POST", path+"/archive", body, ""), 200)
		}
	}
}
