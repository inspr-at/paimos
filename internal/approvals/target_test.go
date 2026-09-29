// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"encoding/json"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestOptionalDeployTargetRoundTrip(t *testing.T) {
	f := newFixture(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['run','journey','stage','nodes.read','approvals.read','harness.read'] WHERE principal_id=$1::uuid`, f.agentA.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"journey.deploy", "stage.deploy"} {
		for _, named := range []bool{false, true} {
			var body map[string]any
			if err := json.Unmarshal([]byte(proposalJSON(scope, "node", &f.nodeA, nil)), &body); err != nil {
				t.Fatal(err)
			}
			if named {
				body["target"] = map[string]any{"hosts": []string{"edge-2", "edge-1"}, "environment": "production", "service": "aeon", "change": "Update image"}
			}
			raw, _ := json.Marshal(body)
			response := f.do(f.agentA, f.wide, "POST", "/api/approvals", string(raw))
			if response.Code != 201 {
				t.Fatalf("%s named=%v: %d %s", scope, named, response.Code, response.Body.String())
			}
			a := decodeApproval(t, response)
			if named && (a.Target == nil || a.Target.Hosts[0] != "edge-1" || len(a.TargetDigestSHA256) != 64) {
				t.Fatalf("lost target: %+v", a)
			}
			if !named && (a.Target != nil || a.TargetDigestSHA256 != "") {
				t.Fatal("invented target")
			}
			response = f.do(f.personA, "", "POST", "/api/approvals/"+a.ID+"/decision", `{"decision":"approved"}`)
			if response.Code != 200 {
				t.Fatalf("decision: %d %s", response.Code, response.Body.String())
			}
			got := decodeApproval(t, response)
			if got.TargetDigestSHA256 != a.TargetDigestSHA256 {
				t.Fatal("decision lost target")
			}
			if named {
				err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
					var count int
					err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE after->>'id'=$1 AND after->>'target_digest_sha256'=$2`, a.ID, a.TargetDigestSHA256).Scan(&count)
					if err == nil && count < 2 {
						t.Errorf("target missing from proposed/decided audit: %d", count)
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	response := f.do(f.personA, "", "GET", "/api/approvals", "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var items []Approval
	if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("list lost approvals: %d", len(items))
	}
}
