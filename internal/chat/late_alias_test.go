// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

func (f *fixture) replayNative(owner tenant.Principal, project, ref, lease, vendor string) *httptest.ResponseRecorder {
	in := map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": ref, "worker_lease": lease, "advertised_capabilities": []string{"inbox"}}
	if vendor != "" {
		in["vendor_session_ref"] = vendor
	}
	return f.call(owner, "POST", "/api/projects/"+project+"/harness-sessions", in, "")
}

func TestLateVendorAliasKeepsWorkerAndHistory(t *testing.T) {
	for _, actor := range []string{"person", "agent"} {
		t.Run(actor, func(t *testing.T) {
			f := newFixture(t)
			thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
			ref, vendor := "late-ref-"+uid(), "late-vendor-"+uid()
			id, lease := f.registerNative(t, f.alice, f.project, ref, "")
			bound := f.bind(t, f.alice, thread, id, "0")
			owner := f.alice
			if actor == "agent" {
				f.agent.Scopes = append(f.agent.Scopes, "harness.write")
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes); err != nil {
					t.Fatal(err)
				}
				owner = f.agent
			}
			for _, alias := range []string{vendor, vendor, ""} {
				expect(t, f.replayNative(owner, f.project, ref, lease, alias), http.StatusCreated)
				resolved := decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{thread.ID, id, "1"}, lease))
				if resolved.ID != thread.ID || resolved.BindingEpoch != "1" {
					t.Fatal("alias replay changed the conversation or binding epoch")
				}
			}
			// Learning an alias extends lasting ownership, not the immutable
			// snapshot of the references known when this registration was bound.
			var claims int
			var snapshotUnchanged bool
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM chat_native_contexts WHERE role_id=$1),vendor_ref_digest IS NULL FROM chat_session_contexts WHERE session_id=$2`, thread.Role.ID, id).Scan(&claims, &snapshotUnchanged); err != nil || claims != 2 || !snapshotUnchanged {
				t.Fatalf("ownership/history not preserved: claims=%d snapshot=%v err=%v", claims, snapshotUnchanged, err)
			}
			if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			next, nextLease := f.registerNative(t, f.alice, f.project, vendor, "")
			resumed := f.bind(t, f.alice, bound, next, "1")
			if resumed.ID != thread.ID || resumed.BindingEpoch != "2" {
				t.Fatal("alias continuation lost the lasting conversation")
			}
			decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{thread.ID, next, "2"}, nextLease))
		})
	}
}

func TestLateVendorAliasCannotCrossChatOwnership(t *testing.T) {
	for _, scenario := range []string{"person", "role", "project"} {
		for _, field := range []string{"ref", "vendor"} {
			t.Run(scenario+"/"+field, func(t *testing.T) {
				f := newFixture(t)
				thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
				ref, vendor := "late-ref-"+uid(), "late-vendor-"+uid()
				id, lease := f.registerNative(t, f.alice, f.project, ref, "")
				f.bind(t, f.alice, thread, id, "0")
				expect(t, f.replayNative(f.alice, f.project, ref, lease, vendor), http.StatusCreated)
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				owner, project := f.alice, f.project
				if scenario == "person" {
					owner = f.bob
				} else if scenario == "project" {
					project = f.secondProject
				}
				target := f.thread(t, owner, f.role(t, owner, project, "worker", "other"))
				nextRef, nextVendor := vendor, ""
				if field == "vendor" {
					nextRef, nextVendor = "other-ref-"+uid(), vendor
				}
				next, _ := f.legacyNative(t, owner, project, nextRef, nextVendor)
				w := f.call(owner, "POST", "/api/chat-threads/"+target.ID+"/binding", map[string]string{"session_id": next, "expected_epoch": "0"}, "")
				expect(t, w, http.StatusNotFound)
				if !strings.Contains(w.Body.String(), "chat binding unavailable") {
					t.Fatal("rejected for a reason other than chat ownership")
				}
				unchanged := decode[Thread](t, f.call(owner, "GET", "/api/chat-threads/"+target.ID, nil, ""))
				if unchanged.BindingEpoch != "0" || unchanged.Revision != "1" {
					t.Fatal("rejected alias changed the target thread")
				}
			})
		}
	}
}

func TestLateVendorAliasRejectsHistoricalConflict(t *testing.T) {
	for _, scenario := range []string{"person", "role", "project", "transferred_owner"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			original := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
			originalRef := "original-ref-" + uid()
			first, firstLease := f.registerNative(t, f.alice, f.project, originalRef, "")
			f.bind(t, f.alice, original, first, "0")
			owner, project := f.alice, f.project
			if scenario == "person" || scenario == "transferred_owner" {
				owner = f.bob
			} else if scenario == "project" {
				project = f.secondProject
			}
			target := f.thread(t, owner, f.role(t, owner, project, "worker", "other"))
			ref, alias := "target-ref-"+uid(), originalRef
			id, lease := f.registerNative(t, owner, project, ref, "")
			f.bind(t, owner, target, id, "0")
			if scenario == "transferred_owner" {
				id, lease, ref, alias = first, firstLease, originalRef, "new-alias-"+uid()
				f.preGuardMutation(t, `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, id, owner.ID)
			}
			var before, after int64
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT row_version FROM harness_sessions WHERE id=$1`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			w := f.replayNative(owner, project, ref, lease, alias)
			expect(t, w, http.StatusConflict)
			if !strings.Contains(w.Body.String(), "chat binding unavailable") {
				t.Fatal("rejected for a reason other than historical ownership")
			}
			var missing bool
			var claims int
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT row_version,vendor_ref_digest IS NULL,(SELECT count(*) FROM chat_native_contexts) FROM harness_sessions WHERE id=$1`, id).Scan(&after, &missing, &claims); err != nil || before != after || !missing || claims != 2 {
				t.Fatalf("failed replay left partial changes: versions=%d/%d missing=%v claims=%d err=%v", before, after, missing, claims, err)
			}
			if scenario != "transferred_owner" {
				decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{original.ID, first, "1"}, firstLease))
				decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{target.ID, id, "1"}, lease))
			}
		})
	}
}

func TestLateVendorAliasInheritsUnboundGenerationHistory(t *testing.T) {
	for _, source := range []string{"ref", "vendor"} {
		t.Run(source, func(t *testing.T) {
			f := newFixture(t)
			thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
			ref := "historical-ref-" + uid()
			first, _ := f.registerNative(t, f.alice, f.project, ref, "")
			bound := f.bind(t, f.alice, thread, first, "0")
			if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, first); err != nil {
				t.Fatal(err)
			}
			nextRef, nextVendor := ref, "learned-alias-"+uid()
			if source == "vendor" {
				nextRef, nextVendor = nextVendor, ref
			}
			next, lease := f.registerNative(t, f.alice, f.project, nextRef, "")
			expect(t, f.replayNative(f.alice, f.project, nextRef, lease, nextVendor), http.StatusCreated)
			var claims int
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM chat_native_contexts WHERE role_id=$1`, thread.Role.ID).Scan(&claims); err != nil || claims != 2 {
				t.Fatalf("unbound replacement lost historical alias ownership: claims=%d err=%v", claims, err)
			}
			f.bind(t, f.alice, bound, next, "1")
			decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{thread.ID, next, "2"}, lease))
		})
	}
}

func TestLateVendorAliasRechecksAccessBeforeHierarchyLock(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
	ref := "late-ref-" + uid()
	id, lease := f.registerNative(t, f.alice, f.project, ref, "")
	f.bind(t, f.alice, thread, id, "0")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.alice.TenantID).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.replayNative(f.alice, f.project, ref, lease, "late-alias-"+uid()) }()
	for {
		select {
		case w := <-done:
			t.Fatalf("replay returned before the access fence: status %d", w.Code)
		default:
		}
		var waiting bool
		// The shared work writer fences the tenant before the handler's own
		// fence. Observe this transaction's blocker, independent of SQL casts.
		if err = f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'
		 AND $1::int=ANY(pg_blocking_pids(pid)) AND query LIKE '%FROM tenants%' AND query LIKE '%FOR NO KEY UPDATE%')`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	var earlyAdvisory bool
	if err = f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE granted AND locktype='advisory'
	 AND $1::int=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&earlyAdvisory); err != nil || earlyAdvisory {
		t.Fatalf("replay acquired an advisory lock before tenant: held=%v err=%v", earlyAdvisory, err)
	}
	var free bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, f.project).Scan(&free); err != nil || !free {
		t.Fatalf("replay locked hierarchy before tenant: free=%v err=%v", free, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		expect(t, w, http.StatusForbidden)
		if !strings.Contains(w.Body.String(), "permission denied") {
			t.Fatal("replay failed for a reason other than revoked permission")
		}
	case <-ctx.Done():
		t.Fatal("replay did not finish")
	}
	var missing bool
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT vendor_ref_digest IS NULL FROM harness_sessions WHERE id=$1`, id).Scan(&missing); err != nil || !missing {
		t.Fatalf("revoked replay wrote the alias: missing=%v err=%v", missing, err)
	}
}
