// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/jackc/pgx/v5"
)

// Risk: even an exact lead-runtime pickup replay must not hand back launch
// authority after the bound run person's computer grant has been revoked.
func TestComputerStartLeadRuntimeRechecksRunPerson(t *testing.T) {
	v := recoveryAgent(t)
	f := v.f
	f.agent.Scopes = append(f.agent.Scopes, "run.claim")
	person, recurrence, routine, launchID := uid(), uid(), uid(), uid()
	var computer string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=array_append(scopes,'run.claim') WHERE id=$1`, f.agent.AuthKeyID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM agent_pairing_computers WHERE principal_id=$1`, f.agent.ID).Scan(&computer)
	})
	demand := agentsetup.LeadDemand{ID: launchID, ProjectID: f.project, RoutineRunID: routine, OwnerID: f.person.ID, Revision: 1}
	pickup := agentsetup.LeadPickup{Demand: demand, AccountID: v.account, ComputerID: computer, LedgerGeneration: uid(), DaemonID: v.identity.DaemonID, DaemonGeneration: v.identity.Generation, DaemonDigest: strings.Repeat("a", 64), CapabilityDigest: strings.Repeat("b", 64), Capability: agentsetup.LeadExecutionCapability, WorktreeID: strings.Repeat("c", 64), Ref: strings.Repeat("d", 32), Lease: strings.Repeat("e", 32)}
	launch := agentsetup.LeadLaunch{Demand: demand, SessionID: v.session, Generation: 1, Grant: agentsetup.RoutineGrant{AgentRunID: v.run, PrincipalID: f.agent.ID, OwnerID: f.person.ID, DaemonID: pickup.DaemonID, DaemonGeneration: pickup.DaemonGeneration, LedgerGeneration: pickup.LedgerGeneration, AccountID: pickup.AccountID, ComputerID: pickup.ComputerID, DaemonDigest: pickup.DaemonDigest, CapabilityDigest: pickup.CapabilityDigest}}
	proof := func(kind, value string) []byte {
		sum := sha256.Sum256([]byte("aeon.harness." + kind + "\x00" + value))
		return sum[:]
	}
	binding, err := json.Marshal(map[string]any{"launch": launch, "worktree_id": pickup.WorktreeID, "ref_digest": proof("ref", pickup.Ref), "lease_digest": proof("lease", pickup.Lease)})
	if err != nil {
		t.Fatal(err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Lead run person')`, []any{f.person.TenantID, person}},
			{`INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,computer_id) SELECT $1,$2,id,'computer',$3 FROM roles WHERE key='admin'`, []any{f.person.TenantID, person, computer}},
			{`UPDATE agent_runs SET acting_person_id=$2 WHERE id=$1`, []any{v.run, person}},
			{`INSERT INTO recurrences(tenant_id,id,project_id,parent_id,template,trigger,created_by_principal_id) VALUES($1,$2,$3,$3,'{}','{"kind":"event"}',$4)`, []any{f.person.TenantID, recurrence, f.project, f.person.ID}},
			{`INSERT INTO recurrence_definitions(tenant_id,recurrence_id,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id) VALUES($1,$2,'project',$3,$4,$3,$3)`, []any{f.person.TenantID, recurrence, f.project, f.person.ID}},
			{`INSERT INTO recurrence_occurrences(tenant_id,recurrence_id,occurrence_key,number,scheduled_at,node_id,outcome) VALUES($1,$2,'computer-start',1,clock_timestamp(),$3,'created')`, []any{f.person.TenantID, recurrence, f.project}},
			{`INSERT INTO routine_runs(tenant_id,id,recurrence_id,occurrence_key,definition_revision,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment,assignment_digest,policy_snapshot,policy_digest,work_node_id,source_receipt) VALUES($1,$2,$3,'computer-start',1,'project',$4,$5,$4,$4,'{}',repeat('a',64),'{}',repeat('b',64),$4,'{}')`, []any{f.person.TenantID, routine, recurrence, f.project, f.person.ID}},
			{`INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state,revision) VALUES($1,$2,$3,$4,1,'starting',2)`, []any{f.person.TenantID, f.project, f.person.ID, v.session}},
			{`INSERT INTO routine_launch_intents(tenant_id,id,routine_run_id,agent_run_id,project_id,owner_principal_id,worker_principal_id,kind,lead_generation,binding,state,worktree_id) VALUES($1,$2,$3,$4,$5,$6,$7,'lead',0,$8,'claimed',$9)`, []any{f.person.TenantID, launchID, routine, v.run, f.project, f.person.ID, f.agent.ID, binding, pickup.WorktreeID}},
		} {
			if _, err := tx.Exec(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	path := "/api/projects/" + f.project + "/lead-runtime/claim"
	// This synthetic replay is not a computer runtime resource grant. Preserve
	// that outer ceiling, then exercise the real scoped-key handler transaction
	// through the same seam as the existing lead-runtime replay tests.
	outer := v.runtimeCall(t, path, pickup)
	expect(t, outer, 403)
	if !strings.Contains(outer.Body.String(), "outside this computer's runtime authority") {
		t.Fatal("synthetic replay escaped or failed a different runtime boundary")
	}
	expect(t, f.call(f.agent, "POST", path, pickup, ""), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='computer'`, person)
		return err
	})
	w := f.call(f.agent, "POST", path, pickup, "")
	expect(t, w, 403)
	if !strings.Contains(w.Body.String(), authz.ErrForbidden.Error()) {
		t.Fatalf("lead runtime refused for another reason: %s", w.Body.String())
	}
}

// Risk: recovery authority is checked for its requester but the run's person
// has lost permission on the executing daemon's computer in the meantime.
func TestComputerStartRecoveryRechecksRunPerson(t *testing.T) {
	for _, mode := range []string{"granted", "revoked", "workspace_admin", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			v := recoveryAgent(t)
			f := v.f
			person := uid()
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Run person')`, f.person.TenantID, person); err != nil {
					return err
				}
				if mode == "workspace_admin" {
					person = f.person.ID
				} else if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,computer_id) SELECT $1,$2,r.id,'computer',c.id FROM roles r CROSS JOIN agent_pairing_computers c WHERE r.key='admin' AND c.principal_id=$3`, f.person.TenantID, person, f.agent.ID); err != nil {
					return err
				}
				if mode != "legacy" {
					_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET acting_person_id=$2 WHERE id=$1`, v.run, person)
					return err
				}
				return nil
			})
			body := v.request(t)
			expect(t, f.call(f.person, "POST", v.path+"/recover-agent", body, ""), 201)
			if mode == "revoked" || mode == "legacy" {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='computer'`, person)
					return err
				})
			}
			if mode != "revoked" {
				if got := v.claim(t); len(got) != 1 || got[0]["id"] != body["request_id"] {
					t.Fatalf("claim=%v", got)
				}
				return
			}
			w := v.runtimeCall(t, "/api/harness-recoveries/claim", map[string]any{"daemon_id": v.identity.DaemonID, "generation": v.identity.Generation})
			expect(t, w, 403)
			if !strings.Contains(w.Body.String(), authz.ErrForbidden.Error()) {
				t.Fatalf("recovery refused for another reason: %s", w.Body.String())
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var state string
				if err := tx.QueryRow(t.Context(), `SELECT state FROM harness_recoveries WHERE id=$1`, body["request_id"]).Scan(&state); err != nil {
					return err
				}
				if state != "pending" {
					t.Fatalf("refusal changed recovery state: %s", state)
				}
				return nil
			})
		})
	}
}
