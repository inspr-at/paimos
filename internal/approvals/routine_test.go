// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks R1/R17: one person decision approving another target/head, expired or
// revoked grants, self-approval, and a narrow hold widening ordinary authority.
func TestRoutineHoldIsExactPersonOwnedAndRevocable(t *testing.T) {
	f := newFixture(t)
	f.agentA.Scopes = append(f.agentA.Scopes, "nodes.write")
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	var hold Approval
	in := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
			if err := db.LockTree(t.Context(), tx, f.tenantA); err != nil {
				return err
			}
			return fn(tx)
		}); err != nil {
			t.Fatal(err)
		}
	}
	in(func(tx pgx.Tx) error {
		pending := []events.Change{}
		var err error
		hold, err = RequestRoutineHoldTx(t.Context(), tx, f.agentA, f.nodeA, "nodes.write", digest, now.Add(time.Hour), now, &pending)
		if err != nil {
			return err
		}
		replayed, err := RequestRoutineHoldTx(t.Context(), tx, f.agentA, f.nodeA, "nodes.write", digest, now.Add(time.Hour), now, &pending)
		if err != nil {
			return err
		}
		if replayed.ID != hold.ID || len(pending) != 1 || hold.Target != nil || hold.TargetDigestSHA256 != "" || hold.Scope != RoutineScope("nodes.write", digest) {
			t.Fatal("hold replay duplicated request/event")
		}
		if err = CanDecide(t.Context(), tx, f.agentA, hold); err == nil {
			t.Fatal("requesting agent could decide")
		}
		for _, event := range pending {
			if _, err := events.Append(t.Context(), tx, f.agentA, event); err != nil {
				return err
			}
		}
		return nil
	})
	if w := f.do(f.personA, "", "POST", "/api/approvals/"+hold.ID+"/decision", `{"decision":"approved"}`); w.Code != 200 {
		t.Fatalf("person approval: %d %s", w.Code, w.Body.String())
	}
	in(func(tx pgx.Tx) error {
		for _, check := range []struct {
			target, digest string
			want           bool
		}{{f.nodeA, digest, true}, {f.deleted, digest, false}, {f.nodeA, strings.Repeat("b", 64), false}} {
			live, err := LiveRoutineHoldTx(t.Context(), tx, f.agentA, hold.ID, check.target, "nodes.write", check.digest, now)
			if err != nil {
				return err
			}
			if live != check.want {
				t.Fatal("approval crossed exact target/action/head digest")
			}
		}
		live, err := LiveGrant(t.Context(), tx, f.agentA.ID, "nodes.write", "node", &f.nodeA, []string{"nodes.write"})
		if live {
			t.Fatal("routine hold enlarged ordinary mutation authority")
		}
		if err != nil {
			return err
		}
		live, err = LiveRoutineHoldTx(t.Context(), tx, f.agentA, hold.ID, f.nodeA, "nodes.write", digest, now.Add(2*time.Hour))
		if live {
			t.Fatal("expired person hold remained live")
		}
		return err
	})
	if w := f.do(f.personA, "", "POST", "/api/approvals/"+hold.ID+"/revoke", ""); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	in(func(tx pgx.Tx) error {
		live, err := LiveRoutineHoldTx(t.Context(), tx, f.agentA, hold.ID, f.nodeA, "nodes.write", digest, now)
		if live {
			t.Fatal("revoked person hold remained live")
		}
		return err
	})
	foreign := tenant.Principal{ID: f.personB.ID, TenantID: f.tenantB, Kind: tenant.Person}
	if w := f.do(foreign, "", "GET", "/api/approvals/"+hold.ID, ""); w.Code != 404 {
		t.Fatalf("foreign hold visible: %d", w.Code)
	}
}
