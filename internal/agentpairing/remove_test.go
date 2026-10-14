// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: native integration makes an ordinary paired adapter into a coordinator
// by inventing permissions on its authenticated principal or widening its key.
// Use the actual person-approved pairing issuance, without synthetic scopes.
func TestNativePilotCannotExpandPairedRuntimeAuthority(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	actor := tenant.Principal{TenantID: f.tenantID, ID: *v.PrincipalID, Kind: tenant.Agent}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT k.id::text,k.created_by_principal_id::text,k.scopes
 FROM agent_pairing_computers c JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.id=c.key_id
 WHERE c.id=$1`, *v.ComputerID).Scan(&actor.KeyID, &actor.KeyCreatorID, &actor.Scopes); err != nil {
		t.Fatal(err)
	}
	actor.AuthKeyID = actor.KeyID
	ctx := tenant.WithPrincipal(t.Context(), actor)
	for _, permission := range []string{"harness.worker", "harness.control", "harness.force_stop", "chat.receive"} {
		err := db.InTenant(ctx, f.db.App, f.tenantID, func(tx pgx.Tx) error {
			if err := agentpairing.LockMutation(ctx, tx); err != nil {
				return err
			}
			return authz.RequireTx(ctx, tx, actor, permission, authz.Scope{})
		})
		if permission == "harness.worker" {
			if err != nil || !slices.Contains(actor.Scopes, permission) {
				t.Fatalf("issued runtime cannot report its own work: %v", err)
			}
		} else if !errors.Is(err, authz.ErrForbidden) || slices.Contains(actor.Scopes, permission) {
			t.Fatalf("paired runtime unexpectedly has %s authority: %v", permission, err)
		}
	}
}

// Risk: pairing cleanup revokes the only principal that can reconcile an
// owned native root or drain its explicit final, because it has no worker run.
func TestNativeRootOwnershipSurvivesPairingDrainAndRemoval(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude", "codex")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	a, sibling := v.Enrollments[0], v.Enrollments[1]
	computer := *v.ComputerID
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	root := uuid(t, f.db)
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO native_session_roots(tenant_id,id,owned_session_id,account_id,computer_id,runtime_epoch,hold_state)
 VALUES($1,$2,gen_random_uuid(),$3,$4,gen_random_uuid(),'queued')`, f.tenantID, root, a.AccountID, computer); err != nil {
		t.Fatal(err)
	}
	// Neither an already released root nor a worker sidecar may keep another
	// enrollment alive or delay final drain after the actual root releases.
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO native_session_roots(tenant_id,id,owned_session_id,account_id,computer_id,hold_state,released_at,run_id)
 VALUES($1,gen_random_uuid(),gen_random_uuid(),$2,$3,'idle',clock_timestamp(),NULL),
 ($1,gen_random_uuid(),gen_random_uuid(),$2,$3,'exit_unconfirmed',NULL,gen_random_uuid())`, f.tenantID, sibling.AccountID, computer); err != nil {
		t.Fatal(err)
	}
	base := "/api/agent-pairing/computers/" + computer
	assertCause := func(path string, body any, person bool, credential string) {
		t.Helper()
		w := f.call("POST", path, body, person, credential, 409)
		var response struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != "native_roots_unreleased" {
			t.Fatalf("ownership refusal for %s: %s (%v)", path, w.Body.String(), err)
		}
	}
	owner := tenant.Principal{TenantID: f.tenantID, ID: f.person, Kind: tenant.Person}
	for _, state := range []string{"queued", "starting", "running", "idle", "taken", "exit_unconfirmed"} {
		if _, err := f.db.Admin.Exec(t.Context(), `UPDATE native_session_roots SET hold_state=$2 WHERE id=$1`, root, state); err != nil {
			t.Fatal(err)
		}
		assertCause(base+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "")
		assertCause(base+"/enrollments/"+a.AccountID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "")
		assertCause("/api/agent-pairing/self/disconnect", map[string]string{"mode": "revoke_now"}, false, key)
		assertCause(base+"/remove", nil, true, "")
		ctx := tenant.WithPrincipal(t.Context(), owner)
		err := db.InTenant(ctx, f.db.App, f.tenantID, func(tx pgx.Tx) error {
			if err := agentpairing.LockMutation(ctx, tx); err != nil {
				return err
			}
			return agentpairing.DisconnectAccount(ctx, tx, owner, a.AccountID)
		})
		if !errors.Is(err, agentpairing.ErrNativeRootsUnreleased) {
			t.Fatalf("%s account disconnect lost exact ownership cause: %v", state, err)
		}
	}
	f.call("POST", base+"/enrollments/"+sibling.AccountID+"/disconnect", map[string]string{"mode": "drain"}, true, "", 200)
	f.call("POST", base+"/disconnect", map[string]string{"mode": "drain"}, true, "", 200)
	assertRetained := func() {
		t.Helper()
		var computerState, enrollmentState string
		var revoked, archived, released bool
		err := f.db.Admin.QueryRow(t.Context(), `SELECT c.state,e.state,k.revoked_at IS NOT NULL,
 c.archived_at IS NOT NULL OR a.archived_at IS NOT NULL,r.released_at IS NOT NULL
 FROM agent_pairing_computers c JOIN agent_pairing_enrollments e ON e.computer_id=c.id
 JOIN agent_accounts a ON a.id=e.account_id JOIN agent_keys k ON k.id=c.key_id
 JOIN native_session_roots r ON r.id=$2 WHERE c.id=$1 AND e.account_id=$3`, computer, root, a.AccountID).Scan(&computerState, &enrollmentState, &revoked, &archived, &released)
		if err != nil || computerState != "draining" || enrollmentState != "draining" || revoked || archived || released {
			t.Fatalf("native owner lost during drain: computer=%s enrollment=%s revoked=%t archived=%t released=%t err=%v", computerState, enrollmentState, revoked, archived, released, err)
		}
		f.call("GET", "/api/agent-pairing/self", nil, false, key, 200)
	}
	assertRetained()
	finish := func() {
		t.Helper()
		ctx := tenant.WithPrincipal(t.Context(), owner)
		if err := db.InTenant(ctx, f.db.App, f.tenantID, func(tx pgx.Tx) error {
			if err := agentpairing.LockMutation(ctx, tx); err != nil {
				return err
			}
			return agentpairing.FinishDrain(ctx, tx, a.AccountID)
		}); err != nil {
			t.Fatal(err)
		}
	}
	finish()
	assertRetained()
	// Release evidence is supplied by the owning lifecycle service; pairing
	// reads only its persisted release, never a display state or stale heartbeat.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE native_session_roots SET released_at=clock_timestamp() WHERE id=$1`, root); err != nil {
		t.Fatal(err)
	}
	finish()
	var revoked bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM agent_keys WHERE id=(SELECT key_id FROM agent_pairing_computers WHERE id=$1)`, computer).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("released root did not finish drain: revoked=%t err=%v", revoked, err)
	}
	f.call("POST", base+"/remove", nil, true, "", 200)
}

func (f *fixture) listedComputers() []string {
	f.t.Helper()
	var out struct {
		Computers []agentpairing.View `json:"computers"`
	}
	decodeResult(f.t, f.call("GET", "/api/agent-pairing/computers", nil, true, "", 200), &out)
	ids := []string{}
	for _, c := range out.Computers {
		if c.ComputerID != nil {
			ids = append(ids, *c.ComputerID)
		}
	}
	return ids
}

func (f *fixture) listedAccounts() []string {
	f.t.Helper()
	var out []struct {
		ID string `json:"id"`
	}
	decodeResult(f.t, f.call("GET", "/api/agent-accounts", nil, true, "", 200), &out)
	ids := []string{}
	for _, a := range out {
		ids = append(ids, a.ID)
	}
	return ids
}

func (f *fixture) events(kind string) int {
	f.t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(f.t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type=$2`, f.tenantID, kind).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// AEON-402: a revoked computer and its account bindings leave the lists on
// Remove; the rows and the audit history stay.
func TestRemoveRevokedComputerArchivesItAndItsAccounts(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude", "codex")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	path := "/api/agent-pairing/computers/" + *v.ComputerID + "/remove"
	w := f.call("POST", path, nil, true, "", 409)
	if !strings.Contains(w.Body.String(), "computer_connected") {
		t.Fatalf("connected computer removal: %s", w.Body.String())
	}
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
	// Accounts whose only computer is revoked already leave the list and the ready count.
	for _, e := range v.Enrollments {
		if slices.Contains(f.listedAccounts(), e.AccountID) {
			t.Fatal("revoked binding still listed")
		}
	}
	if !slices.Contains(f.listedComputers(), *v.ComputerID) {
		t.Fatal("revoked computer should stay listed until removed")
	}
	req := f.request("POST", path, nil, true, "")
	req.Header.Del("Origin")
	cross := httptest.NewRecorder()
	f.h.ServeHTTP(cross, req)
	if cross.Code != 403 {
		t.Fatalf("remove without same origin: %d", cross.Code)
	}

	var removed agentpairing.View
	decodeResult(t, f.call("POST", path, nil, true, "", 200), &removed)
	if removed.ArchivedAt == nil || *removed.ComputerState != "revoked" {
		t.Fatalf("removed view: %+v", removed)
	}
	if slices.Contains(f.listedComputers(), *v.ComputerID) {
		t.Fatal("removed computer still listed")
	}
	f.call("POST", path, nil, true, "", 200)
	if f.events("agent_pairing.removed") != 1 || f.events("agent_pairing.disconnected") != 1 {
		t.Fatal("remove must add one audit event and keep the history")
	}
	var archived int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_accounts WHERE tenant_id=$1 AND archived_at IS NOT NULL AND state='unavailable'`, f.tenantID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != 2 {
		t.Fatalf("archived accounts %d want 2 (rows kept, not deleted)", archived)
	}
	// Still readable by ID for its history.
	f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200)
}

// A computer approved but never confirmed is revoked and removed in one step.
func TestRemoveNeverConfirmedComputerRevokesIt(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	v := f.approve(p, "one_per_harness")
	var removed agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/remove", nil, true, "", 200), &removed)
	if *removed.ComputerState != "revoked" || removed.ArchivedAt == nil {
		t.Fatalf("never-confirmed removal: %+v", removed)
	}
	var run string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT status FROM agent_runs WHERE id=$1`, *v.Enrollments[0].VerificationRunID).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if run != "cancelled" {
		t.Fatalf("queued verification %s, want cancelled", run)
	}
	if f.redeem(p).RuntimePrefix != "" {
		t.Fatal("removed computer can still redeem")
	}
}

// Unsettled runs keep a revoked computer listed until accounting is known.
func TestRemoveRefusesUnsettledRuns(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	e := v.Enrollments[0]
	f.probe(v, e, key, 200)
	f.claim(v, e, key, f.reserve(v, e, key, 200), 200)
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]string{"mode": "revoke_now"}, true, "", 200)
	w := f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/remove", nil, true, "", 409)
	if !strings.Contains(w.Body.String(), "runs_unsettled") {
		t.Fatalf("unsettled removal: %s", w.Body.String())
	}
}

// Remove on an account row: person-only, disconnects a paired binding,
// cancels its queued work, and refuses while a run works on it.
func TestArchiveAccountDisconnectsBindingAndKeepsHistory(t *testing.T) {
	f := newFixture(t)
	p, v := f.twoQualifiedEnrollments()
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	a, b := v.Enrollments[0], v.Enrollments[1]
	f.probe(v, b, key, 200)
	f.claim(v, b, key, f.reserve(v, b, key, 200), 200)

	f.call("POST", "/api/agent-accounts/"+a.AccountID+"/archive", nil, false, key, 403)
	f.call("POST", "/api/agent-accounts/"+a.AccountID+"/archive", nil, true, "", 200)
	listed := f.listedAccounts()
	if slices.Contains(listed, a.AccountID) || !slices.Contains(listed, b.AccountID) {
		t.Fatalf("listed after archive: %v", listed)
	}
	var enrollment, run string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT e.state,r.status FROM agent_pairing_enrollments e JOIN agent_runs r ON r.id=e.verification_run_id WHERE e.account_id=$1`, a.AccountID).Scan(&enrollment, &run); err != nil {
		t.Fatal(err)
	}
	if enrollment != "revoked" || run != "cancelled" {
		t.Fatalf("archived binding %s, queued run %s", enrollment, run)
	}
	f.call("POST", "/api/agent-accounts/"+a.AccountID+"/archive", nil, true, "", 200)
	f.call("PATCH", "/api/agent-accounts/"+a.AccountID, map[string]string{"state": "available"}, true, "", 409)
	if f.events("account.archived") != 1 {
		t.Fatal("archive event missing or duplicated")
	}

	w := f.call("POST", "/api/agent-accounts/"+b.AccountID+"/archive", nil, true, "", 409)
	if !strings.Contains(w.Body.String(), "account_busy") {
		t.Fatalf("busy archive: %s", w.Body.String())
	}
	var computer string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT state FROM agent_pairing_computers WHERE id=$1`, *v.ComputerID).Scan(&computer); err != nil {
		t.Fatal(err)
	}
	if computer != "connected" {
		t.Fatalf("removing one account changed the computer to %s", computer)
	}
}

// A queued vendor-handoff retry is pinned only through retry_account_id; it
// is cancelled with its account, not left queued with nowhere to route.
func TestArchiveAccountCancelsQueuedHandoffRetry(t *testing.T) {
	f := newFixture(t)
	_, v := f.twoQualifiedEnrollments()
	a, b := v.Enrollments[0], v.Enrollments[1]
	insertRetry := func(target string) string {
		var id string
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,retry_of_run_id,retry_account_id)
            SELECT tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,id,$2 FROM agent_runs WHERE id=$1 RETURNING id::text`, *a.VerificationRunID, target).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	mine, other := insertRetry(a.AccountID), insertRetry(b.AccountID)
	f.call("POST", "/api/agent-accounts/"+a.AccountID+"/archive", nil, true, "", 200)
	status := func(id string) string {
		var s string
		if err := f.db.Admin.QueryRow(t.Context(), `SELECT status FROM agent_runs WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := status(mine); got != "cancelled" {
		t.Fatalf("retry pinned to the removed account is %s, want cancelled", got)
	}
	if got := status(other); got != "queued" {
		t.Fatalf("retry pinned to another account is %s, want queued", got)
	}
}
