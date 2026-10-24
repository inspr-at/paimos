// SPDX-License-Identifier: AGPL-3.0-only
package assistants

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: policy scopes are mistaken for grants, admissions overspend,
// retries are charged twice, unknown costs become free, or a clock reset lets
// an assistant spend in another tenant. Every instant derives from DB now().
func TestAssistantPolicyAdmissionMatrix(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{TenantID: "10000000-0000-4000-8000-000000000001", Kind: tenant.Person, Name: "Policy owner"}
	var assistant, project, otherProject string
	var now time.Time
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	policy := DefaultPolicy()
	policy.AllowedScopes = []string{"inbox.read", "inbox.send", "nodes.read", "knowledge.write"}
	policy.WeeklyBudget = Budget{"3", "USD"}
	seed(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'assistant-matrix','Assistant matrix')`, p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Policy owner') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,agent_class) VALUES($1,'agent','Matrix assistant','assistant') RETURNING id::text`, p.TenantID).Scan(&assistant); err != nil {
			return err
		}
		for _, id := range []*string{&project, &otherProject} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Policy project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(id); err != nil {
				return err
			}
		}
		policy.AllowedProjects = []string{project}
		raw, _ := json.Marshal(policy)
		if _, err := tx.Exec(t.Context(), `INSERT INTO assistant_profiles(tenant_id,principal_id,owner_person_id,vendor,policy) VALUES($1,$2,$3,'grok',$4)`, p.TenantID, assistant, p.ID, raw); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now)
	})
	check := func(key, projectID, scope, amount string, at time.Time, want string) Decision {
		t.Helper()
		var out Decision
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
				return err
			}
			in := Check{TenantID: p.TenantID, PrincipalID: assistant, ProjectID: projectID, Operation: "matrix", Key: key, Scopes: []string{scope}, Now: at, Charge: true}
			if amount != "unknown" {
				in.Ceiling = &Budget{amount, "USD"}
			}
			var err error
			out, err = EvaluateTx(t.Context(), tx, in)
			if err != nil {
				return err
			}
			return AppendDecisionsTx(t.Context(), tx, p, out)
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Allowed != (want == "") || out.Reason != want {
			t.Fatalf("%s: allowed=%v reason=%q want=%q", key, out.Allowed, out.Reason, want)
		}
		return out
	}
	check("first", project, "knowledge.write", "1", now, "")
	check("scope", project, "nodes.write", "1", now, "assistant_scope_denied: nodes.write")
	check("project", otherProject, "inbox.read", "1", now, "assistant_project_denied")
	check("edge", project, "knowledge.write", "2", now, "")
	check("over", project, "knowledge.write", "0.000001", now, "assistant_weekly_budget_exhausted")
	check("first", project, "knowledge.write", "1", now, "")
	check("next-week", project, "knowledge.write", "1", now.AddDate(0, 0, 7), "")
	check("unknown", project, "inbox.read", "unknown", now.AddDate(0, 0, 7), "")
	check("after-unknown", project, "inbox.read", "0.000001", now.AddDate(0, 0, 7), "assistant_weekly_budget_exhausted")
	seed(func(tx pgx.Tx) error {
		var count int
		var total string
		if err := tx.QueryRow(t.Context(), `SELECT count(*),sum(amount_micro)::text FROM assistant_budget_reservations WHERE principal_id=$1`, assistant).Scan(&count, &total); err != nil {
			return err
		}
		if count != 4 || total != "6000000" {
			t.Fatalf("reservations=%d total=%s", count, total)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM assistant_policy_receipts WHERE principal_id=$1 AND NOT allowed`, assistant).Scan(&count); err != nil {
			return err
		}
		if count != 4 {
			t.Fatalf("denied receipts=%d", count)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='assistant.policy_evaluated' AND after->>'principal_id'=$1 AND NOT (after->>'allowed')::boolean`, assistant).Scan(&count); err != nil {
			return err
		}
		if count != 4 {
			t.Fatalf("denied events=%d", count)
		}
		return CheckApproverTx(t.Context(), tx, p, assistant)
	})
	// A failed acceptance cannot retain its provisional charge or audit.
	sentinel := errors.New("acceptance failed")
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		_, err := EvaluateTx(t.Context(), tx, Check{TenantID: p.TenantID, PrincipalID: assistant, Operation: "rollback", Key: "rollback", Scopes: []string{"inbox.read"}, Charge: true, Ceiling: &Budget{"1", "USD"}, Now: now.AddDate(0, 0, 14)})
		if err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	seed(func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM assistant_budget_reservations WHERE operation_key='rollback'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("rolled-back charge retained")
		}
		return nil
	})
	// Explicit tenant mismatch never reinterprets an assistant in another context.
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		_, err := EvaluateTx(t.Context(), tx, Check{TenantID: "20000000-0000-4000-8000-000000000001", PrincipalID: assistant, Operation: "matrix", Key: "foreign", RequireAssistant: true})
		return err
	})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("cross-tenant: %v", err)
	}
	// Establish real lock contention before releasing the first admission.
	// Both callers would fit alone; the second must observe the committed hold.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type admission struct {
		decision Decision
		err      error
	}
	ready := make(chan struct{})
	release := make(chan struct{})
	waiting := make(chan uint32, 1)
	done := make(chan admission, 2)
	var releaseOnce sync.Once
	releaseHolder := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHolder()
	admit := func(key string, hold bool) {
		var decision Decision
		err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
			if !hold {
				waiting <- tx.Conn().PgConn().PID()
			}
			if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
				return err
			}
			var err error
			decision, err = EvaluateTx(ctx, tx, Check{TenantID: p.TenantID, PrincipalID: assistant, Operation: "concurrent", Key: key, Scopes: []string{"inbox.read"}, Charge: true, Ceiling: &Budget{"2", "USD"}, Now: now.AddDate(0, 0, 14)})
			if err != nil {
				return err
			}
			if hold {
				close(ready)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return AppendDecisionsTx(ctx, tx, p, decision)
		})
		done <- admission{decision, err}
	}
	go admit("concurrent-first", true)
	select {
	case <-ready:
	case out := <-done:
		t.Fatalf("holder failed: %v", out.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go admit("concurrent-second", false)
	var waiter uint32
	select {
	case waiter = <-waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for {
		var blocked bool
		if err := d.Admin.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, waiter).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
	}
	releaseHolder()
	var allowedAdmissions, refusedAdmissions int
	for range 2 {
		select {
		case out := <-done:
			if out.err != nil {
				t.Fatal(out.err)
			}
			if out.decision.Allowed {
				allowedAdmissions++
			} else if out.decision.Reason == "assistant_weekly_budget_exhausted" {
				refusedAdmissions++
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if allowedAdmissions != 1 || refusedAdmissions != 1 {
		t.Fatalf("contended admissions: allowed=%d refused=%d", allowedAdmissions, refusedAdmissions)
	}
	// The reset weekday follows the policy, with no comparison to real-clock rows.
	day := int(now.Weekday())
	policy.WeeklyResetDay = &day
	start, end := Week(now, policy)
	if start.Weekday() != now.Weekday() || start.Hour() != 0 || !end.Equal(start.AddDate(0, 0, 7)) {
		t.Fatal("policy week boundary")
	}
}
