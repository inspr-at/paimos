// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func intentInput(f *fixture) Input {
	var kind string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(f.t.Context(), `SELECT aeon_seed_work_kinds($1)`, f.p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(f.t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL AND archived_at IS NULL`).Scan(&kind)
	})
	in := f.input()
	in.QueueEach, in.OverlapPolicy = true, "create"
	in.Definition = &Definition{Scope: DefinitionScope{Kind: "project", ProjectID: f.project}, OwnerPrincipalID: f.p.ID,
		Assignment: &Assignment{Goal: "Review the assigned workspace", Role: "build", WorkKindID: kind,
			Sources: []SourceReference{}, AllowedActions: []string{"work.update"},
			RuntimeRequirements: RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"},
			Budget:              DefinitionBudget{Mode: "off"}}}
	return in
}

// Risks: duplicate work/effects after response loss or a scheduler/manual race;
// rollback data loss, tenant/person leakage and stale owner authority. Existing
// receipt/queue guards are reused. PostgreSQL barriers prove actual contention.
func TestRoutineIntentAtomicReplayAndAuthority(t *testing.T) {
	t.Run("atomic rollback and immutable replay", func(t *testing.T) {
		f := setup(t)
		in := intentInput(f)
		in.Definition.Scope = DefinitionScope{Kind: "personal"}
		r := f.create(in)
		actor, err := ensureActor(t.Context(), f.m, f.p.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		abort := errors.New("intent rollback")
		err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
			if _, err := lock(t.Context(), tx, f.p.TenantID, false); err != nil {
				return err
			}
			o, err := occur(t.Context(), tx, actor, r, "manual:lost", f.now, nil, "", "")
			if err != nil {
				return err
			}
			if o.Run == nil || o.Run.State != "pending" || o.Run.WorkOrderID == nil || o.Run.AgentRunID == nil {
				t.Fatalf("missing pending lineage: %+v", o)
			}
			return abort
		})
		if !errors.Is(err, abort) {
			t.Fatal(err)
		}
		f.tx(func(tx pgx.Tx) error {
			var partial bool
			err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM nodes WHERE fields->>'recurrence_id'=$1)
 OR EXISTS(SELECT 1 FROM routine_runs) OR EXISTS(SELECT 1 FROM routine_actions)
 OR EXISTS(SELECT 1 FROM routine_effect_outbox) OR EXISTS(SELECT 1 FROM recurrence_occurrences)
 OR EXISTS(SELECT 1 FROM agent_runs) OR EXISTS(SELECT 1 FROM work_orders)
 OR EXISTS(SELECT 1 FROM events WHERE type='recurrence.occurred')`, r.ID).Scan(&partial)
			if partial || f.get(r.ID).OccurrenceCount != 0 {
				t.Fatal("rollback left partial work/intent/audit")
			}
			return err
		})
		first := f.manual(r.ID, "lost")
		if first.Run == nil || first.Run.State != "pending" {
			t.Fatalf("missing run: %+v", first)
		}
		f.install(New(f.d.App)) // A different server instance loses no receipt.
		if retry := f.manual(r.ID, "lost"); !reflect.DeepEqual(first, retry) {
			t.Fatalf("response-loss replay changed receipt: %+v / %+v", first, retry)
		}
		edit := r.Input
		d := *edit.Definition
		a := *d.Assignment
		a.Goal = "A different assignment"
		d.Assignment, edit.Definition = &a, &d
		f.call(f.p, "PUT", "/api/recurrences/"+r.ID, struct {
			Input
			ExpectedRevision int64 `json:"expected_revision"`
		}{edit, r.Revision}, 200)
		if retry := f.manual(r.ID, "lost"); !reflect.DeepEqual(first.Run, retry.Run) {
			t.Fatal("definition edit changed the original run")
		}
		f.tx(func(tx pgx.Tx) error {
			var frozen bool
			err := tx.QueryRow(t.Context(), `SELECT definition_revision=1 AND owner_principal_id=$2 AND assignment->>'goal'=$3
 AND policy_snapshot#>>'{definition,assignment,goal}'=$3 AND NOT execute_consent AND consent_revision=0 AND execution_principal_id IS NULL
 AND state='pending' AND source_receipt->>'occurrence_key'='manual:lost'
 AND work_node_id=$4 AND work_order_id=$5 AND agent_run_id=$6
 AND (SELECT count(*) FROM routine_actions WHERE run_id=r.id AND state='succeeded' AND kind='work.create')=1
 AND (SELECT count(*) FROM routine_effect_outbox WHERE run_id=r.id AND state='pending')=1
 AND NOT EXISTS(SELECT 1 FROM routine_attempts) AND NOT EXISTS(SELECT 1 FROM agent_runs WHERE status<>'queued')
 FROM routine_runs r WHERE id=$1`, first.Run.ID, f.p.ID, in.Definition.Assignment.Goal, first.NodeID, first.Run.WorkOrderID, first.Run.AgentRunID).Scan(&frozen)
			if err == nil && !frozen {
				t.Fatal("run lineage/authority changed or execution started")
			}
			return err
		})
		// A different person with the output project visible cannot see a personal
		// run, action or outbox. Foreign tenant service visibility cannot either.
		f.tx(func(tx pgx.Tx) error {
			// Retain a real attempt fixture so the RLS assertion cannot pass only
			// because this slice correctly starts no attempts by itself.
			_, err := tx.Exec(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,principal_id,assignment_digest)
 VALUES($1,$2,'synthetic-rls','build',$3,$4)`, f.p.TenantID, first.Run.ID, f.p.ID, first.Run.AssignmentDigest)
			return err
		})
		reader := projectPrincipal(f, "viewer")
		for _, ctx := range []context.Context{tenant.WithPrincipal(t.Context(), reader), dbtest.Seed(t.Context())} {
			tenantID := f.p.TenantID
			if _, ok := tenant.PrincipalFrom(ctx); !ok {
				tenantID = "20000000-0000-4000-8000-000000000001"
			}
			if err := db.InTenant(ctx, f.d.App, tenantID, func(tx pgx.Tx) error {
				for _, table := range []string{"routine_runs", "routine_attempts", "routine_actions", "routine_effect_outbox"} {
					var count int
					if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
						return err
					}
					if count != 0 {
						t.Fatalf("hidden %s leaked", table)
					}
				}
				var events int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='recurrence.occurred' AND after#>>'{run,id}'=$1`, first.Run.ID).Scan(&events); err != nil {
					return err
				}
				if events != 0 {
					t.Fatal("private run receipt leaked through audit events")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		legacy := f.create(f.input())
		if got := f.manual(legacy.ID, "ticket-only"); got.Run != nil {
			t.Fatal("ticket-only recurrence acquired intent")
		}
		skip := intentInput(f)
		skip.OverlapPolicy = "skip"
		s := f.create(skip)
		f.manual(s.ID, "first")
		if got := f.manual(s.ID, "overlap"); got.Outcome != "skipped" || got.Run != nil {
			t.Fatalf("skipped occurrence acquired intent: %+v", got)
		}
	})
	t.Run("scheduler and manual replay contend on one publication", func(t *testing.T) {
		f := setup(t)
		source := f.node("project", nil, "Source project")
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"project_key":"SRC"}'::jsonb WHERE id=$1`, source)
			return err
		})
		in := intentInput(f)
		in.Trigger = Trigger{Kind: "event", Event: "release.published", Filter: &EventFilter{ProjectIDs: []string{source}}}
		r := f.create(in)
		f.tx(func(tx pgx.Tx) error {
			// Synthetic active schedule only. This is not person consent or enable.
			_, err := tx.Exec(t.Context(), `UPDATE recurrences SET paused=false WHERE id=$1`, r.ID)
			return err
		})
		f.now = f.now.Add(time.Hour)
		pub := Publication{ProjectKey: "SRC", ProjectID: source, Name: "Routine fixture", Version: "261010120000.0.0", PublishedAt: f.now}
		f.m.WithHistory([]Publication{pub})
		if _, err := ensureActor(t.Context(), f.m, f.p.TenantID); err != nil {
			t.Fatal(err)
		}
		pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.Contains(sql, "INSERT INTO routine_effect_outbox") })
		scheduler := New(pool).WithHistory([]Publication{pub}).WithReleaseSubscriptionGate(func(context.Context, pgx.Tx, string) (bool, error) { return true, nil })
		scheduler.now = f.m.now
		scheduled := make(chan error, 1)
		go func() { scheduled <- scheduler.RunTenant(ctx, f.p.TenantID) }()
		pid := barrier.Wait(t, ctx)
		manual := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			raw, _ := json.Marshal(map[string]string{"idempotency_key": "manual-replay", "release_key": publicationKey(pub)})
			req := httptest.NewRequest("POST", "/api/recurrences/"+r.ID+"/run-now", strings.NewReader(string(raw)))
			req = req.WithContext(tenant.WithPrincipal(ctx, f.p))
			out := httptest.NewRecorder()
			f.handler.ServeHTTP(out, req)
			manual <- out
		}()
		dbtest.WaitForLock(t, ctx, f.d, pid, "transactionid")
		barrier.Release()
		if err := dbtest.Await(t, ctx, scheduled); err != nil {
			t.Fatal(err)
		}
		out := dbtest.Await(t, ctx, manual)
		var got Occurrence
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != 200 || got.Run == nil {
			t.Fatalf("manual replay: %d %s %v", out.Code, out.Body.String(), err)
		}
		f.run()
		f.tx(func(tx pgx.Tx) error {
			var one bool
			err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM routine_runs)=1 AND (SELECT count(*) FROM routine_actions)=1
 AND (SELECT count(*) FROM routine_effect_outbox)=1 AND (SELECT count(*) FROM agent_runs)=1
 AND (SELECT count(*) FROM recurrence_occurrences)=1 AND EXISTS(SELECT 1 FROM routine_runs WHERE id=$1)`, got.Run.ID).Scan(&one)
			if err == nil && !one {
				t.Fatal("concurrent replay duplicated work or intent")
			}
			return err
		})
	})
	t.Run("owner revocation wins the access fence", func(t *testing.T) {
		f := setup(t)
		owner := projectPrincipal(f, "admin")
		in := intentInput(f)
		in.Definition.OwnerPrincipalID = owner.ID
		r := f.create(in)
		actor, err := ensureActor(t.Context(), f.m, f.p.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		writer, err := f.d.Admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Rollback(t.Context())
		var pid uint32
		if err := writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.p.TenantID); err != nil {
			t.Fatal(err)
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest("POST", "/api/recurrences/"+r.ID+"/run-now", strings.NewReader(`{"idempotency_key":"revoked"}`))
			req = req.WithContext(tenant.WithPrincipal(ctx, f.p))
			out := httptest.NewRecorder()
			f.handler.ServeHTTP(out, req)
			done <- out
		}()
		// The request passed its initial owner/caller check and is now blocked
		// on the final tenant fence, before the membership change commits.
		dbtest.WaitForLock(t, ctx, f.d, pid, "transactionid")
		if _, err := writer.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='viewer') WHERE tenant_id=$1 AND principal_id=$2`, f.p.TenantID, owner.ID); err != nil {
			t.Fatal(err)
		}
		if err := writer.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		out := dbtest.Await(t, ctx, done)
		if out.Code != 403 {
			t.Fatalf("owner revocation: %d %s", out.Code, out.Body.String())
		}
		err = db.InTenant(dbtest.Seed(ctx), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
			if _, err := lock(ctx, tx, f.p.TenantID, false); err != nil {
				return err
			}
			_, err := occur(ctx, tx, actor, r, "time:revoked", f.now, nil, "", "")
			return err
		})
		if !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("scheduler substituted service authority: %v", err)
		}
		f.tx(func(tx pgx.Tx) error {
			var partial bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_runs) OR EXISTS(SELECT 1 FROM recurrence_occurrences)
 OR EXISTS(SELECT 1 FROM nodes WHERE fields->>'recurrence_id'=$1)`, r.ID).Scan(&partial)
			if err == nil && partial {
				t.Fatal("revoked owner created partial work or intent")
			}
			return err
		})
	})
}
