// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestPairingLocksTenantBeforeAdvisory(t *testing.T) {
	for name, lock := range map[string]func(context.Context, pgx.Tx) error{
		"pairing":  agentpairing.Lock,
		"mutation": agentpairing.LockMutation,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			dbtest.TenantBeforeAdvisory(t, f.db, f.tenantID, "aeon-pairing:"+f.tenantID, 0, func(ctx context.Context) error {
				return db.InTenant(ctx, f.db.App, f.tenantID, func(tx pgx.Tx) error { return lock(ctx, tx) })
			})
		})
	}
}

func TestPairingApprovalTenantBeforeAdvisory(t *testing.T) {
	f := newFixture(t)
	old := f.propose("claude")
	f.approve(old, "connect_only")
	paired := f.redeem(old)
	f.call("POST", "/api/agent-pairing/computers/"+*paired.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
	fresh := f.propose("claude")
	keys := []string{}
	for _, account := range fresh.review.Requested {
		keys = append(keys, account.AccountKey)
	}
	req := f.request("POST", "/api/agent-pairing/requests/"+fresh.id+"/approve", map[string]any{
		"request_digest": fresh.review.Digest, "verification": "connect_only", "selected_account_keys": keys,
	}, true, "")
	dbtest.TenantBeforeAdvisory(t, f.db, f.tenantID, "aeon-pairing:"+f.tenantID, 0, func(ctx context.Context) error {
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != 200 {
			return fmt.Errorf("approve: status %d want 200", w.Code)
		}
		return nil
	})
	var state string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT status FROM principals WHERE id=$1`, *paired.PrincipalID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "deactivated" {
		t.Fatalf("replaced principal status=%s want deactivated", state)
	}
}

func testLocalAuthKey(t *testing.T) string {
	t.Helper()
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(elliptic.Marshal(signer.Curve, signer.X, signer.Y))
}

// AEON-470 review: a fresh pairing retires only the identity it replaces. A name
// is not proof: another person's computer with the same name, a different
// machine (platform, architecture) and a computer pinned to another local-auth
// key are all left alone; the same person's same machine is retired.
func TestPairingRepairRetiresOnlyTheApproversOwnReplacedComputer(t *testing.T) {
	f := newFixture(t)
	status := func(principal string) string {
		t.Helper()
		var s string
		if err := f.db.Admin.QueryRow(t.Context(), `SELECT status FROM principals WHERE id=$1::uuid`, principal).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	var bob string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,email,roles) VALUES($1::uuid,'person','Bob Other','bob@example.test','{}') RETURNING id::text`, f.tenantID).Scan(&bob); err != nil {
		t.Fatal(err)
	}
	pair := func(platform, arch, key string) agentpairing.View {
		t.Helper()
		p := f.proposePlatformKey(platform, arch, key, "claude")
		f.approve(p, "connect_only")
		return f.redeem(p)
	}
	disconnect := func(v agentpairing.View) {
		t.Helper()
		f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
	}
	keyA, keyB := testLocalAuthKey(t), testLocalAuthKey(t)
	theirs := pair("darwin", "arm64", "")
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_requests SET approved_by=$2::uuid WHERE computer_id=$1::uuid`, *theirs.ComputerID, bob); err != nil {
		t.Fatal(err)
	}
	otherMachine := pair("linux", "amd64", "")
	pinnedElsewhere := pair("darwin", "arm64", keyA)
	mine := pair("darwin", "arm64", "")
	for _, v := range []agentpairing.View{theirs, otherMachine, pinnedElsewhere, mine} {
		disconnect(v)
	}

	// Same name, but Bob approved it, it is another machine, or its pinned key differs.
	fresh := pair("darwin", "arm64", keyB)
	for name, v := range map[string]agentpairing.View{"another person's computer": theirs, "another platform": otherMachine, "a computer pinned to another key": pinnedElsewhere} {
		if got := status(*v.PrincipalID); got != "active" {
			t.Fatalf("%s was retired by a pairing it does not belong to: %q", name, got)
		}
	}
	if status(*mine.PrincipalID) != "deactivated" {
		t.Fatal("the same person's replaced computer was not retired")
	}
	if status(*fresh.PrincipalID) != "active" {
		t.Fatal("the new pairing was retired")
	}
	var replaced int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='principal.deactivated' AND after->>'replaced_by'=$1`, *fresh.PrincipalID).Scan(&replaced); err != nil || replaced != 1 {
		t.Fatalf("replaced_by recorded on %d identities, want 1: %v", replaced, err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='principal.deactivated' AND after->>'principal_id'=$1`, *theirs.PrincipalID).Scan(&replaced); err != nil || replaced != 0 {
		t.Fatalf("another person's identity has %d deactivation events: %v", replaced, err)
	}
}
