// SPDX-License-Identifier: AGPL-3.0-only
package lanecontrol

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type fixture struct {
	d                                      *dbtest.DB
	p                                      tenant.Principal
	project, lane, agent, profile, account string
	now                                    time.Time
	seq                                    int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), p: tenant.Principal{Kind: tenant.Person}, now: time.Date(2026, 10, 2, 22, 0, 0, 0, time.UTC)}
	ctx := t.Context()
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('lane-enforcement','Lane enforcement') RETURNING id::text`).Scan(&f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	f.tx(t, func(tx pgx.Tx) error {
		for kind, out := range map[string]*string{"person": &f.p.ID, "agent": &f.agent} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,'Lane fixture') RETURNING id::text`, f.p.TenantID, kind).Scan(out); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'autopilot_lane','Lane','LANE','agents',ARRAY[]::text[])`, f.p.TenantID); err != nil {
			return err
		}
		var err error
		f.project, err = f.node(tx, ctx, "project", nil)
		if err != nil {
			return err
		}
		f.lane, err = f.node(tx, ctx, "autopilot_lane", &f.project)
		if err != nil {
			return err
		}
		raw := `{"parallel_limit":2,"budget":{"agent_hours":1},"window":{"timezone":"UTC","days":[1,2,3,4,5,6,7],"start":"21:00","end":"23:00"}}`
		if _, err = tx.Exec(ctx, `INSERT INTO autopilot_lanes(tenant_id,node_id,project_id,owner_principal_id,name,enabled,policy) VALUES($1,$2,$3,$4,'Lane',true,$5)`, f.p.TenantID, f.lane, f.project, f.p.ID, raw); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'lane-fixture','1','codex','openai','test-model','high','strong') RETURNING id::text`, f.p.TenantID).Scan(&f.profile); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,billing_mode,capacity_owner,max_parallel_runs) VALUES($1,'fixture','codex','daemon',$2,'Fixture','subscription',$3,5) RETURNING id::text`, f.p.TenantID, f.agent, f.p.ID).Scan(&f.account); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working','{"cap":2}')`, f.p.TenantID, f.p.ID)
		return err
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "owner")
	var keeper string
	if err := f.d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other owner') RETURNING id::text`, f.p.TenantID).Scan(&keeper); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, f.p.TenantID, keeper, "owner")
	return f
}
func (f *fixture) tx(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) node(tx pgx.Tx, ctx context.Context, kind string, parent *string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Fixture',$2,'{"area":"backend"}' FROM node_kinds k WHERE k.slug=$3 RETURNING id::text`, f.p.TenantID, parent, kind).Scan(&id)
	return id, err
}
func (f *fixture) request(t *testing.T, maximum int64) Reservation {
	t.Helper()
	var in Reservation
	in.LaneID = f.lane
	in.LaneRevision = 1
	in.MaximumMS = maximum
	in.AttemptMS = maximum
	f.tx(t, func(tx pgx.Tx) error {
		ctx := t.Context()
		var err error
		if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&in.DispatchID); err != nil {
			return err
		}
		in.TicketID, err = f.node(tx, ctx, "ticket", &f.project)
		if err != nil {
			return err
		}
		order, err := f.node(tx, ctx, "work_order", &in.TicketID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,assignee_principal_id,status) VALUES($1,$2,$3,$4,'ready')`, f.p.TenantID, order, f.p.ID, f.agent); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id) VALUES($1,$2,$3,$4) RETURNING id::text`, f.p.TenantID, order, f.agent, f.profile).Scan(&in.RunID)
	})
	return in
}
func (f *fixture) reserve(ctx context.Context, in Reservation) error {
	return db.InTenant(dbtest.Seed(ctx), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectMutation(ctx, tx, f.p.TenantID); err != nil {
			return err
		}
		return ReserveTx(ctx, tx, f.p, in, f.now)
	})
}
func (f *fixture) quota(t *testing.T, in Reservation) {
	t.Helper()
	f.tx(t, func(tx pgx.Tx) error {
		var wid string
		ctx := t.Context()
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET account_id=$2 WHERE id=$1`, in.RunID, f.account); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,reserved,capacity_kind,capacity_bucket,capacity_read_at,capacity_source,capacity_allowed) VALUES($1,$2,$3,$4,'requests',100,10,10,'weekly',$5,$6,'harness',true) RETURNING id::text`, f.p.TenantID, f.account, f.now.Add(-time.Hour), f.now.Add(time.Hour), in.RunID, f.now).Scan(&wid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units) VALUES($1,$2,$3,10)`, f.p.TenantID, in.RunID, wid)
		return err
	})
}
func (f *fixture) claim(ctx context.Context, in Reservation, at time.Time) (*Grant, error) {
	var g *Grant
	err := db.InTenant(dbtest.Seed(ctx), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectMutation(ctx, tx, f.p.TenantID); err != nil {
			return err
		}
		var err error
		g, err = ClaimTx(ctx, tx, f.p, in.RunID, Capability, in.RunID, "daemon", "generation", at)
		return err
	})
	return g, err
}
func race(t *testing.T, fn []func() error) []error {
	t.Helper()
	var ready sync.WaitGroup
	ready.Add(len(fn))
	start := make(chan struct{})
	out := make(chan error, len(fn))
	for _, f := range fn {
		go func() { ready.Done(); <-start; out <- f() }()
	}
	ready.Wait()
	close(start)
	errs := []error{}
	for range fn {
		errs = append(errs, <-out)
	}
	return errs
}
func oneWinner(t *testing.T, errs []error) {
	t.Helper()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else {
			var policy *workorders.Error
			if !errors.As(err, &policy) || policy.Status != 409 {
				t.Fatalf("race failed outside admission policy: %v", err)
			}
		}
	}
	if wins != 1 {
		t.Fatalf("want one winner, got %d: %v", wins, errs)
	}
}
func TestLastBudgetBarrier(t *testing.T) {
	f := setup(t)
	a, b := f.request(t, 3600000), f.request(t, 3600000)
	oneWinner(t, race(t, []func() error{func() error { return f.reserve(t.Context(), a) }, func() error { return f.reserve(t.Context(), b) }}))
	f.tx(t, func(tx pgx.Tx) error {
		var held, settled int64
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT held_ms,settled_ms,(SELECT count(*) FROM lane_budget_envelopes) FROM lane_budget_periods`).Scan(&held, &settled, &n); err != nil {
			return err
		}
		if held != 3600000 || settled != 0 || n != 1 {
			t.Fatalf("held=%d settled=%d envelopes=%d", held, settled, n)
		}
		return nil
	})
}
func TestLastSlotBarrierAndUnknownExit(t *testing.T) {
	f := setup(t)
	a, b := f.request(t, 60000), f.request(t, 60000)
	for _, in := range []Reservation{a, b} {
		if err := f.reserve(t.Context(), in); err != nil {
			t.Fatal(err)
		}
		f.quota(t, in)
	}
	// Account routing is advisory for lane starts. Only the atomic claim creates
	// a person/lane slot; two separately routed candidates cannot deadlock the dial.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE user_preferences SET value='{"cap":1}'`)
		return err
	})
	oneWinner(t, race(t, []func() error{
		func() error { _, err := f.claim(t.Context(), a, f.now); return err },
		func() error { _, err := f.claim(t.Context(), b, f.now); return err },
	}))
	f.tx(t, func(tx pgx.Tx) error {
		var run, eid string
		if err := tx.QueryRow(t.Context(), `SELECT run_id::text,envelope_id::text FROM lane_attempt_grants`).Scan(&run, &eid); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='ownership_lost' WHERE id=$1`, run); err != nil {
			return err
		}
		if err := SettleTx(t.Context(), tx, run, nil); err != nil {
			return err
		}
		if err := CloseTx(t.Context(), tx, eid); err == nil {
			t.Fatal("released unknown exit")
		}
		return nil
	})
}
func TestRemainingGrantReplayAndSettlement(t *testing.T) {
	f := setup(t)
	in := f.request(t, 60000)
	in.AttemptMS = 40000
	if err := f.reserve(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	f.quota(t, in)
	g, err := f.claim(t.Context(), in, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if g.RemainingMS != 40000 {
		t.Fatal(g)
	}
	g2, err := f.claim(t.Context(), in, f.now.Add(10*time.Second))
	if err != nil || g2.RemainingMS != 30000 || !g2.ExpiresAt.Equal(g.ExpiresAt) {
		t.Fatalf("replay %+v %v", g2, err)
	}
	f.tx(t, func(tx pgx.Tx) error {
		s := &Settlement{ElapsedMS: 25000, ExitConfirmed: true}
		if err := SettleTx(t.Context(), tx, in.RunID, s); err != nil {
			return err
		}
		if err := SettleTx(t.Context(), tx, in.RunID, s); err != nil {
			return err
		}
		if err := SettleTx(t.Context(), tx, in.RunID, &Settlement{ElapsedMS: 26000, ExitConfirmed: true}); err == nil {
			t.Fatal("divergent settlement accepted")
		}
		return nil
	})
	var retry string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,retry_of_run_id) SELECT tenant_id,work_order_id,agent_principal_id,model_profile_id,id FROM agent_runs WHERE id=$1 RETURNING id::text`, in.RunID).Scan(&retry); err != nil {
			return err
		}
		if err := GuardRunTx(t.Context(), tx, retry); err == nil {
			t.Fatal("unbound retry escaped")
		}
		return BindRunTx(t.Context(), tx, in.DispatchID, retry)
	})
	next := in
	next.RunID = retry
	f.quota(t, next)
	// Mark the confirmed exited predecessor terminal so it cannot occupy a slot.
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed' WHERE id=$1;`, in.RunID)
		return err
	})
	nextGrant, err := f.claim(t.Context(), next, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if nextGrant.RemainingMS != 35000 {
		t.Fatal("retry replenished envelope", nextGrant)
	}
	f.tx(t, func(tx pgx.Tx) error {
		if err := SettleTx(t.Context(), tx, retry, &Settlement{ElapsedMS: 10000, ExitConfirmed: true}); err != nil {
			return err
		}
		if err := CloseTx(t.Context(), tx, in.DispatchID); err != nil {
			return err
		}
		return CloseTx(t.Context(), tx, in.DispatchID)
	})
	f.tx(t, func(tx pgx.Tx) error {
		var held, settled int64
		if err := tx.QueryRow(t.Context(), `SELECT held_ms,settled_ms FROM lane_budget_periods`).Scan(&held, &settled); err != nil {
			return err
		}
		if held != 0 || settled != 35000 {
			t.Fatalf("held=%d settled=%d", held, settled)
		}
		return nil
	})
}
func TestRefusalConditions(t *testing.T) {
	for _, mode := range []string{"unknown_billing", "api_billing", "quota_reserve", "unknown_quota", "target_unset", "revoked", "paused", "revision", "window_end", "capability"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			in := f.request(t, 60000)
			if err := f.reserve(t.Context(), in); err != nil {
				t.Fatal(err)
			}
			f.quota(t, in)
			f.tx(t, func(tx pgx.Tx) error {
				var q string
				switch mode {
				case "unknown_billing":
					q = `UPDATE agent_accounts SET billing_mode='unknown'`
				case "api_billing":
					q = `UPDATE agent_accounts SET billing_mode='api'`
				case "quota_reserve":
					q = `UPDATE account_allowance_windows SET used=65`
				case "unknown_quota":
					q = `UPDATE account_allowance_windows SET capacity_read_at=NULL`
				case "target_unset":
					q = `UPDATE user_preferences SET value='{}'`
				case "revoked":
					q = `DELETE FROM role_bindings WHERE principal_id='` + f.p.ID + `'`
				case "paused":
					q = `UPDATE autopilot_lanes SET paused=true`
				case "revision":
					q = `UPDATE autopilot_lanes SET revision=2`
				}
				if q != "" {
					_, err := tx.Exec(t.Context(), q)
					return err
				}
				return nil
			})
			var err error
			if mode == "capability" {
				err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
					if e := authz.LockProjectMutation(t.Context(), tx, f.p.TenantID); e != nil {
						return e
					}
					_, e := ClaimTx(t.Context(), tx, f.p, in.RunID, "", in.RunID, "daemon", "generation", f.now)
					return e
				})
			} else {
				now := f.now
				if mode == "window_end" {
					now = now.Add(time.Hour)
				}
				_, err = f.claim(t.Context(), in, now)
			}
			if err == nil {
				t.Fatal("unsafe admission succeeded")
			}
			expected := map[string]string{"unknown_billing": "known subscription", "api_billing": "known subscription", "quota_reserve": "retain quota reserve", "unknown_quota": "measured quota", "target_unset": "slots unavailable", "paused": "paused", "revision": "revision changed", "window_end": "window closed", "capability": "lane-capable"}
			if mode == "revoked" {
				if !errors.Is(err, authz.ErrForbidden) {
					t.Fatalf("wrong revocation refusal: %v", err)
				}
			} else if !strings.Contains(err.Error(), expected[mode]) {
				t.Fatalf("wrong refusal for %s: %v", mode, err)
			}
			f.tx(t, func(tx pgx.Tx) error {
				var n int
				var held int64
				if e := tx.QueryRow(t.Context(), `SELECT count(*) FROM lane_attempt_grants`).Scan(&n); e != nil {
					return e
				}
				if e := tx.QueryRow(t.Context(), `SELECT held_ms FROM lane_budget_periods`).Scan(&held); e != nil {
					return e
				}
				if n != 0 || held != 60000 {
					t.Fatalf("refusal changed accounting: %d %d", n, held)
				}
				return nil
			})
		})
	}
}

func TestOwnerRevocationFencesConcurrentClaim(t *testing.T) {
	f := setup(t)
	in := f.request(t, 60000)
	if err := f.reserve(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	f.quota(t, in)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	locked := make(chan struct{})
	commit := make(chan struct{})
	revoked := make(chan error, 1)
	go func() {
		revoked <- db.InTenant(dbtest.Seed(ctx), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
			if err := authz.LockProjectMutation(ctx, tx, f.p.TenantID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.p.ID); err != nil {
				return err
			}
			close(locked)
			select {
			case <-commit:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	begun := make(chan struct{})
	claimed := make(chan error, 1)
	go func() {
		claimed <- db.InTenant(dbtest.Seed(ctx), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
			close(begun)
			if err := authz.LockProjectMutation(ctx, tx, f.p.TenantID); err != nil {
				return err
			}
			_, err := ClaimTx(ctx, tx, f.p, in.RunID, Capability, in.RunID, "daemon", "generation", f.now)
			return err
		})
	}()
	<-begun
	close(commit)
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if err := <-claimed; !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("claim after revocation: %v", err)
	}
}

func TestSlotsDeduplicateSessionsAcrossVisibilityAndKeepUnknownWorkers(t *testing.T) {
	f := setup(t)
	in := f.request(t, 60000)
	other := f.request(t, 60000)
	f.tx(t, func(tx pgx.Tx) error {
		ctx := t.Context()
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET status='running',account_id=$2 WHERE id=$1`, other.RunID, f.account); err != nil {
			return err
		}
		// One managed run with two session generations still occupies one slot.
		for i := 0; i < 2; i++ {
			if _, err := tx.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,harness,host,management,role,ref_digest,lease_digest,owner_principal_id) VALUES($1,$2,$3,$4,'codex','fixture','managed','worker',decode($5,'hex'),decode('02','hex'),$6)`, f.p.TenantID, f.project, f.agent, other.RunID, []string{"01", "03"}[i], f.p.ID); err != nil {
				return err
			}
		}
		// Unknown unmanaged ownership consumes a conservative slot even with a stale
		// heartbeat; it cannot be inferred to have exited from elapsed wall time.
		if _, err := tx.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,phase) VALUES($1,$2,$3,'claude','fixture','unmanaged','worker',decode('04','hex'),decode('05','hex'),'working')`, f.p.TenantID, f.project, f.agent); err != nil {
			return err
		}
		// Internal content-free counts include hidden projects and restore the caller.
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','{}',true)`); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM aeon_lane_working_slots($1,$2)`, f.p.ID, in.RunID).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			t.Fatalf("slot count %d; expected one managed plus one unknown", n)
		}
		var visibility string
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.visible_projects')`).Scan(&visibility); err != nil {
			return err
		}
		if visibility != "{}" {
			t.Fatal("visibility leaked")
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET status='completed' WHERE id=$1`, other.RunID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM aeon_lane_working_slots($1,$2)`, f.p.ID, in.RunID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("finished process waiting for a person kept a slot: %d", n)
		}
		return nil
	})
}
