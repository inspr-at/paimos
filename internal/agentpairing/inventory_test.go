// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// Risk: adoption picks an admin from conflicting provenance, or an inventory
// response leaks transport credentials and hides incomplete evidence.
func TestComputerInventoryReportsEvidenceWithoutAssigningOwner(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	path := "/api/agent-pairing/computers/inventory"
	var initial agentpairing.ComputerInventory
	decodeResult(t, f.call("GET", path, nil, true, "", 200), &initial)
	if len(initial.Computers) != 1 || initial.Computers[0].Owner != "unconfirmed" || initial.Computers[0].ApprovedBy == nil || *initial.Computers[0].ApprovedBy != f.person {
		t.Fatal("consistent evidence must remain unconfirmed")
	}
	var other string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Enrollment owner') RETURNING id::text`, f.tenantID).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=clock_timestamp() WHERE id=$1`, v.Enrollments[0].AccountID, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_keys SET owner_workstation=true,workstation_computer_id=$2 WHERE id=(SELECT key_id FROM agent_pairing_computers WHERE id=$2) AND tenant_id=$1`, f.tenantID, *v.ComputerID); err != nil {
		t.Fatal(err)
	}
	w := f.call("GET", path+"?limit=1", nil, true, "", 200)
	var got agentpairing.ComputerInventory
	decodeResult(t, w, &got)
	if len(got.Computers) != 1 || got.Computers[0].Owner != "ambiguous" || len(got.Computers[0].WorkstationOwners) != 1 || len(got.Computers[0].Enrollments) != 1 || got.Computers[0].Enrollments[0].OwnerPersonID == nil || *got.Computers[0].Enrollments[0].OwnerPersonID != other {
		t.Fatal("conflicting evidence missing")
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	keys := func(value any, want ...string) {
		t.Helper()
		m, ok := value.(map[string]any)
		if !ok {
			t.Fatal("expected JSON object")
		}
		var names []string
		for k := range m {
			names = append(names, k)
		}
		slices.Sort(names)
		slices.Sort(want)
		if !reflect.DeepEqual(names, want) {
			t.Fatalf("inventory JSON keys %v, want %v", names, want)
		}
	}
	keys(body, "computers", "next_cursor")
	computer := body["computers"].([]any)[0].(map[string]any)
	keys(computer, "computer_id", "computer_name", "state", "owner", "approved_by", "workstation_owners", "enrollments", "enrollments_truncated")
	keys(computer["workstation_owners"].([]any)[0], "person_id", "created_by_principal_id")
	keys(computer["enrollments"].([]any)[0], "account_id", "state", "owner_person_id", "approved_by")
	for _, query := range []string{"?limit=0", "?limit=101", "?cursor=invalid"} {
		f.call("GET", path+query, nil, true, "", 400)
	}
	f.call("GET", path, nil, false, "", 401)
	// Real transport credentials cannot access this person-only operator report.
	f.call("GET", path, nil, false, "aeon_"+v.RuntimePrefix+"_"+p.runtime, 403)
	foreign := newFixtureInTenant(t, f.db, "inventory-other")
	var empty agentpairing.ComputerInventory
	decodeResult(t, foreign.call("GET", path, nil, true, "", 200), &empty)
	if len(empty.Computers) != 0 {
		t.Fatal("inventory crossed tenant boundary")
	}
	dbtest.BindRole(t, f.db, f.tenantID, f.person, "member")
	f.call("GET", path, nil, true, "", 403)
}
