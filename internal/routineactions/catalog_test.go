// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risks: duplicate effects after response loss, false success after rollback,
// and writes to a moved/stale target. This tests the actual mutation/receipt
// transaction after admission, without inventing S08's private verified latch.
func TestRoutineActionsAtomicReplayAndTargets(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{TenantID: "10000000-0000-4000-8000-000000000001", Kind: tenant.Person}
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	var project, parent, run, attempt string
	seed(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'actions','Actions')`, p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, p.TenantID).Scan(&p.ID)
	})
	dbtest.BindRole(t, d, p.TenantID, p.ID, "owner")
	node := func(kind string, parentID *string) string {
		t.Helper()
		var id string
		seed(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Fixture',$2 FROM node_kinds WHERE slug=$3 RETURNING id::text`, p.TenantID, parentID, kind).Scan(&id)
		})
		return id
	}
	project = node("project", nil)
	parent = node("work", &project)
	assignment := json.RawMessage(`{"allowed_actions":["work.create","work.update","knowledge.write"],"sources":[]}`)
	seed(func(tx pgx.Tx) error {
		var recurrence string
		if err := tx.QueryRow(t.Context(), `INSERT INTO recurrences(tenant_id,project_id,parent_id,template,trigger,created_by_principal_id) VALUES($1,$2,$3,'{}','{"kind":"event"}',$4) RETURNING id::text`, p.TenantID, project, parent, p.ID).Scan(&recurrence); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO recurrence_definitions(tenant_id,recurrence_id,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment) VALUES($1,$2,'project',$3,$4,$3,$5,$6)`, p.TenantID, recurrence, project, p.ID, parent, assignment); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO recurrence_occurrences(tenant_id,recurrence_id,occurrence_key,number,scheduled_at,node_id,outcome) VALUES($1,$2,'manual:test',1,now(),$3,'created')`, p.TenantID, recurrence, parent); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO routine_runs(tenant_id,recurrence_id,occurrence_key,definition_revision,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment,assignment_digest,policy_snapshot,policy_digest,work_node_id,source_receipt,state) VALUES($1,$2,'manual:test',1,'project',$3,$4,$3,$5,$6,$7,'{}',$7,$5,'{}','running') RETURNING id::text`, p.TenantID, recurrence, project, p.ID, parent, assignment, routineguard.Digest(assignment)).Scan(&run); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,principal_id,assignment_digest,state) VALUES($1,$2,'build','build',$3,$4,'running') RETURNING id::text`, p.TenantID, run, p.ID, routineguard.Digest(assignment)).Scan(&attempt)
	})
	binding := Binding{RunID: run, OwnerID: p.ID, ProjectID: project, ParentID: parent, Assignment: assignment, ScopeType: "project", ScopeProjectID: project}
	revision := func(id string) int64 {
		t.Helper()
		var at time.Time
		seed(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE id=$1`, id).Scan(&at)
		})
		return at.UnixMicro()
	}
	applyOn := func(ctx context.Context, pool *pgxpool.Pool, in Request, rollback bool) (Receipt, error) {
		var out Receipt
		err := db.InTenant(tenant.WithPrincipal(ctx, p), pool, p.TenantID, func(tx pgx.Tx) error {
			if err := agentpairing.Lock(ctx, tx); err != nil {
				return err
			}
			var err error
			out, err = applyTx(ctx, tx, p, binding, in, strings.Repeat("a", 64), "")
			if err != nil {
				return err
			}
			if rollback {
				return errors.New("injected receipt rollback")
			}
			return nil
		})
		return out, err
	}
	apply := func(in Request, rollback bool) (Receipt, error) { return applyOn(t.Context(), d.App, in, rollback) }
	create := Request{AttemptID: attempt, ActionKey: "create", Kind: "work.create", ProjectID: project, TargetID: parent, ExpectedRevision: revision(parent), WorkCreate: &WorkCreate{Title: "Created once", Body: "Bounded body"}}
	pool, barrier, ctx := dbtest.BarrierPool(t, d.App, func(sql string) bool { return strings.Contains(sql, "UPDATE routine_actions SET result") })
	type answer struct {
		receipt Receipt
		err     error
	}
	firstResult, secondResult := make(chan answer, 1), make(chan answer, 1)
	go func() { out, err := applyOn(ctx, pool, create, false); firstResult <- answer{out, err} }()
	pid := barrier.Wait(t, ctx)
	done := make(chan struct{})
	go func() { out, err := applyOn(ctx, d.App, create, false); secondResult <- answer{out, err}; close(done) }()
	if lock := dbtest.BlockedOrDone(t, ctx, d.Admin, pid, done); lock != "transactionid" {
		t.Fatalf("replay did not overlap at tenant fence: %q", lock)
	}
	barrier.Release()
	a, b := dbtest.Await(t, ctx, firstResult), dbtest.Await(t, ctx, secondResult)
	first, err := a.receipt, a.err
	if err != nil || b.err != nil || a.receipt != b.receipt {
		t.Fatalf("concurrent replay: %+v %+v", a, b)
	}
	retry, err := apply(create, false)
	if err != nil || first != retry {
		t.Fatalf("replay: %+v %+v %v", first, retry, err)
	}
	changed := create
	v := *create.WorkCreate
	v.Body = "Changed request"
	changed.WorkCreate = &v
	_, err = apply(changed, false)
	assertFailure(t, err, 409, "action_key_conflict")
	abort := create
	abort.ActionKey = "rollback"
	abort.ExpectedRevision = revision(parent)
	_, err = apply(abort, true)
	if err == nil || err.Error() != "injected receipt rollback" {
		t.Fatal(err)
	}
	text := "Updated once"
	update := Request{AttemptID: attempt, ActionKey: "update", Kind: "work.update", ProjectID: project, TargetID: first.NodeID, ExpectedRevision: revision(first.NodeID), WorkUpdate: &WorkUpdate{Title: &text}}
	updated, err := apply(update, false)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := apply(update, false); err != nil || again != updated {
		t.Fatalf("update replay: %+v %v", again, err)
	}
	stale := update
	stale.ActionKey = "stale"
	_, err = apply(stale, false)
	assertFailure(t, err, 409, "target_revision_changed")
	state := "in_progress"
	status := Request{AttemptID: attempt, ActionKey: "parent-status", Kind: "work.update", ProjectID: project, TargetID: parent, ExpectedRevision: revision(parent), WorkUpdate: &WorkUpdate{State: &state}}
	_, err = apply(status, false)
	assertFailure(t, err, 409, "parent status follows its children")
	knowledge := Request{AttemptID: attempt, ActionKey: "knowledge", Kind: "knowledge.write", ProjectID: project, TargetID: project, ExpectedRevision: revision(project), KnowledgeWrite: &KnowledgeWrite{Type: "memory", Slug: "findings", Title: "Findings", Body: "Retained findings"}}
	k, err := apply(knowledge, false)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := apply(knowledge, false); err != nil || again != k {
		t.Fatalf("knowledge replay: %+v %v", again, err)
	}
	knowledge.ActionKey = "knowledge-update"
	knowledge.TargetID = k.NodeID
	knowledge.ExpectedRevision = k.Revision
	knowledge.KnowledgeWrite.Body = "New findings"
	ku, err := apply(knowledge, false)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := apply(knowledge, false); err != nil || again != ku {
		t.Fatalf("knowledge update replay: %+v %v", again, err)
	}
	duplicate := knowledge
	duplicate.ActionKey = "duplicate-slug"
	duplicate.TargetID = project
	duplicate.ExpectedRevision = revision(project)
	_, err = apply(duplicate, false)
	assertFailure(t, err, 409, "slug_taken")
	outside := node("work", &project)
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, first.NodeID, outside)
		return err
	})
	escaped := update
	escaped.ActionKey = "outside"
	escaped.ExpectedRevision = revision(first.NodeID)
	_, err = apply(escaped, false)
	assertFailure(t, err, 403, "target_outside_run_scope")
	other := node("project", nil)
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, first.NodeID, other)
		return err
	})
	moved := update
	moved.ActionKey = "moved"
	moved.ExpectedRevision = revision(first.NodeID)
	_, err = apply(moved, false)
	assertFailure(t, err, 409, "target_project_changed")
	seed(func(tx pgx.Tx) error {
		var count, actions, audit int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE title='Created once' AND deleted_at IS NULL`).Scan(&count); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_actions WHERE run_id=$1`, run).Scan(&actions); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE metadata->>'routine_run_id'=$1 AND type IN ('node.created','node.updated','knowledge.created','knowledge.updated')`, run).Scan(&audit); err != nil {
			return err
		}
		// Updated title has replaced the one created record. No aborted second row.
		if count != 0 || actions != 4 || audit != 4 {
			t.Fatalf("partial/duplicate effects: nodes=%d actions=%d audit=%d", count, actions, audit)
		}
		return nil
	})
}
func assertFailure(t *testing.T, err error, status int, message string) {
	t.Helper()
	var e *workorders.Error
	if !errors.As(err, &e) || e.Status != status || e.Error() != message {
		t.Fatalf("got %v want %d %s", err, status, message)
	}
}

// Risk: a generic effect or unbounded payload escaping the fixed catalogue.
func TestRoutineActionCatalogueClosedAndBounded(t *testing.T) {
	in := Request{AttemptID: "10000000-0000-4000-8000-000000000001", ActionKey: "action", Kind: "work.create", ProjectID: "10000000-0000-4000-8000-000000000002", TargetID: "10000000-0000-4000-8000-000000000003", ExpectedRevision: 1, WorkCreate: &WorkCreate{Title: "Valid"}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"survey", "notify", "command", "merge", "deploy", "pr.open", "pipeline.request", "unknown"} {
		v := in
		v.Kind = kind
		if v.Validate() == nil {
			t.Errorf("accepted %s", kind)
		}
	}
	v := in
	v.WorkCreate = &WorkCreate{Title: "Valid", Body: strings.Repeat("x", 32769)}
	if v.Validate() == nil {
		t.Fatal("accepted oversized body")
	}
	v = in
	v.ExpectedRevision = MaxRevision + 1
	if v.Validate() == nil {
		t.Fatal("accepted overflowing revision")
	}
	v = in
	v.WorkUpdate = &WorkUpdate{Body: new(string)}
	if v.Validate() == nil {
		t.Fatal("accepted mixed payload")
	}
	mux := http.NewServeMux()
	New(nil, nil).Mount(mux)
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"command":"deploy"}`, 400}, {strings.Repeat("x", MaxBodyBytes+1), 413}, {`{} {}`, 400}} {
		req := httptest.NewRequest("POST", "/api/routine-runs/10000000-0000-4000-8000-000000000004/actions", strings.NewReader(tc.body)).WithContext(tenant.WithPrincipal(t.Context(), tenant.Principal{ID: in.AttemptID, TenantID: in.ProjectID, Kind: tenant.Person}))
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Fatalf("body boundary: %d %s want %d", out.Code, out.Body.String(), tc.status)
		}
	}
	v = in
	v.ActionKey = "occurrence"
	if v.Validate() == nil {
		t.Fatal("accepted reserved receipt key")
	}
}
