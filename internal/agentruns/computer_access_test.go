// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: a person can create on an ungranted computer, or a queued run keeps
// start authority after revocation. NULL actors must preserve legacy claims.
func TestComputerStartCreationAndClaimUseCurrentPersonGrant(t *testing.T) {
	f := setup(t)
	// Keep the ordinary reservation/claim fixture and add paired computers to
	// its daemon principals without changing account-use admission.
	f.key(t, f.other, []string{"run.claim"})
	computers := []string{uuid(), uuid()}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for i, principal := range []string{f.agent.ID, f.other.ID} {
			request, fingerprint := uuid(), strings.ReplaceAll(uuid()+uuid(), "-", "")
			if _, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,$2,$3,$4,$4,$4,'{"platform":"darwin","arch":"arm64"}',$4,'redeemed',$5)`, f.person.TenantID, request, []string{"123456789", "123456780"}[i], fingerprint, f.person.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash) SELECT $1,$2,$3,$4,id,$5,$6 FROM agent_keys WHERE principal_id=$4 ORDER BY created_at LIMIT 1`, f.person.TenantID, computers[i], request, principal, []string{"daemon-test", "daemon-other"}[i], fingerprint); err != nil {
				return err
			}
		}
		return nil
	})
	o := f.order(t, nil)
	// Owner's Go template supplies start permission for A and B.
	adminRun := f.run(t, o)
	if adminRun.ActingPersonID == nil || *adminRun.ActingPersonID != f.person.ID {
		t.Fatal("personal start lost its person")
	}
	otherOrder := f.order(t, nil)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_orders SET assignee_principal_id=$2 WHERE node_id=$1`, otherOrder.NodeID, f.other.ID)
		return err
	})
	otherPath := "/api/work-orders/" + otherOrder.NodeID + "/runs"
	f.call(t, f.person, "POST", otherPath, map[string]any{"agent_principal_id": f.other.ID, "model_profile_id": f.profile}, 201, nil)
	dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "member")
	path := "/api/work-orders/" + o.NodeID + "/runs"
	body := map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}
	f.call(t, f.person, "POST", path, body, 403, nil)
	grant := func(computer string, add bool) {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			if !add {
				_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='computer' AND computer_id=$2`, f.person.ID, computer)
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,computer_id) SELECT $1,$2,id,'computer',$3 FROM roles WHERE key='admin' ON CONFLICT DO NOTHING`, f.person.TenantID, f.person.ID, computer)
			return err
		})
	}
	grant(computers[0], true)
	var created agentruns.Run
	f.call(t, f.person, "POST", path, body, 201, &created)
	body["agent_principal_id"] = f.other.ID
	f.call(t, f.person, "POST", otherPath, body, 403, nil)
	for _, mode := range []string{"granted", "revoked", "other_computer", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			grant(computers[0], true)
			run := f.run(t, f.order(t, nil))
			if mode == "legacy" {
				// Model a pre-expansion writer without changing a bound run's actor.
				f.tx(t, f.person, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id) SELECT tenant_id,work_order_id,agent_principal_id,model_profile_id FROM agent_runs WHERE id=$1 RETURNING id::text`, run.ID).Scan(&run.ID)
				})
			}
			ids := f.reserve(t, run)
			if mode != "granted" {
				grant(computers[0], false)
			}
			if mode == "other_computer" {
				grant(computers[1], true)
			}
			status := 200
			if mode == "revoked" || mode == "other_computer" {
				status = 403
			}
			raw, err := json.Marshal(claimBody(ids))
			if err != nil {
				t.Fatal(err)
			}
			w := f.request(f.agent, "POST", "/api/runs/"+run.ID+"/claim", string(raw), f.token)
			if w.Code != status || status == 403 && !strings.Contains(w.Body.String(), "permission_revoked") {
				t.Fatalf("%s claim: %d %s", mode, w.Code, w.Body.String())
			}
		})
	}
}
