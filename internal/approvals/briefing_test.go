// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"encoding/json"
	"testing"
)

func TestBriefingPendingApprovalsBeforeLimitRegression(t *testing.T) {
	f := newFixture(t)
	// A pending request older than a full page of decided history must survive.
	var pending string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,agent_principal_id,proposed_by_principal_id,scope,resource_kind,rationale,expires_at,proposed_at) VALUES($1,$2,$2,'nodes.read','tenant','old pending',now()+interval '1 hour',now()-interval '1 day') RETURNING id::text`, f.tenantA, f.agentA.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `WITH requests AS (
 INSERT INTO approval_requests(tenant_id,agent_principal_id,proposed_by_principal_id,scope,resource_kind,rationale,expires_at)
 SELECT $1,$2,$2,'nodes.read','tenant','decided history',now()+interval '1 hour' FROM generate_series(1,201) RETURNING id,tenant_id)
 INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) SELECT tenant_id,id,$3,'denied' FROM requests`, f.tenantA, f.agentA.ID, f.personA.ID); err != nil {
		t.Fatal(err)
	}
	w := f.do(f.personA, "", "GET", "/api/approvals?pending=true&limit=200", "")
	var rows []Approval
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(rows) != 1 || rows[0].ID != pending {
		t.Fatalf("pending lost behind history: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.personA, "", "GET", "/api/approvals?pending=bad", ""); w.Code != 400 {
		t.Fatal("invalid pending accepted")
	}
}
