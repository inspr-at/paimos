// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWorkstationPathsSerializeWithPairingLifecycle(t *testing.T) {
	for _, action := range []string{"mark", "unmark", "authorize"} {
		for _, first := range []string{"lifecycle", "workstation"} {
			t.Run(action+"/"+first+"_first", func(t *testing.T) {
				testWorkstationPairingInterleaving(t, action, first, false, "pairing")
			})
		}
	}
}

// After either real writer acquires tree, a pairing waiter must still leave
// tenant free. This rejects pairing -> tenant -> tree as well as the original
// tenant -> pairing inversion, without relying on deadlock detector timing.
func TestWorkstationPathsAcquireTreeBeforeTenant(t *testing.T) {
	for _, action := range []string{"mark", "unmark", "authorize"} {
		for _, first := range []string{"lifecycle", "workstation"} {
			t.Run(action+"/"+first+"_first", func(t *testing.T) {
				testWorkstationPairingInterleaving(t, action, first, false, "tree")
			})
		}
	}
}

// A snapshot authenticated before disconnect must be rejected by the final
// handler transaction after disconnect commits, without creating a role or
// restoring designation. This uses the same forced overlap as the success cases.
func TestWorkstationWaitersRecheckPairingRevocation(t *testing.T) {
	for _, action := range []string{"mark", "authorize"} {
		t.Run(action, func(t *testing.T) {
			testWorkstationPairingInterleaving(t, action, "lifecycle", true, "pairing")
		})
	}
}

func testWorkstationPairingInterleaving(t *testing.T, action, first string, revoke bool, fence string) {
	t.Helper()
	f := newWorkstationFixture(t)
	if action != "mark" {
		f.enable(t)
	}
	originalPool := f.m.pool
	// Read durable effects as the owner, including addressed pairing events.
	// An anonymous transaction cannot see their audit rows under event RLS.
	assertCtx := tenant.WithPrincipal(t.Context(), f.owner)
	if err := db.InTenant(t.Context(), originalPool, f.owner.TenantID, func(tx pgx.Tx) error {
		proof := sha256.Sum256([]byte(f.deviceProof))
		_, err := tx.Exec(t.Context(), `UPDATE agent_pairing_requests
 SET computer_id=$1,lifecycle_hash=$2,approved_by=$3 WHERE id=$1`, f.computer, hex.EncodeToString(proof[:]), f.owner.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var principal tenant.Principal
	if action == "authorize" {
		prefix, secret, _ := parseBearer("Bearer " + f.key.Token)
		var ok bool
		var err error
		principal, ok, err = f.m.authenticateAgent(t.Context(), prefix, secret)
		if err != nil || !ok || !authz.OwnerWorkstation(principal) {
			t.Fatal("fixture did not authenticate its designated workstation")
		}
	}
	count := func(kind string) int {
		t.Helper()
		var n int
		if err := db.InTenant(assertCtx, originalPool, f.owner.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(assertCtx, `SELECT count(*) FROM events WHERE type=$1`, kind).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	beforeDesignation := count("agent_key.owner_workstation_changed")
	beforeRoles := count("authz.role_created")
	beforeReported := count("agent_pairing.reported")
	beforeDisconnected := count("agent_pairing.disconnected")

	// Pause AFTER the first request acquires the selected advisory fence,
	// BEFORE tenant. The second request's actual pairing wait proves overlap;
	// neither request may own tenant until both advisory fences are held.
	pool, barrier, ctx := dbtest.BarrierPool(t, originalPool, func(query string) bool {
		return strings.Contains(query, "pg_advisory_xact_lock") &&
			strings.Contains(query, "aeon-pairing:") == (fence == "pairing")
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lifecyclePool, workstationPool := originalPool, originalPool
	if first == "lifecycle" {
		lifecyclePool = pool
	} else {
		workstationPool = pool
	}
	f.m.pool = workstationPool
	lifecycleMux, workstationMux := http.NewServeMux(), http.NewServeMux()
	agentpairing.New(lifecyclePool, "http://example.com", "").Mount(lifecycleMux)
	authz.New(workstationPool).Mount(workstationMux)

	start := func(operation string) (*httptest.ResponseRecorder, <-chan struct{}) {
		t.Helper()
		var request *http.Request
		var handler http.Handler
		if operation == "lifecycle" {
			body, err := json.Marshal(map[string]any{
				"tenant_id": f.owner.TenantID, "request_id": f.computer, "lifecycle_secret": f.deviceProof,
				"progress": map[string]string{"state": "login_required"},
			})
			if err != nil {
				t.Fatal(err)
			}
			request = httptest.NewRequest("POST", "http://example.com/api/agent-pairing/reconcile", strings.NewReader(string(body))).WithContext(ctx)
			if revoke {
				request = httptest.NewRequest("POST", "http://example.com/api/agent-pairing/computers/"+f.computer+"/disconnect",
					strings.NewReader(`{"expected_revision":1,"mode":"revoke_now"}`)).WithContext(tenant.WithPrincipal(ctx, f.owner))
				request.Header.Set("Origin", "http://example.com")
			}
			handler = lifecycleMux
		} else if action == "authorize" {
			// Exercise the actual role writer with the guard installed at its
			// final transaction boundary and a previously authenticated snapshot.
			guarded := db.WithTenantGuard(tenant.WithPrincipal(ctx, principal), workstationGuard(principal, "roles.manage", authz.Scope{}))
			request = httptest.NewRequest("POST", "/api/roles", strings.NewReader(`{"name":"Concurrent workstation role","permissions":["nodes.read"]}`)).WithContext(guarded)
			handler = workstationMux
		} else {
			body := map[string]any{"owner_workstation": action == "mark"}
			if action == "mark" {
				body["workstation_computer_id"] = f.computer
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request = httptest.NewRequest("PUT", "http://example.com/api/agent-keys/"+f.key.ID+"/owner-workstation", strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, f.owner))
			request.Header.Set("Origin", "http://example.com")
			request.SetPathValue("id", f.key.ID)
			handler = http.HandlerFunc(f.m.handleOwnerWorkstation)
		}
		response, done := httptest.NewRecorder(), make(chan struct{})
		go func() { defer close(done); handler.ServeHTTP(response, request) }()
		t.Cleanup(func() {
			cancel()
			barrier.Release()
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			dbtest.Await(t, cleanup, done)
		})
		return response, done
	}
	firstResponse, firstDone := start(first)
	pid := barrier.Wait(t, ctx)
	second := "workstation"
	if first == "workstation" {
		second = "lifecycle"
	}
	secondResponse, secondDone := start(second)
	if lock := dbtest.BlockedOrDone(t, ctx, originalPool, pid, secondDone); lock != "advisory" {
		t.Fatalf("second request did not wait on the first request's pairing fence: lock=%q", lock)
	}
	if err := db.InTenant(ctx, originalPool, f.owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE NOWAIT`, f.owner.TenantID)
		return err
	}); err != nil {
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "55P03" {
			t.Fatalf("unexpected tenant probe failure: %v", err)
		}
		t.Errorf("writer owns tenant before completing pairing -> tree entry (paused after %s)", fence)
	}
	barrier.Release()
	dbtest.Await(t, ctx, firstDone)
	dbtest.Await(t, ctx, secondDone)
	responses := map[string]*httptest.ResponseRecorder{first: firstResponse, second: secondResponse}
	if responses["lifecycle"].Code != http.StatusOK {
		t.Fatalf("lifecycle request failed: status=%d", responses["lifecycle"].Code)
	}
	want := http.StatusOK
	if action == "authorize" {
		want = http.StatusCreated
	}
	if revoke {
		want = http.StatusForbidden
		if action == "mark" {
			want = http.StatusConflict // Disconnect has revoked the key itself.
		}
	}
	if responses["workstation"].Code != want {
		t.Fatalf("workstation %s status=%d, want %d", action, responses["workstation"].Code, want)
	}
	if err := db.InTenant(assertCtx, originalPool, f.owner.TenantID, func(tx pgx.Tx) error {
		var marked, revoked, seen bool
		var state, setup string
		var roles int
		if err := tx.QueryRow(assertCtx, `SELECT k.owner_workstation,k.revoked_at IS NOT NULL,c.state,c.setup_state,c.last_seen_at IS NOT NULL
 FROM agent_keys k JOIN agent_pairing_computers c ON c.id=$2 WHERE k.id=$1`, f.key.ID, f.computer).Scan(&marked, &revoked, &state, &setup, &seen); err != nil {
			return err
		}
		if revoke {
			if !revoked || state != "revoked" || marked != (action == "authorize") {
				t.Error("disconnect or rejected designation did not preserve durable state")
			}
		} else if revoked || state != "connected" || setup != "login_required" || !seen || marked != (action != "unmark") {
			t.Error("lifecycle report or workstation designation did not persist")
		}
		if err := tx.QueryRow(assertCtx, `SELECT count(*) FROM roles WHERE name='Concurrent workstation role'`).Scan(&roles); err != nil {
			return err
		}
		wantRoles := 0
		if action == "authorize" && !revoke {
			wantRoles = 1
		}
		if roles != wantRoles {
			t.Errorf("durable role mutations=%d, want %d", roles, wantRoles)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantDesignation, wantRoles, wantReported, wantDisconnected := beforeDesignation, beforeRoles, beforeReported, beforeDisconnected
	if !revoke {
		wantReported++
		if action == "authorize" {
			wantRoles++
		} else {
			wantDesignation++
		}
	} else {
		wantDisconnected++
	}
	for kind, want := range map[string]int{
		"agent_key.owner_workstation_changed": wantDesignation, "authz.role_created": wantRoles,
		"agent_pairing.reported": wantReported, "agent_pairing.disconnected": wantDisconnected,
	} {
		if got := count(kind); got != want {
			t.Errorf("%s audit count=%d, want %d", kind, got, want)
		}
	}
}
