// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
)

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
