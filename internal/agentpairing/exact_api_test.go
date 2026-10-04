// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestExactAPIConnectOnlySixAndSevenAccounts(t *testing.T) {
	for _, count := range []int{6, 7} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newFixture(t)
			err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
				for _, h := range []string{"gemini", "opencode"} {
					var id string
					if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'1',$2,'google','test-model','low','fast') RETURNING id::text`, f.tenantID, h).Scan(&id); err != nil {
						return err
					}
					f.profiles[h] = id
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			harnesses := []string{"claude", "codex", "cursor", "grok", "pi", "gemini", "opencode"}
			p := f.propose(harnesses[:count]...)
			f.approve(p, "connect_only")
			v := f.redeem(p)
			if len(v.Enrollments) != count {
				t.Fatalf("enrolled %d, want %d", len(v.Enrollments), count)
			}
			for _, enrollment := range v.Enrollments {
				if enrollment.VerificationState == "queued" {
					t.Fatal("Connect only queued verification")
				}
			}
			var reviewed agentpairing.View
			decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &reviewed)
			if len(reviewed.Enrollments) != count {
				t.Fatal("person view lost selected accounts")
			}
		})
	}
}
