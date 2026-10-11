// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Risks: revoked authority winning a final mutation, duplicate notification or
// charge, a guessed PID freeing resources, and failed notification losing its
// intent. Reuse real consent/budget/inbox guards; a PostgreSQL barrier proves
// revocation and the attempted final authorization actually overlap.
func TestRoutineRecoveryRevocationReceiptsAndPendingNotice(t *testing.T) {
	d := setupDispatch(t)
	f := d.fixture
	ctx := db.AllProjects(t.Context(), "recovery fixture")
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		must(t, f.d.Admin.QueryRow(t.Context(), q, args...).Scan(&n))
		return n
	}
	// Missing capacity is WAIT, not a failed work attempt or reconciliation.
	d.mode.Store(1)
	_, err := d.e.RunTenant(t.Context(), f.owner.TenantID)
	must(t, err)
	if count(`SELECT count(*) FROM routine_attempts WHERE run_id=$1`, f.run) != 0 || count(`SELECT count(*) FROM routine_effect_outbox WHERE run_id=$1 AND effect_key='recovery' AND (result->>'reconciliations')::int<>0`, f.run) != 0 {
		t.Fatal("capacity wait consumed a work/recovery attempt")
	}
	d.mode.Store(0)
	must(t, f.tx(func(tx pgx.Tx) error {
		out, err := d.e.DispatchTx(t.Context(), tx, f.owner.TenantID, f.run, "", 0)
		if err == nil && !out.Routed {
			t.Fatalf("positive dispatch: %+v", out)
		}
		return err
	}))
	var g agentsetup.RoutineGrant
	must(t, f.tx(func(tx pgx.Tx) error { return loadActorGrant(t.Context(), tx, d.agentRun, &g) }))
	beforeHolds := count(`SELECT count(*) FROM routine_budget_grants WHERE run_id=$1 AND state='held'`, f.run)
	if beforeHolds != 1 {
		t.Fatal("positive control lacks exactly one held grant")
	}
	// Seed the independently accepted claim. Never execute a native/model call.
	f.exec(`UPDATE routine_launch_intents SET state='claimed',claimed_at=clock_timestamp(),worktree_id=repeat('7',64) WHERE id=$1`, g.LaunchID)
	f.exec(`UPDATE routine_budget_grants SET state='unknown' WHERE id=$1`, g.BudgetGrantID)
	var admin string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Responsible administrator') RETURNING id::text`, f.owner.TenantID).Scan(&admin))
	dbtest.BindRole(t, f.d, f.owner.TenantID, admin, "owner")
	// Establish the revocation transaction first, holding the real access fence.
	pool, barrier, raceCtx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.HasPrefix(q, "UPDATE principals SET status='deactivated'") })
	revoked := make(chan error, 1)
	go func() {
		revoked <- db.InTenant(db.AllProjects(raceCtx, "revocation fixture"), pool, f.owner.TenantID, func(tx pgx.Tx) error {
			if err := agentpairing.Lock(raceCtx, tx); err != nil {
				return err
			}
			_, err := tx.Exec(raceCtx, `UPDATE principals SET status='deactivated' WHERE id=$1`, f.owner.ID)
			return err
		})
	}()
	pid := barrier.Wait(t, raceCtx)
	attempted := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		attempted <- db.InTenant(db.AllProjects(raceCtx, "final consent barrier"), f.d.App, f.owner.TenantID, func(tx pgx.Tx) error {
			_, err := recurrences.RequireExecutionConsentTx(raceCtx, tx, f.owner.TenantID, f.routine.ID, f.routine.Revision, f.broker.Runtime)
			return err
		})
	}()
	if lock := dbtest.BlockedOrDone(t, raceCtx, f.d.Admin, pid, done); lock == "" {
		t.Fatal("final mutation authorization did not contend with revocation")
	}
	barrier.Release()
	must(t, dbtest.Await(t, raceCtx, revoked))
	err = dbtest.Await(t, raceCtx, attempted)
	var refusal *workorders.Error
	if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Message != "owner_unavailable" {
		t.Fatalf("revocation refused for wrong reason: %v", err)
	}
	must(t, d.e.recoverOwner(ctx, f.owner.TenantID, f.routine.ID))
	var event string
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT id::text FROM events WHERE tenant_id=$1 AND type='recurrence.paused' AND metadata->>'owner_pause'='true'`, f.owner.TenantID).Scan(&event))
	if count(`SELECT count(*) FROM recurrences r JOIN recurrence_definitions d ON d.recurrence_id=r.id WHERE r.id=$1 AND r.paused AND NOT d.execute_consent`, f.routine.ID) != 1 {
		t.Fatal("revocation did not atomically pause and revoke consent")
	}
	// An actual failed inbox write rolls back notification only. The committed
	// scoped pause event remains a pending intent across executor reconstruction.
	f.exec(`CREATE FUNCTION fail_routine_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.sender_label='Routines' THEN RAISE EXCEPTION 'fixture_notice_failure' USING ERRCODE='XX000'; END IF; RETURN NEW; END $$`)
	f.exec(`CREATE TRIGGER fail_routine_notice BEFORE INSERT ON inbox_messages FOR EACH ROW EXECUTE FUNCTION fail_routine_notice()`)
	sent, err := d.e.notifyPause(ctx, f.owner.TenantID, event)
	if sent || err == nil || !strings.Contains(err.Error(), "fixture_notice_failure") {
		t.Fatalf("failed notice result: sent=%v error=%v", sent, err)
	}
	if count(`SELECT count(*) FROM inbox_messages WHERE idempotency_key=$1`, "routine-pause:"+event) != 0 {
		t.Fatal("failed notice left an acceptance")
	}
	f.exec(`DROP TRIGGER fail_routine_notice ON inbox_messages`)
	d.e = New(f.d.App, d.options)
	for range 2 {
		sent, err = d.e.notifyPause(ctx, f.owner.TenantID, event)
		must(t, err)
		if !sent {
			t.Fatal("pending notice did not retry")
		}
	}
	if count(`SELECT count(*) FROM inbox_messages WHERE idempotency_key=$1 AND recipient_principal_id=$2`, "routine-pause:"+event, admin) != 1 {
		t.Fatal("notice replay duplicated or chose revoked owner")
	}
	must(t, d.e.recoverOwner(ctx, f.owner.TenantID, f.routine.ID))
	if count(`SELECT count(*) FROM events WHERE tenant_id=$1 AND metadata->>'owner_pause'='true'`, f.owner.TenantID) != 1 {
		t.Fatal("pause replay duplicated its intent")
	}
	// Unknown PID/usage cannot issue a stop or free/reassign the original launch.
	for range 3 {
		must(t, d.e.recoverRun(ctx, f.owner.TenantID, f.run))
	}
	if count(`SELECT count(*) FROM harness_controls WHERE kind='stop'`) != 0 || count(`SELECT count(*) FROM routine_launch_intents WHERE id=$1 AND exit_observed_at IS NULL AND worktree_id=repeat('7',64)`, g.LaunchID) != 1 || count(`SELECT count(*) FROM routine_budget_grants WHERE run_id=$1 AND state='unknown'`, f.run) != 1 || count(`SELECT count(*) FROM routine_budget_settlements WHERE grant_id=$1`, g.BudgetGrantID) != 0 {
		t.Fatal("unknown process/usage changed ownership or charged")
	}
	if count(`SELECT count(*) FROM routine_effect_outbox WHERE run_id=$1 AND effect_key='recovery-notice'`, f.run) != 1 {
		t.Fatal("bounded recovery did not escalate once")
	}
	// An exact current managed process allows one cooperative control. A daemon
	// restart/generation mismatch must remain pending instead of guessing a PID.
	identity := ownedprocess.Identity{DaemonID: g.DaemonID, Generation: "wrong-generation", ProcessID: strings.Repeat("8", 32), RootPID: 1234, GroupID: 1234, StartedAt: time.Now().UTC()}
	lease := "recovery-test-owned-worker-lease-000000000"
	leaseHash := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	refHash := sha256.Sum256([]byte("aeon.harness.ref\x00recovery-worker"))
	var session string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,harness,host,management,role,work_shape,ticket_node_id,phase,heartbeat_at,ref_digest,lease_digest,process_ownership,capabilities)
 VALUES($1,$2,$3,$4,'codex','local','managed','worker','ship',$5,'working',clock_timestamp(),$6,$7,$8,ARRAY['stop','managed_control_v1']) RETURNING id::text`, f.owner.TenantID, f.project, f.agent.ID, d.agentRun, f.work, refHash[:], leaseHash[:], identity).Scan(&session))
	must(t, d.e.recoverRun(ctx, f.owner.TenantID, f.run))
	if count(`SELECT count(*) FROM harness_controls WHERE session_id=$1`, session) != 0 {
		t.Fatal("generation mismatch authorized stop")
	}
	identity.Generation = g.DaemonGeneration
	f.exec(`UPDATE harness_sessions SET process_ownership=$2,heartbeat_at=clock_timestamp() WHERE id=$1`, session, identity)
	for range 2 {
		must(t, d.e.recoverRun(ctx, f.owner.TenantID, f.run))
	}
	if count(`SELECT count(*) FROM harness_controls WHERE session_id=$1 AND kind='stop' AND expected_ownership=$2::jsonb`, session, identity) != 1 {
		t.Fatal("exact stop missing or duplicated")
	}
	// Exercise the real yield boundary: System has no invented harness.control
	// role, and personal history remains hidden from this ordinary worker view.
	mux := http.NewServeMux()
	harness.New(f.d.App).Mount(mux)
	worker := f.agent
	worker.Scopes = append(worker.Scopes, "harness.worker")
	// Reuse the fixture daemon key under an independently active administrator.
	// A disabled person's key must remain unable to poll, even for safety work.
	must(t, f.d.Admin.QueryRow(t.Context(), `UPDATE agent_keys SET scopes=array_append(scopes,'harness.worker'),created_by_principal_id=$2
   WHERE principal_id=$1 RETURNING id::text`, worker.ID, admin).Scan(&worker.KeyID))
	worker.AuthKeyID, worker.KeyCreatorID = worker.KeyID, admin
	request := httptest.NewRequest("POST", fmt.Sprintf("/api/projects/%s/harness-sessions/%s/yield", f.project, session), strings.NewReader(`{}`)).WithContext(tenant.WithPrincipal(t.Context(), worker))
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("X-Aeon-Worker-Lease", lease)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatalf("scoped stop yield: %d %s", w.Code, w.Body.String())
	}
	var offered struct {
		Controls []harness.Control `json:"controls"`
	}
	must(t, json.Unmarshal(w.Body.Bytes(), &offered))
	if len(offered.Controls) != 1 || offered.Controls[0].Kind != "stop" || offered.Controls[0].ExpectedOwnership == nil {
		t.Fatalf("scoped stop not offered: %s", w.Body.String())
	}
	// Restoration cannot resume by itself, and bearer/person-less resume cannot
	// turn this original paused run into a newly authorized assignment.
	f.exec(`UPDATE principals SET status='active' WHERE id=$1`, f.owner.ID)
	must(t, d.e.recoverOwner(ctx, f.owner.TenantID, f.routine.ID))
	if count(`SELECT count(*) FROM recurrences WHERE id=$1 AND paused`, f.routine.ID) != 1 {
		t.Fatal("rights restoration automatically resumed")
	}
	f.call("GET", "/api/recurrences/"+f.routine.ID, nil, &f.routine)
	bearer := f.owner
	bearer.BrowserSession = false
	request = httptest.NewRequest("POST", "/api/recurrences/"+f.routine.ID+"/resume", strings.NewReader(fmt.Sprintf(`{"expected_revision":%d}`, f.routine.Revision))).WithContext(tenant.WithPrincipal(t.Context(), bearer))
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, request)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "interactive_person_required") {
		t.Fatalf("person-less recovery resume: %d %s", w.Code, w.Body.String())
	}
	f.call("POST", "/api/recurrences/"+f.routine.ID+"/resume", map[string]any{"expected_revision": f.routine.Revision}, nil)
	f.call("GET", "/api/recurrences/"+f.routine.ID, nil, &f.routine)
	if f.routine.Paused || f.routine.ExecutionPolicy == nil || f.routine.ExecutionPolicy.Eligible || f.routine.ExecutionPolicy.WaitReason != "execution_consent_required" {
		t.Fatalf("resume borrowed old consent: %+v", f.routine)
	}
	if count(`SELECT count(*) FROM routine_budget_grants WHERE run_id=$1`, f.run) != 1 || count(`SELECT count(*) FROM routine_attempts WHERE run_id=$1`, f.run) != 1 {
		t.Fatal("recovery duplicated an attempt or charge")
	}
	for _, cause := range []string{"role_loss", "parent_project_move", "occurrence_project_move"} {
		t.Run(cause, func(t *testing.T) {
			d := setupDispatch(t)
			f := d.fixture
			if cause == "role_loss" {
				var retainedOwner string
				must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Retained owner') RETURNING id::text`, f.owner.TenantID).Scan(&retainedOwner))
				dbtest.BindRole(t, f.d, f.owner.TenantID, retainedOwner, "owner")
				f.exec(`UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$2 AND key='viewer') WHERE principal_id=$1 AND scope_type='workspace'`, f.owner.ID, f.owner.TenantID)
			} else {
				var project string
				must(t, f.tx(func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Moved target' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.owner.TenantID).Scan(&project)
				}))
				moved := f.parent
				if cause == "occurrence_project_move" {
					moved = f.work
				}
				f.exec(`UPDATE nodes SET parent_id=$2 WHERE id=$1`, moved, project)
			}
			ctx := db.AllProjects(t.Context(), "live ownership recovery fixture")
			if cause == "occurrence_project_move" {
				must(t, d.e.recoverRun(ctx, f.owner.TenantID, f.run))
			} else {
				// The final guard closes before the recovery worker records pause.
				err := f.tx(func(tx pgx.Tx) error {
					_, err := recurrences.RequireExecutionConsentTx(t.Context(), tx, f.owner.TenantID, f.routine.ID, f.routine.Revision, f.broker.Runtime)
					return err
				})
				var refusal *workorders.Error
				want := "output_project_changed"
				if cause == "role_loss" {
					want = "owner_unavailable"
				}
				if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Message != want {
					t.Fatalf("live final guard refused for wrong reason: %v", err)
				}
				must(t, d.e.recoverOwner(ctx, f.owner.TenantID, f.routine.ID))
			}
			var paused, consent bool
			must(t, f.d.Admin.QueryRow(t.Context(), `SELECT r.paused,d.execute_consent FROM recurrences r JOIN recurrence_definitions d ON d.recurrence_id=r.id WHERE r.id=$1`, f.routine.ID).Scan(&paused, &consent))
			if !paused || consent {
				t.Fatal("live authority/target change failed to pause and revoke consent")
			}
		})
	}
	t.Run("original_consent_changed", func(t *testing.T) {
		d := setupDispatch(t)
		f := d.fixture
		must(t, f.tx(func(tx pgx.Tx) error {
			out, err := d.e.DispatchTx(t.Context(), tx, f.owner.TenantID, f.run, "", 0)
			if err == nil && !out.Routed {
				t.Fatalf("consent stop positive dispatch: %+v", out)
			}
			return err
		}))
		var g agentsetup.RoutineGrant
		must(t, f.tx(func(tx pgx.Tx) error { return loadActorGrant(t.Context(), tx, d.agentRun, &g) }))
		f.exec(`UPDATE routine_launch_intents SET state='claimed',claimed_at=clock_timestamp() WHERE id=$1`, g.LaunchID)
		f.exec(`UPDATE recurrence_definitions SET execute_consent=false WHERE recurrence_id=$1`, f.routine.ID)
		must(t, d.e.recoverRun(db.AllProjects(t.Context(), "changed consent fixture"), f.owner.TenantID, f.run))
		var paused, runPaused bool
		var stops, held int
		must(t, f.d.Admin.QueryRow(t.Context(), `SELECT c.paused,r.state='paused',
   (SELECT count(*) FROM routine_effect_outbox WHERE run_id=r.id AND kind='routine.stop' AND state='pending'),
   (SELECT count(*) FROM routine_budget_grants WHERE run_id=r.id AND state='held')
   FROM routine_runs r JOIN recurrences c ON c.id=r.recurrence_id WHERE r.id=$1`, f.run).Scan(&paused, &runPaused, &stops, &held))
		if paused || !runPaused || stops != 1 || held != 1 {
			t.Fatalf("changed consent lost the original stop/hold or paused fresh definition: %v %v %d %d", paused, runPaused, stops, held)
		}
	})
	t.Run("bounded_evidence_escalates_once", func(t *testing.T) {
		d := setupDispatch(t)
		f := d.fixture
		f.exec(`INSERT INTO routine_effect_outbox(tenant_id,run_id,effect_key,kind,state,payload)
   SELECT $1,$2,'unknown:'||n,'fixture.external','unknown','{}'::jsonb FROM generate_series(1,257) n`, f.owner.TenantID, f.run)
		for range 4 {
			must(t, d.e.recoverRun(db.AllProjects(t.Context(), "bounded evidence fixture"), f.owner.TenantID, f.run))
		}
		var notices, retained int
		must(t, f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE effect_key='recovery-notice'),count(*) FILTER(WHERE state='unknown') FROM routine_effect_outbox WHERE run_id=$1`, f.run).Scan(&notices, &retained))
		if notices != 1 || retained != 257 {
			t.Fatalf("bounded evidence lost uncertainty or repeated escalation: %d %d", notices, retained)
		}
		must(t, f.tx(func(tx pgx.Tx) error {
			out, err := d.e.DispatchTx(t.Context(), tx, f.owner.TenantID, f.run, "", 0)
			if err == nil && (out.Routed || out.WaitReason != "routine_recovery_unconfirmed") {
				t.Fatalf("unknown outcomes authorized another actor: %+v", out)
			}
			return err
		}))
	})
}
